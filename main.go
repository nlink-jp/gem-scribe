// Command gem-scribe transcribes audio with Vertex AI's dedicated transcription
// model and serves that capability over MCP. See docs/ja/gem-scribe-rfp.ja.md
// for the design.
package main

import "github.com/nlink-jp/gem-scribe/cmd"

func main() {
	cmd.Execute()
}
