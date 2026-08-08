package perf

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/ring"
)

var (
	ErrNotImplemented = errors.New("Not Implemented")
	ErrNoGpuTool      = errors.New("no GPU monitoring tool available")
)

type Monitor struct {
	mutex   sync.RWMutex
	log     *logmon.Monitor
	conf    config.PerformanceConfig
	sysRing ring.Buffer[SysStat]
	gpuRing ring.Buffer[[]GpuStat]

	stopCtx    context.Context
	stopCancel context.CancelFunc

	sysListeners map[chan SysStat]struct{}
	gpuListeners map[chan []GpuStat]struct{}

	// GPU telemetry readiness. gpuSampled is closed when the first GPU
	// snapshot lands; gpuAbsent when this host turns out to have no usable GPU
	// source (or its source died before producing anything). Together they let
	// AwaitFirstSample — and any consumer reading a budget — tell "telemetry is
	// still warming up" apart from "there is nothing to wait for". That
	// distinction is load-bearing: an unsampled monitor reports zeros, and a
	// zero budget is indistinguishable from a measured one. Recreated by every
	// Start, since a config reload stops and restarts sampling. nil until the
	// first Start.
	gpuSampled     chan struct{}
	gpuSampledOnce *sync.Once
	gpuAbsent      chan struct{}
	gpuAbsentOnce  *sync.Once
}

func ringCapacity(c config.PerformanceConfig) int {
	n := int(time.Hour / c.Every)
	if n < 1 {
		n = 1
	}
	return n
}

func New(c config.PerformanceConfig, logger *logmon.Monitor) (*Monitor, error) {

	if c.Every < 100*time.Millisecond {
		c.Every = 100 * time.Millisecond
	}

	if logger == nil {
		return nil, errors.New("logger is required")
	}

	capacity := ringCapacity(c)
	return &Monitor{
		conf:         c,
		log:          logger,
		sysRing:      ring.NewBuffer[SysStat](capacity),
		gpuRing:      ring.NewBuffer[[]GpuStat](capacity),
		sysListeners: make(map[chan SysStat]struct{}),
		gpuListeners: make(map[chan []GpuStat]struct{}),
	}, nil
}

func (m *Monitor) Stop() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.stopCancel == nil {
		return
	}
	m.stopCancel()
	m.stopCancel = nil
}

// UpdateConfig updates the monitor configuration and restarts if changed.
func (m *Monitor) UpdateConfig(newConf config.PerformanceConfig) {
	m.mutex.RLock()
	changed := m.conf != newConf
	m.mutex.RUnlock()

	if !changed {
		return
	}

	m.Stop()
	m.mutex.Lock()
	m.conf = newConf
	capacity := ringCapacity(newConf)
	m.sysRing = ring.NewBuffer[SysStat](capacity)
	m.gpuRing = ring.NewBuffer[[]GpuStat](capacity)
	m.mutex.Unlock()
	if !newConf.Disabled {
		m.Start()
	}
}

// Subscribe returns channels to listen to system and GPU stats.
func (m *Monitor) Subscribe() (chan SysStat, chan []GpuStat, func()) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	sysChan := make(chan SysStat, 1)
	gpuChan := make(chan []GpuStat, 1)

	m.sysListeners[sysChan] = struct{}{}
	m.gpuListeners[gpuChan] = struct{}{}

	unsub := func() {
		m.mutex.Lock()
		defer m.mutex.Unlock()
		delete(m.sysListeners, sysChan)
		delete(m.gpuListeners, gpuChan)
	}

	return sysChan, gpuChan, unsub
}

func (m *Monitor) Start() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.stopCancel != nil {
		return
	}

	m.stopCtx, m.stopCancel = context.WithCancel(context.Background())
	m.gpuSampled, m.gpuSampledOnce = make(chan struct{}), &sync.Once{}
	m.gpuAbsent, m.gpuAbsentOnce = make(chan struct{}), &sync.Once{}

	// Read host memory NOW rather than at the first tick. Everything that
	// budgets memory (fit, the memory guard, placement) reads the latest
	// snapshot, and an empty ring reads as "0 MB total, 0 MB available" — a
	// figure indistinguishable from a measured zero. Boot-time placement
	// planning believed it: on z4 (2026-08-08) a 91 GB hybrid model was
	// planned blind and then refused as unfittable on every container restart,
	// with ~102 GB genuinely available, until a manual config reload re-planned
	// it against a warm sampler. This costs exactly what the tick would have
	// cost `Every` seconds later — one /proc (or sysctl) read.
	if s, err := ReadSysStats(); err == nil {
		m.pushSysLocked(s)
	} else if !errors.Is(err, ErrNotImplemented) {
		m.log.Errorf("failed to read initial sys stats: %s", err.Error())
	}

	go func() {
		tick := time.NewTicker(m.conf.Every)
		defer tick.Stop()
		for {
			select {
			case <-m.stopCtx.Done():
				return
			case <-tick.C:
				s, err := ReadSysStats()
				if err != nil {
					if err != ErrNotImplemented {
						m.log.Errorf("failed to read sys stats: %s", err.Error())
					}
					continue
				}
				m.mutex.Lock()
				m.pushSysLocked(s)
				m.mutex.Unlock()
			}
		}
	}()

	go func() {
		gpuCh, err := getGpuStats(m.stopCtx, m.conf.Every, m.log)
		if err != nil {
			if errors.Is(err, ErrNotImplemented) || errors.Is(err, ErrNoGpuTool) {
				m.log.Infof("GPU monitoring not available: %s", err.Error())
			} else {
				m.log.Errorf("failed to initialize GPU monitoring: %s", err.Error())
			}
			// No source at all: consumers waiting on the first sample must be
			// released rather than left waiting for a snapshot that can never
			// come. This is also what tells them the host has no VRAM budget
			// to find, as opposed to one not measured yet.
			m.markGPUAbsent()
			return
		}

		for {
			select {
			case <-m.stopCtx.Done():
				return
			case g, ok := <-gpuCh:
				if !ok {
					m.log.Errorf("failed reading from gpuCh - stopping read goroutine")
					m.markGPUAbsent()
					return
				}
				m.mutex.Lock()
				m.gpuRing.Push(g)
				for l := range m.gpuListeners {
					select {
					case l <- g:
					default:
					}
				}
				m.mutex.Unlock()
				m.markGPUSampled()
			}
		}
	}()
}

// pushSysLocked records a sys snapshot and fans it out to subscribers. The
// caller must hold m.mutex.
func (m *Monitor) pushSysLocked(s SysStat) {
	m.sysRing.Push(s)
	for l := range m.sysListeners {
		select {
		case l <- s:
		default:
		}
	}
}

func (m *Monitor) markGPUSampled() {
	m.mutex.RLock()
	once, ch := m.gpuSampledOnce, m.gpuSampled
	m.mutex.RUnlock()
	if once != nil {
		once.Do(func() { close(ch) })
	}
}

func (m *Monitor) markGPUAbsent() {
	m.mutex.RLock()
	once, ch := m.gpuAbsentOnce, m.gpuAbsent
	m.mutex.RUnlock()
	if once != nil {
		once.Do(func() { close(ch) })
	}
}

// AwaitFirstSample blocks until this host's hardware picture is readable —
// host memory (which Start samples synchronously) plus either a first GPU
// snapshot or the verdict that this host has no GPU telemetry at all — or
// until timeout. It reports whether the picture is complete.
//
// Use it before any decision that budgets memory and is not revisited: the
// alternative is reading an unsampled monitor's zeros as measured figures,
// which is how boot-time placement came to refuse a model that fits (z4,
// 2026-08-08). Returning false is not fatal — it means "still unknown", and
// callers must fail open on that as they would on any unknown budget.
func (m *Monitor) AwaitFirstSample(timeout time.Duration) bool {
	m.mutex.RLock()
	sampled, absent := m.gpuSampled, m.gpuAbsent
	m.mutex.RUnlock()

	// Never started, so nothing is on its way; report what we already have.
	if sampled == nil || absent == nil {
		return m.haveHostMemory()
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-sampled:
	case <-absent:
	case <-timer.C:
		return false
	}
	return m.haveHostMemory()
}

func (m *Monitor) haveHostMemory() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.sysRing.Len() > 0
}

// GPUTelemetryAbsent reports that this host has been found to have no usable
// GPU telemetry source. Only true once the probe has actually run, so callers
// can distinguish a host with no GPU (system RAM IS the budget) from GPU
// telemetry that has not arrived yet (the budget is simply unknown) — the two
// look identical in an empty GPU ring.
func (m *Monitor) GPUTelemetryAbsent() bool {
	m.mutex.RLock()
	ch := m.gpuAbsent
	m.mutex.RUnlock()
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Current returns a copy of the current log of system and GPU stats.
func (m *Monitor) Current() ([]SysStat, []GpuStat) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	sysStats := m.sysRing.Slice()

	snapshots := m.gpuRing.Slice()
	var gpuStats []GpuStat
	for _, snapshot := range snapshots {
		gpuStats = append(gpuStats, snapshot...)
	}
	return sysStats, gpuStats
}

func ReadSysStats() (SysStat, error) {
	s, err := readSysStats()
	if err != nil {
		return s, err
	}
	return applyEffectiveMemoryLimit(s, EffectiveMemoryLimit), nil
}

// applyEffectiveMemoryLimit fills the effective-memory fields from the cgroup
// limit resolver. Fail-open: without an applicable limit the effective
// figures equal the raw ones. Swap is never part of any effective figure.
func applyEffectiveMemoryLimit(s SysStat, resolve func() (memoryLimit, bool)) SysStat {
	s.MemEffectiveTotalMB = s.MemTotalMB
	s.MemEffectiveAvailableMB = s.MemAvailableMB
	s.MemLimitSource = "none"

	lim, ok := resolve()
	if !ok {
		return s
	}
	const toMB = 1024 * 1024
	limitMB := int(lim.LimitBytes / toMB)
	// A limit above physical RAM constrains nothing (common cgroup default
	// on bare metal); report it as no limit.
	if limitMB <= 0 || limitMB > s.MemTotalMB {
		return s
	}
	s.MemLimitSource = lim.Source
	s.MemEffectiveTotalMB = limitMB
	remainMB := limitMB - int(lim.UsageBytes/toMB)
	if remainMB < 0 {
		remainMB = 0
	}
	if remainMB < s.MemEffectiveAvailableMB {
		s.MemEffectiveAvailableMB = remainMB
	}
	return s
}

func GetGpuStats(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	return getGpuStats(ctx, every, logger)
}
