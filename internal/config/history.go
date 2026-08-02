package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Retention defaults and backstop for config-history/. See RuntimeState and
// SnapshotConfig: every successful config transition snapshots the config it
// is about to replace, so any actor's mess (a bad hand edit, an agent's
// wholesale replacement, a corrupt PATCH) is one rollback call away instead
// of a manual forensic recovery.
const (
	DefaultHistoryKeep       = 20
	DefaultHistoryMaxAgeDays = 30
	// HistoryHardCap bounds total retained snapshots regardless of Keep/
	// MaxAgeDays — a backstop against unbounded disk growth if an operator
	// sets MaxAgeDays very high, not a limit users are expected to hit.
	HistoryHardCap = 200
	historyDirName = "config-history"
)

// ConfigHistoryConfig controls the config-history/ snapshot directory that
// llama-skein maintains beside the config file.
type ConfigHistoryConfig struct {
	// Keep is the minimum number of most-recent snapshots retained regardless
	// of age. nil (unset) uses DefaultHistoryKeep. 0 disables snapshotting of
	// NEW changes — it does not delete snapshots already on disk.
	Keep *int `yaml:"keep"`
	// MaxAgeDays additionally retains any snapshot younger than this many
	// days, even beyond Keep. nil (unset) uses DefaultHistoryMaxAgeDays. <= 0
	// (explicitly set) disables the age-based extension, leaving only Keep.
	MaxAgeDays *int `yaml:"maxAgeDays"`
}

func (c ConfigHistoryConfig) keep() int {
	if c.Keep == nil {
		return DefaultHistoryKeep
	}
	return *c.Keep
}

func (c ConfigHistoryConfig) maxAgeDays() int {
	if c.MaxAgeDays == nil {
		return DefaultHistoryMaxAgeDays
	}
	return *c.MaxAgeDays
}

// HistoryEntry describes one retained snapshot, as returned by ListHistory.
type HistoryEntry struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Actor   string    `json:"actor"`
	Summary string    `json:"summary,omitempty"`
}

type historyFile struct {
	HistoryEntry
	path     string
	metaPath string
}

func historyDir(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), historyDirName)
}

// SnapshotConfig writes data (the config content about to be replaced) into
// config-history/ beside configFile, tagged with actor/summary, then prunes
// the directory to hist's retention policy. A nil/empty data is a no-op —
// there is nothing to archive the first time a config is ever created.
// hist.keep() == 0 also short-circuits: snapshotting is disabled, but
// existing snapshots are left alone (disabling is not the same as deleting
// history the operator may still want).
func SnapshotConfig(configFile string, hist ConfigHistoryConfig, actor, summary string, data []byte) error {
	if hist.keep() == 0 || len(data) == 0 {
		return nil
	}
	dir := historyDir(configFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config history: %w", err)
	}

	id, err := uniqueSnapshotID(dir, time.Now().UTC())
	if err != nil {
		return err
	}
	base := filepath.Join(dir, id)
	if err := os.WriteFile(base+".yaml", data, 0o644); err != nil {
		return fmt.Errorf("config history: write snapshot: %w", err)
	}
	entry := HistoryEntry{ID: id, Time: time.Now().UTC(), Actor: actor, Summary: summary}
	metaData, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("config history: marshal metadata: %w", err)
	}
	if err := os.WriteFile(base+".json", metaData, 0o644); err != nil {
		return fmt.Errorf("config history: write metadata: %w", err)
	}
	return pruneHistory(dir, hist)
}

// uniqueSnapshotID returns a UTC-timestamp-based ID guaranteed not to
// collide with an existing snapshot in dir (two changes landing within the
// same nanosecond tick, e.g. under test, get a numeric suffix).
func uniqueSnapshotID(dir string, t time.Time) (string, error) {
	base := t.Format("20060102T150405.000000000Z")
	id := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, id+".yaml")); errors.Is(err, os.ErrNotExist) {
			return id, nil
		} else if err != nil {
			return "", fmt.Errorf("config history: %w", err)
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// ListHistory returns retained snapshots, newest first.
func ListHistory(configFile string) ([]HistoryEntry, error) {
	files, err := listHistoryFiles(historyDir(configFile))
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, len(files))
	for i, f := range files {
		out[i] = f.HistoryEntry
	}
	return out, nil
}

func listHistoryFiles(dir string) ([]historyFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("config history: %w", err)
	}
	var files []historyFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".yaml")
		hf := historyFile{
			HistoryEntry: HistoryEntry{ID: id},
			path:         filepath.Join(dir, e.Name()),
			metaPath:     filepath.Join(dir, id+".json"),
		}
		if metaData, err := os.ReadFile(hf.metaPath); err == nil {
			_ = json.Unmarshal(metaData, &hf.HistoryEntry)
			hf.ID = id // metadata should already agree, but the filename is authoritative
		}
		if hf.Time.IsZero() {
			if info, err := e.Info(); err == nil {
				hf.Time = info.ModTime().UTC()
			}
		}
		files = append(files, hf)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Time.After(files[j].Time) })
	return files, nil
}

// pruneHistory removes snapshots beyond the retention policy: files are kept
// if they are among the `keep` most recent, OR younger than `maxAgeDays| —
// whichever set is larger — but never more than HistoryHardCap total.
func pruneHistory(dir string, hist ConfigHistoryConfig) error {
	files, err := listHistoryFiles(dir) // newest first
	if err != nil {
		return err
	}
	keep := hist.keep()
	maxAge := hist.maxAgeDays()
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAge)

	retained := 0
	for i, f := range files {
		keepThis := retained < HistoryHardCap && (i < keep || (maxAge > 0 && f.Time.After(cutoff)))
		if keepThis {
			retained++
			continue
		}
		if err := removeHistoryFile(f); err != nil {
			return err
		}
	}
	return nil
}

func removeHistoryFile(f historyFile) error {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config history: prune: %w", err)
	}
	if err := os.Remove(f.metaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config history: prune: %w", err)
	}
	return nil
}

// ErrHistoryNotFound is returned by RollbackConfig when ref does not match
// any retained snapshot.
var ErrHistoryNotFound = errors.New("config history: snapshot not found")

// RollbackConfig overwrites configFile with the content of snapshot ref. ref
// is matched against enumerated snapshot IDs only — it is never joined into
// a filesystem path, so a malicious or malformed ref cannot escape the
// history directory.
//
// This does not itself snapshot the content it replaces: a rollback is,
// from the config file's point of view, just another write, and the normal
// reload path (RuntimeState + the reload pass — see runtime_state.go)
// already snapshots whatever was active immediately before ANY successful
// transition. Callers are expected to trigger a reload afterward, exactly
// as every other config-mutating handler does; that reload is what makes
// the rollback itself undoable, consistent with how every other write is
// covered, with no special-casing here.
func RollbackConfig(configFile string, ref string) error {
	dir := historyDir(configFile)
	files, err := listHistoryFiles(dir)
	if err != nil {
		return err
	}
	var target *historyFile
	for i := range files {
		if files[i].ID == ref {
			target = &files[i]
			break
		}
	}
	if target == nil {
		return ErrHistoryNotFound
	}
	data, err := os.ReadFile(target.path)
	if err != nil {
		return fmt.Errorf("config history: read snapshot: %w", err)
	}
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		return fmt.Errorf("config history: write restored config: %w", err)
	}
	return nil
}

// MigrateLegacyBackups sweeps ad-hoc "<configFile>.bak*" files sitting beside
// configFile into config-history/ once, tagged with actor "legacy-bak", and
// removes the originals so the config directory converges on config-history/
// as the single place history lives. Safe to call on every startup: once a
// .bak file is migrated it no longer exists to match again. Returns the
// number of files migrated.
func MigrateLegacyBackups(configFile string, hist ConfigHistoryConfig) (int, error) {
	baseDir := filepath.Dir(configFile)
	baseName := filepath.Base(configFile)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0, fmt.Errorf("config history: legacy migration: %w", err)
	}
	dir := historyDir(configFile)
	migrated := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == baseName || !strings.HasPrefix(name, baseName+".bak") {
			continue
		}
		srcPath := filepath.Join(baseDir, name)
		data, err := os.ReadFile(srcPath)
		if err != nil {
			continue // best-effort: an unreadable legacy file is skipped, not fatal
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return migrated, fmt.Errorf("config history: legacy migration: %w", err)
		}
		id, err := uniqueSnapshotID(dir, info.ModTime().UTC())
		if err != nil {
			return migrated, err
		}
		if err := os.WriteFile(filepath.Join(dir, id+".yaml"), data, 0o644); err != nil {
			continue
		}
		entry := HistoryEntry{ID: id, Time: info.ModTime().UTC(), Actor: "legacy-bak", Summary: "migrated from " + name}
		metaData, _ := json.MarshalIndent(entry, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, id+".json"), metaData, 0o644)
		_ = os.Remove(srcPath)
		migrated++
	}
	if migrated > 0 {
		if err := pruneHistory(dir, hist); err != nil {
			return migrated, err
		}
	}
	return migrated, nil
}
