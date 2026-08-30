package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

func waitDone(t *testing.T, m *Manager, id string) Status {
	t.Helper()
	for i := 0; i < 1000; i++ {
		st, err := m.Get(id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if st.State == StateDone || st.State == StateError {
			return st
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return Status{}
}

func TestSubmitDone(t *testing.T) {
	m := NewManager(context.Background())
	id := m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		report(Progress{Fraction: 0.5, Message: "halfway"})
		return map[string]any{"ok": true}, nil
	})
	st := waitDone(t, m, id)
	if st.State != StateDone {
		t.Fatalf("state: %+v", st)
	}
	if st.Progress.Fraction != 1 {
		t.Errorf("done fraction: %v", st.Progress.Fraction)
	}
	if st.Result.(map[string]any)["ok"] != true {
		t.Errorf("result: %v", st.Result)
	}
	if st.QueuedAt == "" || st.StartedAt == "" || st.FinishedAt == "" {
		t.Errorf("timestamps: %+v", st)
	}
}

func TestSubmitError(t *testing.T) {
	m := NewManager(context.Background())
	id := m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		return nil, toolerr.New(toolerr.CodeTranscribeFailed, "boom")
	})
	st := waitDone(t, m, id)
	if st.State != StateError || st.Error == nil || st.Error.Code != toolerr.CodeTranscribeFailed {
		t.Fatalf("status: %+v err=%v", st, st.Error)
	}
	if st.Result != nil {
		t.Errorf("errored job must not carry a result: %v", st.Result)
	}
}

func TestSubmitErrorNonToolerr(t *testing.T) {
	m := NewManager(context.Background())
	id := m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		return nil, errors.New("raw failure")
	})
	st := waitDone(t, m, id)
	if st.State != StateError || st.Error.Code != toolerr.CodeTranscribeFailed {
		t.Fatalf("status: %+v", st)
	}
}

func TestGetNotFound(t *testing.T) {
	m := NewManager(context.Background())
	_, err := m.Get("job_nope")
	if !errors.Is(err, toolerr.New(toolerr.CodeJobNotFound, "")) {
		t.Fatalf("want job_not_found, got %v", err)
	}
}

// TestSerialFIFO verifies the single worker runs jobs one at a time (never two
// concurrently) — the invariant that keeps the Metal engine safe.
func TestSerialFIFO(t *testing.T) {
	m := NewManager(context.Background())
	var (
		mu      sync.Mutex
		running int
		maxSeen int
	)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
			mu.Lock()
			running++
			if running > maxSeen {
				maxSeen = running
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return nil, nil
		}))
	}
	for _, id := range ids {
		waitDone(t, m, id)
	}
	if maxSeen != 1 {
		t.Errorf("max concurrent jobs = %d, want 1 (renders must be serialized)", maxSeen)
	}
}

// TestQueuedState verifies a job submitted behind a slow one is observably
// queued before it starts running.
func TestQueuedState(t *testing.T) {
	m := NewManager(context.Background())
	release := make(chan struct{})
	blocker := m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		<-release
		return nil, nil
	})
	queued := m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		return nil, nil
	})
	// The second job should be queued while the first blocks.
	st, err := m.Get(queued)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateQueued {
		t.Errorf("second job state = %q, want queued", st.State)
	}
	close(release)
	waitDone(t, m, blocker)
	waitDone(t, m, queued)
}
