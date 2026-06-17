package shared

const ProcessStateChangeEventID = 0x01
const ConfigFileChangedEventID = 0x03
const ActivityLogEventID = 0x05
const ModelPreloadedEventID = 0x06
const InFlightRequestsEventID = 0x07
const MemoryGuardTrippedEventID = 0x08

// ProcessStateChangeEvent is emitted whenever a process transitions between
// lifecycle states. States are carried as strings so this package stays a leaf
// (no import of internal/process).
type ProcessStateChangeEvent struct {
	ProcessName string
	OldState    string
	NewState    string
}

func (e ProcessStateChangeEvent) Type() uint32 {
	return ProcessStateChangeEventID
}

type ReloadingState int

const (
	ReloadingStateStart ReloadingState = iota
	ReloadingStateEnd
)

type ConfigFileChangedEvent struct {
	State ReloadingState
}

func (e ConfigFileChangedEvent) Type() uint32 {
	return ConfigFileChangedEventID
}

type ModelPreloadedEvent struct {
	ModelName string
	Success   bool
}

func (e ModelPreloadedEvent) Type() uint32 {
	return ModelPreloadedEventID
}

type InFlightRequestsEvent struct {
	Total int
}

func (e InFlightRequestsEvent) Type() uint32 {
	return InFlightRequestsEventID
}

// MemoryGuardTrippedEvent is emitted when the host memory guard unloads all
// local models because available memory stayed below the configured threshold.
// It carries the memory snapshot and the unloaded model IDs so clients can
// surface a clear error rather than seeing models silently disappear.
type MemoryGuardTrippedEvent struct {
	AvailableMB    int      `json:"available_mb"`
	TotalMB        int      `json:"total_mb"`
	ThresholdPct   float64  `json:"threshold_pct"`
	UnloadedModels []string `json:"unloaded_models"`
}

func (e MemoryGuardTrippedEvent) Type() uint32 {
	return MemoryGuardTrippedEventID
}
