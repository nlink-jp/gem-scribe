package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is overridden at build time via -ldflags "-X .../cmd.Version=<vX.Y.Z>".
var Version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(cmd.OutOrStdout(), Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)

	// Every tool in the org answers `--version`, and the shared homebrew
	// formula template tests for it — without the flag `brew test` fails on
	// "unknown flag". Setting Version here (rather than in the rootCmd literal)
	// keeps the linker-injected value as the single source; cobra adds the flag
	// on its own. The `version` subcommand stays for compatibility, and the
	// template below strips cobra's "<name> version " prefix so both spellings
	// print exactly the same string.
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
}
