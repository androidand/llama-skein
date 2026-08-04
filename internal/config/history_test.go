package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHistory_SnapshotThenList(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")

	if err := SnapshotConfig(cfgFile, ConfigHistoryConfig{}, "test", "first", []byte("models: {}\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := ListHistory(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(entries))
	}
	if entries[0].Actor != "test" || entries[0].Summary != "first" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestHistory_EmptyDataIsNoOp(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")

	if err := SnapshotConfig(cfgFile, ConfigHistoryConfig{}, "test", "", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := ListHistory(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 snapshots for nil data, got %d", len(entries))
	}
}

func TestHistory_KeepZeroDisablesNewSnapshots(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")
	zero := 0
	hist := ConfigHistoryConfig{Keep: &zero}

	if err := SnapshotConfig(cfgFile, hist, "test", "", []byte("x")); err != nil {
		t.Fatal(err)
	}
	entries, err := ListHistory(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("keep=0 must disable snapshotting, got %d entries", len(entries))
	}
}

// snapshotAt writes a snapshot pair directly with a controlled timestamp, so
// retention tests don't depend on real wall-clock spacing between calls.
func snapshotAt(t *testing.T, dir string, when time.Time, actor string) {
	t.Helper()
	histDir := filepath.Join(dir, historyDirName)
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := when.Format("20060102T150405.000000000Z")
	if err := os.WriteFile(filepath.Join(histDir, id+".yaml"), []byte("models: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := HistoryEntry{ID: id, Time: when, Actor: actor}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(histDir, id+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHistory_RetentionKeepsNewestNAndAnyWithinMaxAge(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	// 5 snapshots: 3 recent (within maxAge), 2 ancient (outside both keep and maxAge).
	snapshotAt(t, dir, now.Add(-1*time.Hour), "a")
	snapshotAt(t, dir, now.Add(-2*time.Hour), "b")
	snapshotAt(t, dir, now.Add(-3*time.Hour), "c")
	snapshotAt(t, dir, now.AddDate(0, 0, -60), "old1")
	snapshotAt(t, dir, now.AddDate(0, 0, -90), "old2")

	keep, maxAge := 2, 30
	hist := ConfigHistoryConfig{Keep: &keep, MaxAgeDays: &maxAge}
	if err := pruneHistory(historyDir(cfgFile), hist); err != nil {
		t.Fatal(err)
	}

	entries, err := ListHistory(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 retained (2 newest + 1 more within 30d... wait, only the 3 recent are within 30d), got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Actor == "old1" || e.Actor == "old2" {
			t.Errorf("ancient snapshot %s should have been pruned", e.Actor)
		}
	}
}

func TestHistory_HardCapNeverExceeded(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	now := time.Now().UTC()

	// All within maxAgeDays AND all "recent" — without a hard cap this set
	// would all be retained.
	for i := 0; i < HistoryHardCap+25; i++ {
		snapshotAt(t, dir, now.Add(-time.Duration(i)*time.Minute), "actor")
	}

	keep, maxAge := 5, 3650 // effectively unbounded age window
	hist := ConfigHistoryConfig{Keep: &keep, MaxAgeDays: &maxAge}
	if err := pruneHistory(historyDir(cfgFile), hist); err != nil {
		t.Fatal(err)
	}
	entries, err := ListHistory(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != HistoryHardCap {
		t.Fatalf("want exactly %d (hard cap), got %d", HistoryHardCap, len(entries))
	}
}

func TestHistory_RollbackRestoresSnapshotContent(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")

	if err := SnapshotConfig(cfgFile, ConfigHistoryConfig{}, "test", "good state", []byte("models:\n  good: {}\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := ListHistory(cfgFile)
	if err != nil || len(entries) != 1 {
		t.Fatalf("setup: want 1 snapshot, got %d, err=%v", len(entries), err)
	}
	goodID := entries[0].ID

	// simulate a bad overwrite of the live config
	if err := os.WriteFile(cfgFile, []byte("models:\n  bad: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RollbackConfig(cfgFile, goodID); err != nil {
		t.Fatal(err)
	}

	restored, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "models:\n  good: {}\n" {
		t.Errorf("rollback did not restore the snapshot content, got: %q", restored)
	}
}

func TestHistory_RollbackUnknownRefFails(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")
	err := RollbackConfig(cfgFile, "does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
}

func TestHistory_RollbackRefIsNeverUsedAsAPath(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")
	// A path-traversal-shaped ref must be rejected as "not found", never
	// resolved relative to the history directory.
	err := RollbackConfig(cfgFile, "../../../../etc/passwd")
	if err == nil {
		t.Fatal("expected an error for a path-shaped ref")
	}
}

func TestHistory_MigrateLegacyBackups(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeConfigFile(t, dir, "models: {}\n")
	for _, name := range []string{"config.yaml.bak-preload", "config.yaml.bak.1234567", "config.yaml.bak-20260729T235131Z"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("models:\n  legacy: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	n, err := MigrateLegacyBackups(cfgFile, ConfigHistoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 migrated, got %d", n)
	}

	for _, name := range []string{"config.yaml.bak-preload", "config.yaml.bak.1234567", "config.yaml.bak-20260729T235131Z"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("legacy file %s should have been removed after migration", name)
		}
	}
	entries, err := ListHistory(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 history entries after migration, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Actor != "legacy-bak" {
			t.Errorf("migrated entry should have actor legacy-bak, got %q", e.Actor)
		}
	}

	// idempotent: calling again finds nothing left to migrate
	n2, err := MigrateLegacyBackups(cfgFile, ConfigHistoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second migration pass should be a no-op, migrated %d", n2)
	}
}
