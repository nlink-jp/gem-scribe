//go:build linux

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// claimStdout duplicates the real stdout for exclusive use by the MCP
// transport and redirects fd 1 to stderr. See the darwin variant for why.
//
// Linux gets its own implementation because dup2 is not a syscall on every
// architecture Linux runs on — arm64 has only dup3 — and dup3 is available
// everywhere Linux is.
func claimStdout() (*os.File, error) {
	fd, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate stdout for the MCP transport: %w", err)
	}
	if err := syscall.Dup3(int(os.Stderr.Fd()), int(os.Stdout.Fd()), 0); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("redirect stdout to stderr: %w", err)
	}
	return os.NewFile(uintptr(fd), "mcp-stdout"), nil
}
