package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var configPath string

var rootCmd = &cobra.Command{
	Use:   "gem-scribe",
	Short: "Cloud speech-to-text CLI and MCP server on Vertex AI Gemini",
	Long: `gem-scribe transcribes audio with Vertex AI's dedicated transcription model
and serves that capability over MCP, so an agent whose model cannot handle
audio can still read a recording.

The model returns speaker turns and word-level timestamps as structured data,
so nothing has to ask a language model to emit JSON. Output carries speaker
labels and timestamps in an envelope compatible with voice-scribe, the local
counterpart of this tool, so downstream tools parse cloud and local
transcripts alike.

Choosing between the two: voice-scribe costs nothing to run and keeps the
audio on the machine; gem-scribe is more accurate, faster, and separates up to
eight speakers where voice-scribe stops at four.`,
	// Don't dump the usage help on RunE errors; cobra still prints "Error: ..." to stderr.
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "",
		"Path to config.toml (default: ~/.config/gem-scribe/config.toml)")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// cobra has already printed "Error: ..." to stderr.
		os.Exit(1)
	}
}
