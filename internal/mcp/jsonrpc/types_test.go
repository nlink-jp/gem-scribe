package jsonrpc

import (
	"encoding/json"
	"testing"
)

func TestIsNotification(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.IsNotification() {
		t.Error("request without id should be a notification")
	}

	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.IsNotification() {
		t.Error("request with id must not be a notification")
	}
}

func TestResponseOmitsEmpty(t *testing.T) {
	// A result-only response must not emit an "error" field, and vice versa.
	rb := json.RawMessage(`{"ok":true}`)
	b, err := json.Marshal(Response{JSONRPC: "2.0", Result: rb})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if want := `"result":{"ok":true}`; !contains(s, want) {
		t.Errorf("result missing: %s", s)
	}
	if contains(s, `"error"`) {
		t.Errorf("result-only response must not carry error: %s", s)
	}

	b, err = json.Marshal(Response{JSONRPC: "2.0", Error: &Error{Code: CodeInvalidParams, Message: "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); contains(s, `"result"`) {
		t.Errorf("error-only response must not carry result: %s", s)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
