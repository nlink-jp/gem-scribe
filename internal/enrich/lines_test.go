package enrich

import (
	"strings"
	"testing"
)

func TestNumberedBlock(t *testing.T) {
	got := numberedBlock([]string{"first", "second"})
	if got != "1\tfirst\n2\tsecond\n" {
		t.Errorf("got %q", got)
	}
}

// A newline inside an item would split it across two numbered lines and
// silently shift everything after it.
func TestNumberedBlock_CollapsesNewlines(t *testing.T) {
	got := numberedBlock([]string{"a\nb\n\nc"})
	if strings.Count(got, "\n") != 1 {
		t.Errorf("an embedded newline survived: %q", got)
	}
	if got != "1\ta b c\n" {
		t.Errorf("got %q", got)
	}
}

func TestSplitNumbered(t *testing.T) {
	tests := []struct {
		in    string
		idx   int
		text  string
		valid bool
	}{
		{"1\thello", 1, "hello", true},
		{"2. hello", 2, "hello", true},
		{"3) hello", 3, "hello", true},
		{"4: hello", 4, "hello", true},
		{"12   hello", 12, "hello", true},
		{"hello", 0, "", false},
		{"", 0, "", false},
	}
	for _, tt := range tests {
		idx, text, ok := splitNumbered(tt.in)
		if ok != tt.valid || (ok && (idx != tt.idx || text != tt.text)) {
			t.Errorf("splitNumbered(%q) = (%d, %q, %v), want (%d, %q, %v)",
				tt.in, idx, text, ok, tt.idx, tt.text, tt.valid)
		}
	}
}

// A model that answers twice for one slot must not make the result depend on
// how long it rambled.
func TestParseNumberedBlock_DuplicateIndexKeepsTheFirst(t *testing.T) {
	got := parseNumberedBlock("1\tfirst\n1\tsecond\n", 1)
	if got[1] != "first" {
		t.Errorf("got %q, want the first answer", got[1])
	}
}

func TestParseNumberedBlock_DropsEmptyAndOutOfRange(t *testing.T) {
	got := parseNumberedBlock("1\t\n2\tkept\n3\ttoo far\n", 2)
	if len(got) != 1 || got[2] != "kept" {
		t.Errorf("got %v, want only slot 2", got)
	}
}

func TestChunk(t *testing.T) {
	items := make([]string, 10)
	for i := range items {
		items[i] = "0123456789"
	}

	byCount := chunk(items, 3, 1_000_000)
	if len(byCount) != 4 || len(byCount[0]) != 3 || len(byCount[3]) != 1 {
		t.Errorf("count-bounded chunking: %v", byCount)
	}

	byChars := chunk(items, 1000, 25)
	for _, batch := range byChars {
		if len(batch) > 3 {
			t.Errorf("a batch exceeded the character budget: %v", byChars)
		}
	}

	// Every item lands in exactly one batch, in order.
	seen := 0
	for _, batch := range byChars {
		for _, idx := range batch {
			if idx != seen {
				t.Fatalf("chunking reordered or skipped: expected %d, got %d", seen, idx)
			}
			seen++
		}
	}
	if seen != len(items) {
		t.Errorf("chunking lost items: %d of %d", seen, len(items))
	}

	if len(chunk(nil, 5, 100)) != 0 {
		t.Error("chunking nothing should produce no batches")
	}
}

// A single item larger than the whole budget still has to be sent.
func TestChunk_OversizedItemIsNotDropped(t *testing.T) {
	batches := chunk([]string{strings.Repeat("x", 500)}, 10, 100)
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Errorf("an oversized item was dropped: %v", batches)
	}
}
