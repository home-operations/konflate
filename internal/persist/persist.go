// Package persist gives konflate's in-memory PR store durability across
// restarts: each PR's rendered diff is written to a zstd-compressed JSON file
// under a state directory (on the operator's persistent cache volume), and
// reloaded at startup. Open PRs come back showing their last diff immediately
// and only re-render if the head advanced; the recently-merged shelf survives
// at all (it is never re-listed from the forge, so without this a restart
// empties it).
//
// One file per PR, named "<number>.json.zst". Writes are atomic (temp file +
// rename) so a crash mid-write can't leave a torn record, and Load skips any
// file it can't read rather than failing startup. zstd (over gzip) buys a
// better ratio on the highlight-HTML payload and markedly faster decompression,
// which is what dominates startup; klauspost/compress is already in the module
// graph, so it costs nothing extra.
package persist

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/home-operations/konflate/internal/api"
)

const fileSuffix = ".json.zst"

// Package-level codec: zstd's EncodeAll/DecodeAll are safe for concurrent use,
// so one shared encoder/decoder serves every Save/Load with no per-call stream
// allocation. SpeedDefault already beats gzip on both ratio and speed here.
var (
	zEnc = func() *zstd.Encoder {
		e, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			panic("persist: zstd encoder: " + err.Error()) // only fails on a bad constant option
		}
		return e
	}()
	zDec = func() *zstd.Decoder {
		d, err := zstd.NewReader(nil)
		if err != nil {
			panic("persist: zstd decoder: " + err.Error())
		}
		return d
	}()
)

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

// Save writes rec atomically: the zstd-compressed JSON goes to a temp file that
// is then renamed over the PR's file, so a reader (or a crash) never sees a
// partial one.
func (s *Store) Save(rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("persist: encode: %w", err)
	}
	blob := zEnc.EncodeAll(data, nil)

	tmp, err := os.CreateTemp(s.dir, strconv.Itoa(rec.PR.Number)+".*.tmp")
	if err != nil {
		return fmt.Errorf("persist: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // harmless no-op once the rename below succeeds

	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("persist: write: %w", err)
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

// Load returns every readable Record in the directory. Files are read and
// decompressed concurrently (CPU-bound zstd, so it scales with cores) since a
// keep-forever shelf can hold many; unreadable or corrupt files are logged and
// skipped — one bad file must not block startup.
func (s *Store) Load() []Record {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("persist: read state dir", "dir", s.dir, "error", err)
		}
		return nil
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), fileSuffix) {
			paths = append(paths, filepath.Join(s.dir, e.Name()))
		}
	}

	recs := make([]Record, len(paths))
	loaded := make([]bool, len(paths))
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rec, err := s.read(p)
			if err != nil {
				s.log.Warn("persist: skipping unreadable state file", "file", filepath.Base(p), "error", err)
				return
			}
			recs[i], loaded[i] = rec, true
		}()
	}
	wg.Wait()

	out := make([]Record, 0, len(paths))
	for i := range recs {
		if loaded[i] {
			out = append(out, recs[i])
		}
	}
	return out
}

func (s *Store) read(path string) (Record, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	data, err := zDec.DecodeAll(blob, nil)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}
