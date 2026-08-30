//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// claimStdout duplicates the real stdout for exclusive use by the MCP
// transport and redirects fd 1 to stderr.
//
// Returning an *os.File rather than writing through fd 1 is the point: after
// this call there is no way to reach the protocol stream except through the
// returned handle, so a stray write from any dependency lands in the log
// instead of corrupting the protocol.
func claimStdout() (*os.File, error) {
	fd, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate stdout for the MCP transport: %w", err)
	}
	if err := syscall.Dup2(int(os.Stderr.Fd()), int(os.Stdout.Fd())); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("redirect stdout to stderr: %w", err)
	}
	return os.NewFile(uintptr(fd), "mcp-stdout"), nil
}
