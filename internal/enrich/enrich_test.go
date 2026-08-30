package enrich

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// fakeGenerator replays canned replies and records what it was asked.
type fakeGenerator struct {
	replies []string
	err     error
	calls   int
	prompts []string
	systems []string
}

func (f *fakeGenerator) Generate(_ context.Context, system, user string) (string, error) {
	f.calls++
	f.systems = append(f.systems, system)
	f.prompts = append(f.prompts, user)
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "", nil
	}
	reply := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return reply, nil
}

func sample(n int) *transcript.Result {
	r := &transcript.Result{
		Metadata: transcript.Metadata{
			Source: "meeting.m4a", Model: "m", Languages: []string{"ja"}, Diarized: true,
		},
	}
	for i := range n {
		r.Segments = append(r.Segments, transcript.Segment{
			Start:   float64(i),
			End:     float64(i) + 1,
			Speaker: fmt.Sprintf("spk:%d", i%2),
			Text:    map[string]string{"ja": fmt.Sprintf("原文%d", i+1)},
		})
	}
	r.Normalize()
	return r
}

func TestTranslate_AddsALanguageWithoutTouchingTheStructure(t *testing.T) {
	r := sample(3)
	before := len(r.Segments)
	gen := &fakeGenerator{replies: []string{"1\tone\n2\ttwo\n3\tthree\n"}}

	report, err := NewWithGenerator(gen).Translate(context.Background(), r, "en")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if report.Translated != 3 || report.Untranslated != 0 {
		t.Errorf("report = %+v", report)
	}
	if len(r.Segments) != before {
		t.Fatalf("the segment count changed: %d → %d", before, len(r.Segments))
	}
	for i, s := range r.Segments {
		if s.Text["ja"] != fmt.Sprintf("原文%d", i+1) {
			t.Errorf("segment %d lost its original text: %v", i, s.Text)
		}
		if s.Text["en"] == "" {
			t.Errorf("segment %d has no translation: %v", i, s.Text)
		}
	}
	if !r.Metadata.Translated {
		t.Error("metadata.translated was not set")
	}
	if err := r.Validate(); err != nil {
		t.Errorf("the enriched transcript is not valid: %v", err)
	}
}

// The whole point of the numbered protocol: a line that does not come back
// costs that line, and the caller is told.
func TestTranslate_MissingLinesAreReportedNotFabricated(t *testing.T) {
	r := sample(3)
	gen := &fakeGenerator{replies: []string{"1\tone\n3\tthree\n"}}

	report, err := NewWithGenerator(gen).Translate(context.Background(), r, "en")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if report.Translated != 2 || report.Untranslated != 1 {
		t.Errorf("report = %+v, want 2 translated and 1 not", report)
	}
	if _, ok := r.Segments[1].Text["en"]; ok {
		t.Error("the missing line was filled in from somewhere")
	}
	if r.Segments[1].Text["ja"] != "原文2" {
		t.Error("the missing line lost its original")
	}
	if err := r.Validate(); err != nil {
		t.Errorf("a partial translation left the transcript invalid: %v", err)
	}
}

// Preamble, code fences and re-spaced separators are what models actually
// produce. None of them changes which slot a line belongs to.
func TestTranslate_ToleratesTheRepliesModelsActuallySend(t *testing.T) {
	r := sample(2)
	gen := &fakeGenerator{replies: []string{"Sure, here you go:\n```\n1. one\n2) two\n```\n"}}

	report, err := NewWithGenerator(gen).Translate(context.Background(), r, "en")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if report.Translated != 2 {
		t.Errorf("report = %+v", report)
	}
	if r.Segments[0].Text["en"] != "one" || r.Segments[1].Text["en"] != "two" {
		t.Errorf("parsed wrongly: %v / %v", r.Segments[0].Text, r.Segments[1].Text)
	}
}

// An index the request never asked about must not be applied to anything.
func TestTranslate_IgnoresIndicesOutsideTheBatch(t *testing.T) {
	r := sample(2)
	gen := &fakeGenerator{replies: []string{"1\tone\n2\ttwo\n7\tinvented\n0\talso invented\n"}}

	report, err := NewWithGenerator(gen).Translate(context.Background(), r, "en")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if report.Translated != 2 {
		t.Errorf("an out-of-range index was applied: %+v", report)
	}
}

// A failed call costs its batch, not the transcript.
func TestTranslate_FailedCallDegradesToUntranslated(t *testing.T) {
	r := sample(2)
	gen := &fakeGenerator{err: errors.New("upstream is down")}

	report, err := NewWithGenerator(gen).Translate(context.Background(), r, "en")
	if err != nil {
		t.Fatalf("a failed batch should not fail the pass: %v", err)
	}
	if report.Translated != 0 || report.Untranslated != 2 {
		t.Errorf("report = %+v", report)
	}
	if r.Metadata.Translated {
		t.Error("metadata claims a translation that did not happen")
	}
	if err := r.Validate(); err != nil {
		t.Errorf("the transcript did not survive a failed translation: %v", err)
	}
}

func TestTranslate_Batches(t *testing.T) {
	r := sample(maxBatchItems + 5)
	replies := make([]string, 0, 2)
	for _, n := range []int{maxBatchItems, 5} {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d\tt%d\n", i, i)
		}
		replies = append(replies, b.String())
	}
	gen := &fakeGenerator{replies: replies}

	report, err := NewWithGenerator(gen).Translate(context.Background(), r, "en")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if gen.calls != 2 {
		t.Errorf("calls = %d, want 2 batches", gen.calls)
	}
	if report.Translated != maxBatchItems+5 {
		t.Errorf("report = %+v", report)
	}
	// Numbering restarts per batch, so the second batch's line 1 must land on
	// the 41st segment, not the 1st.
	if r.Segments[maxBatchItems].Text["en"] != "t1" {
		t.Errorf("batch offset is wrong: %v", r.Segments[maxBatchItems].Text)
	}
}

func TestTranslate_Rejections(t *testing.T) {
	c := NewWithGenerator(&fakeGenerator{})
	if _, err := c.Translate(context.Background(), sample(1), ""); err == nil {
		t.Error("an empty target should be rejected")
	}
	if _, err := c.Translate(context.Background(), &transcript.Result{}, "en"); !errors.Is(err, transcript.ErrEmpty) {
		t.Error("an empty transcript should report ErrEmpty")
	}
	if _, err := c.Translate(context.Background(), sample(1), "ja-JP"); err == nil {
		t.Error("translating into the language it is already in should be rejected")
	}
}

func TestNameSpeakers(t *testing.T) {
	r := sample(4)
	gen := &fakeGenerator{replies: []string{"spk:0 = 田中\nspk:1 = 佐藤\n"}}

	n, err := NewWithGenerator(gen).NameSpeakers(context.Background(), r, []string{"田中", "佐藤"})
	if err != nil {
		t.Fatalf("NameSpeakers: %v", err)
	}
	if n != 2 {
		t.Errorf("named %d speakers, want 2", n)
	}
	for _, s := range r.Segments {
		if strings.HasPrefix(s.Speaker, "spk:") {
			t.Errorf("segment kept its label: %q", s.Speaker)
		}
	}
	// The hints are the record of who was said to be present.
	if len(r.Metadata.SpeakerHints) != 2 {
		t.Errorf("speaker hints = %v", r.Metadata.SpeakerHints)
	}
	// The prompt has to carry the candidate list, or the constraint is decoration.
	if !strings.Contains(gen.systems[0], "田中") {
		t.Error("the hints did not reach the prompt")
	}
}

// A label the model does not resolve keeps its label. A partial answer is
// normal and must not rename anyone by guesswork.
func TestNameSpeakers_UnresolvedLabelsSurvive(t *testing.T) {
	r := sample(4)
	gen := &fakeGenerator{replies: []string{"spk:0 = 田中\n"}}

	n, err := NewWithGenerator(gen).NameSpeakers(context.Background(), r, nil)
	if err != nil {
		t.Fatalf("NameSpeakers: %v", err)
	}
	if n != 1 {
		t.Errorf("named %d, want 1", n)
	}
	if r.Segments[1].Speaker != "spk:1" {
		t.Errorf("an unresolved speaker was renamed to %q", r.Segments[1].Speaker)
	}
}

func TestParseSpeakerMap(t *testing.T) {
	labels := []string{"spk:0", "spk:1"}
	tests := []struct {
		name  string
		reply string
		want  map[string]string
	}{
		{"plain", "spk:0 = 田中\nspk:1 = 佐藤", map[string]string{"spk:0": "田中", "spk:1": "佐藤"}},
		{"with prose and fences", "Here:\n```\nspk:0 = A\n```", map[string]string{"spk:0": "A"}},
		{"invented label is dropped", "spk:9 = ghost", map[string]string{}},
		{"echoing the label is not a name", "spk:0 = spk:0", map[string]string{}},
		{"an explicit unknown is not a name", "spk:0 = unknown\nspk:1 = 不明", map[string]string{}},
		{"empty name is dropped", "spk:0 = ", map[string]string{}},
		{"garbage is ignored", "I could not tell who was speaking.", map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSpeakerMap(tt.reply, labels)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestNameSpeakers_RefusesAnUndiarizedTranscript(t *testing.T) {
	r := sample(2)
	for i := range r.Segments {
		r.Segments[i].Speaker = transcript.SingleSpeaker
	}
	_, err := NewWithGenerator(&fakeGenerator{}).NameSpeakers(context.Background(), r, nil)
	if err == nil || !strings.Contains(err.Error(), "diarization") {
		t.Errorf("err = %v, want a message pointing at diarization", err)
	}
}

func TestSpeakerSamples_CapsPerSpeaker(t *testing.T) {
	r := sample(40)
	out := speakerSamples(*r, r.Speakers())
	for _, label := range r.Speakers() {
		if !strings.Contains(out, label+":") {
			t.Errorf("samples omit %q", label)
		}
	}
	// One talkative speaker must not crowd the others out of the prompt.
	if lines := strings.Count(out, "  "); lines > 2*6+2 {
		t.Errorf("samples are not capped: %d indented lines", lines)
	}
}
