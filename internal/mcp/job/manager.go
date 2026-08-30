// Package job runs transcriptions in the background and tracks their progress
// in memory. A single worker goroutine processes jobs FIFO.
//
// Serialising is not about local resources here — the work is one network call.
// It bounds how hard a batch of queued jobs can hit the project's quota, and it
// makes the order results come back in predictable. An agent that wants two
// transcriptions at once can run two servers; an agent that queues twenty
// should not be the reason a 429 storm starts.
//
// Jobs are deliberately NOT persisted: after a server restart check_job reports
// job_not_found with recovery guidance, and re-running transcribe is safe
// because it works from the same agent-prepared workspace.
package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

// Job states.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
)

// maxFinishedJobs bounds the finished-job history kept in memory.
const maxFinishedJobs = 32

// Progress is a coarse view of how far a render has advanced.
type Progress struct {
	Fraction float64 `json:"fraction"`          // 0..1 (best-effort; the engine reports sampling steps)
	Message  string  `json:"message,omitempty"` // e.g. "loading model", "step 12/30"
}

// Status is the check_job view of a job.
type Status struct {
	JobID      string         `json:"job_id"`
	State      string         `json:"state"` // queued | running | done | error
	Progress   Progress       `json:"progress"`
	Result     any            `json:"result,omitempty"` // set on done
	Error      *toolerr.Error `json:"error,omitempty"`  // set on error
	QueuedAt   string         `json:"queued_at"`
	StartedAt  string         `json:"started_at,omitempty"`
	FinishedAt string         `json:"finished_at,omitempty"`
}

// RunFunc performs the work. It reports progress via report and returns the
// result payload (surfaced verbatim in Status.Result) or an error.
type RunFunc func(ctx context.Context, report func(Progress)) (any, error)

type jobState struct {
	id  string
	run RunFunc

	mu         sync.Mutex
	state      string
	progress   Progress
	result     any
	err        *toolerr.Error
	queuedAt   time.Time
	startedAt  time.Time
	finishedAt time.Time
}

// Manager owns the in-memory job table and a single FIFO worker goroutine.
type Manager struct {
	ctx   context.Context
	mu    sync.Mutex
	jobs  map[string]*jobState
	queue chan *jobState
	once  sync.Once
}

// NewManager creates a job manager whose worker runs renders under ctx.
// Canceling ctx aborts the in-flight render and stops the worker. A nil ctx is
// treated as context.Background().
func NewManager(ctx context.Context) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	m := &Manager{
		ctx:   ctx,
		jobs:  make(map[string]*jobState),
		queue: make(chan *jobState, 128),
	}
	m.once.Do(func() { go m.worker() })
	return m
}

// Submit enqueues run and returns the new job id immediately. The job starts in
// the queued state and transitions to running when the worker picks it up.
func (m *Manager) Submit(run RunFunc) string {
	id := "job_" + randomHex(8)
	js := &jobState{
		id:       id,
		run:      run,
		state:    StateQueued,
		queuedAt: time.Now(),
	}
	m.mu.Lock()
	m.jobs[id] = js
	m.evictLocked()
	m.mu.Unlock()
	m.queue <- js
	return id
}

// worker processes queued jobs one at a time (FIFO), which is what bounds the
// request rate a queue of jobs can produce.
func (m *Manager) worker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case js := <-m.queue:
			js.start()
			res, err := js.run(m.ctx, js.report)
			js.finish(res, err)
		}
	}
}

func (js *jobState) start() {
	js.mu.Lock()
	defer js.mu.Unlock()
	if js.state == StateQueued {
		js.state = StateRunning
		js.startedAt = time.Now()
	}
}

func (js *jobState) report(p Progress) {
	js.mu.Lock()
	defer js.mu.Unlock()
	if js.state == StateRunning {
		js.progress = p
	}
}

func (js *jobState) finish(res any, err error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	if js.state != StateRunning {
		return
	}
	js.finishedAt = time.Now()
	if err != nil {
		js.state = StateError
		var te *toolerr.Error
		if errors.As(err, &te) {
			js.err = te
		} else {
			js.err = toolerr.New(toolerr.CodeTranscribeFailed, err.Error())
		}
		return
	}
	js.state = StateDone
	js.result = res
	js.progress.Fraction = 1
}

// Get returns the status of a job, or a job_not_found error that tells the
// agent how to recover after a server restart.
func (m *Manager) Get(jobID string) (Status, error) {
	m.mu.Lock()
	js, ok := m.jobs[jobID]
	m.mu.Unlock()
	if !ok {
		return Status{}, toolerr.Newf(toolerr.CodeJobNotFound,
			"job %q not found — jobs do not survive a server restart; re-submit generate (it re-renders from the same workspace)", jobID)
	}

	js.mu.Lock()
	defer js.mu.Unlock()
	st := Status{
		JobID:    js.id,
		State:    js.state,
		Progress: js.progress,
		Result:   js.result,
		Error:    js.err,
		QueuedAt: js.queuedAt.Format(time.RFC3339),
	}
	if !js.startedAt.IsZero() {
		st.StartedAt = js.startedAt.Format(time.RFC3339)
	}
	if !js.finishedAt.IsZero() {
		st.FinishedAt = js.finishedAt.Format(time.RFC3339)
	}
	return st, nil
}

// evictLocked drops the oldest finished jobs beyond maxFinishedJobs.
// Caller holds m.mu.
func (m *Manager) evictLocked() {
	type fin struct {
		id string
		at time.Time
	}
	var finished []fin
	for id, js := range m.jobs {
		js.mu.Lock()
		done := js.state == StateDone || js.state == StateError
		at := js.finishedAt
		js.mu.Unlock()
		if done {
			finished = append(finished, fin{id, at})
		}
	}
	if len(finished) <= maxFinishedJobs {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].at.Before(finished[j].at) })
	for _, f := range finished[:len(finished)-maxFinishedJobs] {
		delete(m.jobs, f.id)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable in practice; a time-based
		// fallback keeps ids unique enough for an in-memory table.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))[:2*n]
	}
	return hex.EncodeToString(b)
}
