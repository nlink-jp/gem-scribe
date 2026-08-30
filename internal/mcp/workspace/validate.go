package workspace

import (
	"regexp"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

// workspaceIDPattern: 1–64 chars, ASCII letters/digits/underscore/hyphen only.
// One workspace corresponds to one transcription project.
var workspaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ErrInvalidID is the sentinel for an invalid workspace_id. errors.Is matches
// by Code so wrapped variants with detailed messages still satisfy it.
var ErrInvalidID = toolerr.New(toolerr.CodeInvalidWorkspaceID, "invalid workspace_id: must match ^[a-zA-Z0-9_-]{1,64}$")

// ValidateID returns ErrInvalidID if id does not match the allowed pattern.
func ValidateID(id string) error {
	if !workspaceIDPattern.MatchString(id) {
		return toolerr.Newf(toolerr.CodeInvalidWorkspaceID,
			"invalid workspace_id %q: must match ^[a-zA-Z0-9_-]{1,64}$", id)
	}
	return nil
}
