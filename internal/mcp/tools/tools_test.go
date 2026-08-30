package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// fakeTranscriber stands in for Vertex AI so the protocol and the plumbing are
// testable without a project, credentials, or a network.
type fakeTranscriber struct {
	result transcript.Result
	err    error
	seen   Request
}

func (f *fakeTranscriber) Transcribe(_ context.Context, req Request) (transcript.Result, error) {
	f.seen = req
	if f.err != nil {
		return transcript.Result{}, f.err
	}
	return f.result, nil
}

func sampleTranscript() transcript.Result {
	r := transcript.Result{
		Metadata: transcript.Metadata{
			Source: "meeting.m4a", Model: "test-model", Languages: []string{"ja"}, Diarized: true,
		},
		Segments: []transcript.Segment{
			{Start: 0.1, End: 4.4, Speaker: "spk:0", Text: map[string]string{"ja": "こんにちは"}},
			{Start: 5.3, End: 9.8, Speaker: "spk:1", Text: map[string]string{"ja": "よろしく"}},
		},
	}
	r.Normalize()
	return r
}

type harness struct {
	srv   *mcpserver.Server
	fake  *fakeTranscriber
	root  string
	audio string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	// The agent puts the recording in the workspace; the server never fetches.
	wsDir := filepath.Join(root, "default")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(wsDir, "meeting.m4a")
	if err := os.WriteFile(audio, []byte("not really audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeTranscriber{result: sampleTranscript()}
	srv := mcpserver.New("gem-scribe", "test", nil, nil)
	Register(srv, &Deps{
		WS:         workspace.NewManager(root),
		Transcribe: fake,
		Jobs:       job.NewManager(context.Background()),
	})
	return &harness{srv: srv, fake: fake, root: root, audio: audio}
}

func (h *harness) call(t *testing.T, name string, args string) (any, error) {
	t.Helper()
	return h.srv.Call(context.Background(), name, json.RawMessage(args))
}

// await polls check_job the way a client does, until the job leaves the queue.
func (h *harness) await(t *testing.T, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := h.call(t, "check_job", `{"job_id":"`+jobID+`"}`)
		if err != nil {
			t.Fatalf("check_job: %v", err)
		}
		status := toMap(t, out)
		switch status["state"] {
		case "done", "error":
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func submit(t *testing.T, h *harness, args string) string {
	t.Helper()
	out, err := h.call(t, "transcribe", args)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	m := toMap(t, out)
	id, _ := m["job_id"].(string)
	if id == "" {
		t.Fatalf("no job_id in %v", m)
	}
	return id
}

func TestTranscribe_WritesTheTranscriptAndReturnsItInline(t *testing.T) {
	h := newHarness(t)
	status := h.await(t, submit(t, h, `{"audio":"meeting.m4a"}`))

	if status["state"] != "done" {
		t.Fatalf("job did not succeed: %v", status)
	}
	result := toMap(t, status["result"])
	if result["text"] == nil || result["truncated"] != false {
		t.Errorf("a short transcript should come back inline: %v", result)
	}
	// The file is written either way, so an agent that keeps the transcript
	// never has to ask for it again.
	path, _ := result["absolute_path"].(string)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("transcript file was not written: %v", err)
	}
	if rel, _ := result["path"].(string); rel != "output/meeting.json" {
		t.Errorf("path = %q, want output/meeting.json", rel)
	}
}

// Defaults must reach the transcriber, not just the schema.
func TestTranscribe_DefaultsDiarizeAndTimestamps(t *testing.T) {
	h := newHarness(t)
	h.await(t, submit(t, h, `{"audio":"meeting.m4a"}`))

	if !h.fake.seen.Diarize || !h.fake.seen.WordTimestamps {
		t.Errorf("defaults did not reach the transcriber: %+v", h.fake.seen)
	}
}

// smart alone is a complete request: it must silently exclude the two defaults
// it conflicts with rather than reporting a conflict the caller never chose.
func TestTranscribe_SmartAloneTurnsOffTheConflictingDefaults(t *testing.T) {
	h := newHarness(t)
	h.await(t, submit(t, h, `{"audio":"meeting.m4a","smart":true}`))

	if !h.fake.seen.Smart || h.fake.seen.Diarize || h.fake.seen.WordTimestamps {
		t.Errorf("smart did not exclude the conflicting defaults: %+v", h.fake.seen)
	}
}

func TestTranscribe_Rejections(t *testing.T) {
	tests := []struct {
		name string
		args string
		code string
	}{
		{"missing audio", `{}`, toolerr.CodeMissingArgument},
		{"unknown field", `{"audio":"meeting.m4a","nope":1}`, toolerr.CodeInvalidArguments},
		{"bad format", `{"audio":"meeting.m4a","format":"docx"}`, toolerr.CodeInvalidArguments},
		{"smart with an explicit diarize", `{"audio":"meeting.m4a","smart":true,"diarize":true}`, toolerr.CodeModeConflict},
		{"srt without timings", `{"audio":"meeting.m4a","format":"srt","word_timestamps":false}`, toolerr.CodeInvalidArguments},
		{"escaping the workspace", `{"audio":"../../etc/passwd"}`, toolerr.CodePathNotAllowed},
		{"absolute path", `{"audio":"/etc/passwd"}`, toolerr.CodePathNotAllowed},
		{"missing recording", `{"audio":"absent.m4a"}`, toolerr.CodeInputNotFound},
		{"bad workspace id", `{"audio":"meeting.m4a","workspace_id":"../evil"}`, toolerr.CodeInvalidWorkspaceID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			_, err := h.call(t, "transcribe", tt.args)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			var te *toolerr.Error
			if !errors.As(err, &te) {
				t.Fatalf("error is not structured: %v", err)
			}
			if te.Code != tt.code {
				t.Errorf("code = %q, want %q (%v)", te.Code, tt.code, err)
			}
		})
	}
}

// A container the model cannot read is refused up front, with the accepted list
// in hand, rather than minutes later as an opaque API failure.
func TestTranscribe_RejectsUnsupportedContainer(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.root, "default", "clip.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.call(t, "transcribe", `{"audio":"clip.mkv"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeUnsupportedFormat {
		t.Fatalf("err = %v, want unsupported_format", err)
	}
	if !strings.Contains(err.Error(), ".wav") {
		t.Errorf("the rejection should list what is accepted: %v", err)
	}
}

func TestTranscribe_FailureIsReportedWithACode(t *testing.T) {
	h := newHarness(t)
	h.fake.err = asr.ErrSafetyBlock

	status := h.await(t, submit(t, h, `{"audio":"meeting.m4a"}`))
	if status["state"] != "error" {
		t.Fatalf("job should have failed: %v", status)
	}
	failure := toMap(t, status["error"])
	if failure["code"] != toolerr.CodeSafetyBlocked {
		t.Errorf("code = %v, want %q", failure["code"], toolerr.CodeSafetyBlocked)
	}
}

func TestCheckJob_UnknownID(t *testing.T) {
	h := newHarness(t)
	_, err := h.call(t, "check_job", `{"job_id":"nope"}`)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeJobNotFound {
		t.Fatalf("err = %v, want job_not_found", err)
	}
}

func TestGetUsage_ReturnsTheManual(t *testing.T) {
	h := newHarness(t)
	out, err := h.call(t, "get_usage", `{}`)
	if err != nil {
		t.Fatalf("get_usage: %v", err)
	}
	raw, ok := out.(mcpserver.RawResult)
	if !ok || len(raw.Content) == 0 || !strings.Contains(raw.Content[0].Text, "workspace") {
		t.Errorf("unexpected usage result: %#v", out)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"safety", asr.ErrSafetyBlock, toolerr.CodeSafetyBlocked},
		{"mode conflict", asr.ErrModeConflict, toolerr.CodeModeConflict},
		{"empty", transcript.ErrEmpty, toolerr.CodeEmptyTranscript},
		{"missing project", errors.New("GCP project is required: set ..."), toolerr.CodeProjectRequired},
		{"missing credentials", errors.New("could not find default credentials"), toolerr.CodeCredentials},
		{"anything else", errors.New("boom"), toolerr.CodeTranscribeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var te *toolerr.Error
			if !errors.As(classify(tt.err), &te) {
				t.Fatalf("classify did not produce a structured error for %v", tt.err)
			}
			if te.Code != tt.code {
				t.Errorf("code = %q, want %q", te.Code, tt.code)
			}
		})
	}

	// An error that already carries a code keeps it rather than being
	// re-classified into something vaguer.
	original := toolerr.New(toolerr.CodeInputNotFound, "gone")
	var te *toolerr.Error
	if !errors.As(classify(original), &te) || te.Code != toolerr.CodeInputNotFound {
		t.Error("classify overwrote an existing structured code")
	}
}

// The diagnosis is the only signal an agent has that a structurally perfect
// transcript is substantively wrong, so it must reach the result.
func TestResult_CarriesTheDiagnosisAsAWarning(t *testing.T) {
	h := newHarness(t)
	r := sampleTranscript()
	// Diarization that returned one speaker for a two-turn conversation.
	r.Segments[1].Speaker = r.Segments[0].Speaker
	h.fake.result = r

	status := h.await(t, submit(t, h, `{"audio":"meeting.m4a"}`))
	result := toMap(t, status["result"])
	warning, _ := result["warning"].(string)
	if !strings.Contains(warning, "single speaker") {
		t.Errorf("warning = %q, want the single-speaker diagnosis", warning)
	}
}
