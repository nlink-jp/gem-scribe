package job

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// Wait returns once the running job has unwound after a cancellation, and
// not before: a server waits for it before it exits or frees what the job
// uses.
func TestWaitReturnsAfterTheCancelledJobUnwinds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx)
	started, unwound := make(chan struct{}), make(chan struct{})
	m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		close(started)
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond) // cleanup after the cancellation
		close(unwound)
		return nil, ctx.Err()
	})
	<-started
	if m.Wait(20 * time.Millisecond) {
		t.Fatal("Wait returned while the ctx was still live")
	}
	cancel()
	if !m.Wait(5 * time.Second) {
		t.Fatal("Wait timed out after the cancellation")
	}
	select {
	case <-unwound:
	default:
		t.Fatal("Wait returned before the job finished its cleanup")
	}
}

// A job that ignores its cancellation does not hold the server forever.
func TestWaitGivesUpAtItsTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx)
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	m.Submit(func(context.Context, func(Progress)) (any, error) {
		close(started)
		<-release
		return nil, nil
	})
	<-started
	cancel()
	if m.Wait(50 * time.Millisecond) {
		t.Fatal("Wait reported a job that never returned as finished")
	}
}

// A job still queued when the manager is cancelled is never started, however
// select orders the cancellation and the queue.
func TestAQueuedJobIsNotStartedAfterTheCancellation(t *testing.T) {
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		m := NewManager(ctx)
		started := make(chan struct{})
		m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		var ran atomic.Bool
		m.Submit(func(context.Context, func(Progress)) (any, error) {
			ran.Store(true)
			return nil, nil
		})
		<-started
		cancel()
		if !m.Wait(5 * time.Second) {
			t.Fatal("Wait timed out")
		}
		if ran.Load() {
			t.Fatalf("run %d: the queued job started after the cancellation", i)
		}
	}
}

// A job the server cancelled is recorded as cancelled, not as a failure
// reading "context canceled".
func TestACancelledJobIsRecordedAsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx)
	id := m.Submit(func(ctx context.Context, report func(Progress)) (any, error) {
		cancel()
		return nil, ctx.Err()
	})
	if !m.Wait(5 * time.Second) {
		t.Fatal("Wait timed out")
	}
	st, err := m.Get(id)
	if err != nil || st.Error == nil || !strings.Contains(st.Error.Message, "the job was cancelled") {
		t.Errorf("status = %+v, %v; want a cancelled job", st, err)
	}
}
