// Package enrich adds what the transcription model cannot: a translation
// alongside the original, and real names in place of spk:0 / spk:1.
//
// Both run over the transcript *text* with a general Gemini model, and both
// obey the rule the whole tool is built on (ADR-0001): a model never authors
// the transcript's structure. The segments already exist and are authoritative.
// A model is only ever asked to fill slots this package numbered, and anything
// that does not come back leaves its slot as it was — so a bad response costs
// one line's translation, never the transcript.
package enrich

import (
	"fmt"
	"strconv"
	"strings"
)

// numberedBlock renders slots for the model to fill: one line per item, each
// prefixed with its index.
//
// Numbering is what makes the response verifiable. Asking for a JSON array
// means a single malformed character costs every item; asking for numbered
// lines means an unreadable line costs that line, and the reader can say which
// one it was.
func numberedBlock(items []string) string {
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, collapseNewlines(item))
	}
	return b.String()
}

// parseNumberedBlock reads the model's reply back into slots.
//
// It is deliberately lenient about everything except the number: models prefix
// replies with prose, wrap them in code fences, and re-space the separator.
// None of that changes which slot a line belongs to, and refusing the whole
// response over a stray "Here you go:" would throw away good translations.
// It is strict about the one thing that matters — an index outside the range
// asked for is dropped rather than guessed at.
func parseNumberedBlock(reply string, want int) map[int]string {
	out := make(map[int]string, want)
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		idx, text, ok := splitNumbered(line)
		if !ok || idx < 1 || idx > want {
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			// A duplicated index keeps the first answer: a model that repeats
			// itself is usually correcting nothing, and taking the last one
			// would make the result depend on how much it rambled.
			if _, seen := out[idx]; !seen {
				out[idx] = text
			}
		}
	}
	return out
}

// splitNumbered pulls a leading index off a line, accepting the separators
// models actually produce: a tab, a period, a colon, or a parenthesis.
func splitNumbered(line string) (int, string, bool) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(line[:i])
	if err != nil {
		return 0, "", false
	}
	rest := line[i:]
	rest = strings.TrimLeft(rest, ".):\t ")
	return n, rest, true
}

// collapseNewlines keeps one item on one line. A newline inside an item would
// break the numbering the response is read back with.
func collapseNewlines(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// chunk splits indices into batches small enough that one response stays short.
//
// Batching is not about token limits; it is about blast radius and about the
// well-known tendency of long generations to drift. A batch that comes back
// unusable costs its own segments, and the rest of the transcript is unaffected.
func chunk(items []string, maxItems, maxChars int) [][]int {
	var out [][]int
	var current []int
	chars := 0
	for i, item := range items {
		if len(current) > 0 && (len(current) >= maxItems || chars+len(item) > maxChars) {
			out = append(out, current)
			current, chars = nil, 0
		}
		current = append(current, i)
		chars += len(item)
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}
