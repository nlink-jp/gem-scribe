package toolerr

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestErrorString(t *testing.T) {
	if got := New(CodeProjectRequired, "").Error(); got != CodeProjectRequired {
		t.Errorf("empty message: got %q", got)
	}
	if got := New(CodeProjectRequired, "set [gcp].project").Error(); got != "project_required: set [gcp].project" {
		t.Errorf("got %q", got)
	}
}

func TestIsMatchesByCode(t *testing.T) {
	err := Newf(CodeInputNotFound, "recording %q is not in the workspace", "meeting.m4a")
	if !errors.Is(err, New(CodeInputNotFound, "")) {
		t.Error("errors.Is should match by code regardless of message")
	}
	if errors.Is(err, New(CodeTranscribeFailed, "")) {
		t.Error("different code must not match")
	}
}

func TestWithDetails(t *testing.T) {
	base := New(CodeTranscribeFailed, "boom")
	d := base.WithDetails(map[string]any{"exit": 1})
	if base.Details != nil {
		t.Error("WithDetails must not mutate the receiver")
	}
	b, _ := json.Marshal(d)
	if !contains(string(b), `"details":{"exit":1}`) {
		t.Errorf("details not serialized: %s", b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
