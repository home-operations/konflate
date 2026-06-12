package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/provider"
)

// stubWriter is a provider.Writer whose Verify returns a configurable error.
type stubWriter struct{ verifyErr error }

func (stubWriter) SetStatus(context.Context, api.PR, provider.Status) error    { return nil }
func (stubWriter) UpsertComment(context.Context, api.PR, string, string) error { return nil }
func (s stubWriter) Verify(context.Context) error                              { return s.verifyErr }

func TestVerifyWriteBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		verifyErr    error
		wantDisabled bool
	}{
		{"verified stays enabled", nil, false},
		{"permanent rejection disables", fmt.Errorf("verify: %w", provider.ErrWriteAuthRejected), true},
		{"transient failure stays enabled", errors.New("503 service unavailable"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{cfg: &config.Config{}, log: discardLog(), writer: stubWriter{verifyErr: tc.verifyErr}}
			s.verifyWriteBack(context.Background())
			if (s.writer == nil) != tc.wantDisabled {
				t.Errorf("write-back disabled = %v, want %v", s.writer == nil, tc.wantDisabled)
			}
		})
	}
}

func TestRetryWrite(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on the first try", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := retryWrite(context.Background(), 3, time.Millisecond, func() error { calls++; return nil })
		if err != nil || calls != 1 {
			t.Fatalf("calls=%d err=%v; want 1 call, no error", calls, err)
		}
	})

	t.Run("retries a transient failure then succeeds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := retryWrite(context.Background(), 3, time.Millisecond, func() error {
			calls++
			if calls < 3 {
				return errors.New("forge unavailable")
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("calls=%d err=%v; want 3 calls, no error", calls, err)
		}
	})

	t.Run("gives up after the attempt cap, returning the last error", func(t *testing.T) {
		t.Parallel()
		want := errors.New("solar flare")
		calls := 0
		err := retryWrite(context.Background(), 3, time.Millisecond, func() error { calls++; return want })
		if calls != 3 || !errors.Is(err, want) {
			t.Fatalf("calls=%d err=%v; want 3 calls and the last error %v", calls, err, want)
		}
	})

	t.Run("stops retrying once the context is done", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled: the first try runs, the backoff bails
		calls := 0
		err := retryWrite(ctx, 5, time.Hour, func() error { calls++; return errors.New("down") })
		if calls != 1 || err == nil {
			t.Fatalf("calls=%d err=%v; want a single try and an error (no long sleep)", calls, err)
		}
	})
}

// TestWriteBack_SerializesPerPR is the duplicate-comment guard: two write-backs
// for the same PR (the queue can finish its in-flight and trailing renders
// back-to-back, each firing one) must run one at a time, so the later one sees
// and edits the earlier one's comment instead of racing into a second create.
func TestWriteBack_SerializesPerPR(t *testing.T) {
	t.Parallel()
	srv := &Server{runCtx: context.Background(), log: discardLog()}
	var inFlight, maxConc int32
	var wg sync.WaitGroup
	work := func(context.Context) error {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxConc)
			if cur <= m || atomic.CompareAndSwapInt32(&maxConc, m, cur) {
				break
			}
		}
		time.Sleep(3 * time.Millisecond) // widen the window a racing write would overlap in
		atomic.AddInt32(&inFlight, -1)
		wg.Done()
		return nil
	}
	wg.Add(2)
	srv.writeBack("comment", 5, work)
	srv.writeBack("comment", 5, work)
	wg.Wait()
	if maxConc != 1 {
		t.Fatalf("same-PR write-backs overlapped (max concurrency %d), want serialized (1)", maxConc)
	}
}

func TestKeyedMutex_DistinctKeysDoNotBlock(t *testing.T) {
	t.Parallel()
	var km keyedMutex
	unlock1 := km.lock(1)
	defer unlock1()
	done := make(chan struct{})
	go func() {
		km.lock(2)() // a different key must not wait on key 1
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lock on a distinct key blocked while another key was held")
	}
}

func TestKeyedMutex_DropsIdleEntries(t *testing.T) {
	t.Parallel()
	var km keyedMutex
	km.lock(7)() // lock then immediately unlock
	km.mu.Lock()
	defer km.mu.Unlock()
	if n := len(km.m); n != 0 {
		t.Fatalf("idle key left %d map entries, want 0 (no unbounded growth)", n)
	}
}
