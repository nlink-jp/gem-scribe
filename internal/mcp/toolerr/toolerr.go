// Package toolerr defines a structured tool-error type that MCP tools return
// to clients. Each error carries a stable code (slug) that LLM clients can
// branch on, plus a human-readable message and optional details.
//
// The error type satisfies the standard error interface, and its Is method
// compares by Code so errors.Is works with sentinel values regardless of the
// inner Message.
package toolerr

import "fmt"

// Error is a structured tool error.
type Error struct {
	// Code is a stable slug for client-side branching (e.g. "render_failed").
	Code string `json:"code"`
	// Message is a human-readable summary.
	Message string `json:"message"`
	// Details carries machine-readable context (e.g. missing model, exit code).
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is reports whether target is a *Error with the same Code. This lets sentinel
// values work under errors.Is regardless of the inner Message and Details.
func (e *Error) Is(target error) bool {
	te, ok := target.(*Error)
	if !ok {
		return false
	}
	return te.Code == e.Code
}

// WithDetails returns a copy of e with the given details attached.
func (e *Error) WithDetails(d map[string]any) *Error {
	cp := *e
	cp.Details = d
	return &cp
}

// New creates an Error.
func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf creates an Error with a printf-formatted message.
func Newf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Stable error codes used across the tool implementations. Adding a new code
// is a no-op for older clients (they fall back to inspecting Message), but
// renaming an existing code is a breaking change.
const (
	CodeInvalidArguments   = "invalid_arguments"
	CodeMissingArgument    = "missing_argument"
	CodeInvalidWorkspaceID = "invalid_workspace_id"
	CodePathNotAllowed     = "path_not_allowed"
	CodeWorkspaceFailed    = "workspace_failed"
	CodeJobNotFound        = "job_not_found"

	// Work directory, per organization ADR-021. Split out of path_not_allowed
	// so a caller can tell "you did not pass one" from "it is not there" from
	// "this server refuses to write there".
	CodeWorkDirRequired    = "work_dir_required"
	CodeWorkDirInvalid     = "work_dir_invalid"
	CodeWorkDirNotFound    = "work_dir_not_found"
	CodeWorkDirNotWritable = "work_dir_not_writable"
	CodeWorkDirDenied      = "work_dir_denied"

	// Transcription-specific.
	CodeInputNotFound     = "input_not_found"      // the named recording is missing from the workspace
	CodeUnsupportedFormat = "unsupported_format"   // the file extension is not an audio format the model accepts
	CodeTranscribeFailed  = "transcribe_failed"    // the API call failed
	CodeEmptyTranscript   = "empty_transcript"     // the call succeeded but produced no speech
	CodeSafetyBlocked     = "safety_blocked"       // the safety filter refused the audio
	CodeModeConflict      = "mode_conflict"        // smart was combined with diarization or word timestamps
	CodeProjectRequired   = "project_required"     // no GCP project is configured
	CodeCredentials       = "credentials"          // Application Default Credentials are missing or expired
	CodeTooLargeForInline = "too_large_for_inline" // the file needs a staging bucket that is not configured
)
