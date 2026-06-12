package server

import (
	"context"
	"errors"
	"fmt"
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
