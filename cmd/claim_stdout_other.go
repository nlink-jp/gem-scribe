//go:build !darwin && !linux

package cmd

import "os"

// claimStdout hands back stdout unchanged on platforms without a file
// descriptor to redirect.
//
// The redirection is a second line of defence — a stray write from a
// dependency corrupts the JSON-RPC stream — not the first. Losing it here
// costs the safety net, not correctness: nothing in this program writes to
// stdout outside the transport. Refusing to run instead would be a worse
// trade, since it would take the whole MCP server away from these platforms
// to guard against a leak that has not happened.
func claimStdout() (*os.File, error) {
	return os.Stdout, nil
}
