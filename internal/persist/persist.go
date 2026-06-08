// Package persist gives konflate's in-memory PR store durability across
// restarts: each PR's rendered diff is written to a gzipped JSON file under a
// state directory (on the operator's persistent cache volume), and reloaded at
// startup. Open PRs come back showing their last diff immediately and only
// re-render if the head advanced; the recently-merged shelf survives at all
// (it is never re-listed from the forge, so without this a restart empties it).
//
// One file per PR, named "<number>.json.gz". Writes are atomic (temp file +
// rename) so a crash mid-write can't leave a torn record, and Load skips any
// file it can't read rather than failing startup.
package persist

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/konflate/internal/api"
)

const fileSuffix = ".json.gz"

// Record is the durable snapshot of one PR's store entry. It mirrors the store's
// internal job (minus derived fields like signals, recomputed from Result on
// load) and is plain JSON — DiffResult/PR are the same structs the API serves.
type Record struct {
	PR         api.PR          `json:"pr"`
	Status     api.JobStatus   `json:"status"`
	Result     *api.DiffResult `json:"result,omitempty"`
	ErrMsg     string          `json:"errMsg,omitempty"`
	RefreshErr string          `json:"refreshErr,omitempty"`
	Updated    time.Time       `json:"updated"`
	ClosedAt   time.Time       `json:"closedAt,omitzero"`
	RenderedAt time.Time       `json:"renderedAt,omitzero"`
}

// Store reads and writes Records under a single directory.
type Store struct {
	dir string
	log *slog.Logger
}

// New ensures dir exists and returns a Store rooted there.
func New(dir string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("persist: create state dir: %w", err)
	}
	return &Store{dir: dir, log: log}, nil
}

func (s *Store) path(number int) string {
	return filepath.Join(s.dir, strconv.Itoa(number)+fileSuffix)
}

// Save writes rec atomically: the gzipped JSON goes to a temp file that is then
// renamed over the PR's file, so a reader (or a crash) never sees a partial one.
func (s *Store) Save(rec Record) error {
	tmp, err := os.CreateTemp(s.dir, strconv.Itoa(rec.PR.Number)+".*.tmp")
	if err != nil {
		return fmt.Errorf("persist: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // harmless no-op once the rename below succeeds

	gz := gzip.NewWriter(tmp)
	if err := json.NewEncoder(gz).Encode(rec); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("persist: encode: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("persist: gzip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("persist: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path(rec.PR.Number)); err != nil {
		return fmt.Errorf("persist: rename: %w", err)
	}
	return nil
}

// Delete removes a PR's file; a missing file is not an error.
func (s *Store) Delete(number int) error {
	if err := os.Remove(s.path(number)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("persist: delete: %w", err)
	}
	return nil
}

// Load returns every readable Record in the directory. Unreadable or corrupt
// files are logged and skipped — one bad file must not block startup.
func (s *Store) Load() []Record {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("persist: read state dir", "dir", s.dir, "error", err)
		}
		return nil
	}
	var recs []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), fileSuffix) {
			continue
		}
		rec, err := s.read(filepath.Join(s.dir, e.Name()))
		if err != nil {
			s.log.Warn("persist: skipping unreadable state file", "file", e.Name(), "error", err)
			continue
		}
		recs = append(recs, rec)
	}
	return recs
}

func (s *Store) read(path string) (Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = gz.Close() }()
	var rec Record
	if err := json.NewDecoder(gz).Decode(&rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}
