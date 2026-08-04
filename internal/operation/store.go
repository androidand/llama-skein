package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ErrNotFound is returned by Store.Load for an unknown operation ID.
var ErrNotFound = errors.New("operation: not found")

// record is the on-disk shape. Kept separate from Operation so the wire/
// domain type can evolve without silently changing what old files decode
// into, and vice versa.
type record struct {
	ID               string             `json:"id"`
	Phase            Phase              `json:"phase"`
	SourceRepository string             `json:"source_repository"`
	SourceRevision   string             `json:"source_revision"`
	ModelID          string             `json:"model_id"`
	Artifacts        []ArtifactProgress `json:"artifacts"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	Error            *Error             `json:"error,omitempty"`
	Warnings         []string           `json:"warnings,omitempty"`
}

func toRecord(op *Operation) record {
	return record{
		ID:               op.ID,
		Phase:            op.Phase,
		SourceRepository: op.SourceRepository,
		SourceRevision:   op.SourceRevision,
		ModelID:          op.ModelID,
		Artifacts:        op.Artifacts,
		CreatedAt:        op.CreatedAt,
		UpdatedAt:        op.UpdatedAt,
		Error:            op.Error,
		Warnings:         op.Warnings,
	}
}

func (r record) toOperation() *Operation {
	return &Operation{
		ID:               r.ID,
		Phase:            r.Phase,
		SourceRepository: r.SourceRepository,
		SourceRevision:   r.SourceRevision,
		ModelID:          r.ModelID,
		Artifacts:        r.Artifacts,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		Error:            r.Error,
		Warnings:         r.Warnings,
	}
}

// DefaultStateDir returns llama-skein's owned operation-record directory,
// ~/.llama-skein/operations — the same ~/.llama-skein root the server's
// profile storage already uses (internal/server/server.go), so operation
// records and the user profile live under one place instead of introducing
// a second convention for "where llama-skein keeps its own state."
func DefaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("operation store: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".llama-skein", "operations"), nil
}

// Store persists Operation records as one JSON file per operation under dir.
// Every write is a temp-file-plus-rename so a crash or concurrent read never
// observes a partially written record (design.md decision 3: "small atomic
// JSON records").
type Store struct {
	dir         string
	maxTerminal int // bounds retained succeeded/cancelled/failed records; non-terminal ones are never pruned.
}

// NewStore returns a Store rooted at dir, creating it if necessary. dir is
// the caller's responsibility to choose — production code points this at
// llama-skein's owned state directory (~/.llama-skein/operations); tests use
// t.TempDir().
func NewStore(dir string, maxTerminal int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("operation store: %w", err)
	}
	return &Store{dir: dir, maxTerminal: maxTerminal}, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Save atomically writes op's current state. Safe to call repeatedly as an
// operation progresses — each call fully replaces the previous record.
func (s *Store) Save(op *Operation) error {
	data, err := json.MarshalIndent(toRecord(op), "", "  ")
	if err != nil {
		return fmt.Errorf("operation store: marshal %s: %w", op.ID, err)
	}
	if err := atomicWriteFile(s.path(op.ID), data, 0o644); err != nil {
		return fmt.Errorf("operation store: save %s: %w", op.ID, err)
	}
	return nil
}

// Load reads one operation by ID. Returns ErrNotFound if it does not exist.
func (s *Store) Load(id string) (*Operation, error) {
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("operation store: load %s: %w", id, err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("operation store: decode %s: %w", id, err)
	}
	return r.toOperation(), nil
}

// List returns every stored operation, most recently created first. Malformed
// or unreadable files are skipped rather than failing the whole list — one
// corrupt record should not hide every other operation from an operator.
func (s *Store) List() ([]*Operation, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("operation store: list: %w", err)
	}
	ops := make([]*Operation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		op, err := s.Load(id)
		if err != nil {
			continue
		}
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool {
		if !ops[i].CreatedAt.Equal(ops[j].CreatedAt) {
			return ops[i].CreatedAt.After(ops[j].CreatedAt)
		}
		return ops[i].ID > ops[j].ID // stable tiebreak when timestamps collide
	})
	return ops, nil
}

// Prune removes the oldest terminal (succeeded/cancelled/failed) records
// beyond maxTerminal. Non-terminal operations are never removed by Prune —
// only Save (never Prune) may act on an in-flight operation, so recovery
// (task 2.4) always finds every interrupted one.
//
// Count-based only for this slice; whether history should also expire by age
// is design.md's own open question, not resolved here.
func (s *Store) Prune() error {
	ops, err := s.List() // newest first
	if err != nil {
		return err
	}
	kept := 0
	for _, op := range ops {
		if !op.Phase.Terminal() {
			continue
		}
		kept++
		if kept <= s.maxTerminal {
			continue
		}
		if err := os.Remove(s.path(op.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("operation store: prune %s: %w", op.ID, err)
		}
	}
	return nil
}

// atomicWriteFile writes data to path via temp file + rename, so a reader
// never observes a partially written file. Same idiom as
// internal/server/confighelpers.go and proxy/proxymanager_config.go's
// unexported helpers of the same name — duplicated rather than shared
// because none of the three packages should depend on each other for this.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
