package enrich

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
	"google.golang.org/genai"
)

// Batching limits. Small batches keep a bad response cheap; these sizes put a
// typical hour-long meeting in the tens of calls rather than hundreds.
const (
	maxBatchItems = 40
	maxBatchChars = 6000
)

// Generator is the model call. It is an interface so every decision in this
// package — batching, parsing, what happens to a line that does not come back —
// is testable without a network or a GCP project.
type Generator interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Client enriches transcripts with a general Gemini model.
type Client struct {
	gen Generator
}

// New builds a Client against the configured second-pass model.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	if err := cfg.RequireProject(); err != nil {
		return nil, err
	}
	inner, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.GCP.Project,
		Location: cfg.SecondPassLocation(),
	})
	if err != nil {
		return nil, fmt.Errorf("create second-pass client: %w", err)
	}
	return &Client{gen: &vertexGenerator{inner: inner, model: cfg.SecondPass.Model}}, nil
}

// NewWithGenerator builds a Client over an arbitrary generator.
func NewWithGenerator(g Generator) *Client { return &Client{gen: g} }

type vertexGenerator struct {
	inner *genai.Client
	model string
}

func (v *vertexGenerator) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	cfg := &genai.GenerateContentConfig{}
	if systemPrompt != "" {
		cfg.SystemInstruction = genai.NewContentFromText(systemPrompt, "")
	}
	resp, err := v.inner.Models.GenerateContent(ctx, v.model,
		[]*genai.Content{genai.NewContentFromText(userPrompt, genai.RoleUser)}, cfg)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range resp.Candidates {
		if c.Content == nil {
			continue
		}
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

// TranslateReport says what a translation pass actually achieved.
//
// Untranslated is the number that matters and the reason this is a struct: a
// pass that silently left a third of a meeting in the original language must
// not look like a success, and the caller is the only one who can decide what
// to do about it.
type TranslateReport struct {
	Target       string
	Translated   int
	Untranslated int
}

// Translate adds a translation to every segment, in place.
//
// The segments are not re-created, re-ordered or re-counted — only a key is
// added to each one's text map. A segment the model did not return keeps the
// languages it already had, and is counted in the report.
func (c *Client) Translate(ctx context.Context, r *transcript.Result, target string) (TranslateReport, error) {
	target = baseLanguage(target)
	if target == "" {
		return TranslateReport{}, fmt.Errorf("translation target language is empty")
	}
	report := TranslateReport{Target: target}
	if len(r.Segments) == 0 {
		return report, transcript.ErrEmpty
	}

	source := primaryLanguage(*r)
	if source == target {
		return report, fmt.Errorf("the transcript is already in %q", target)
	}

	originals := make([]string, len(r.Segments))
	for i, s := range r.Segments {
		originals[i] = textIn(s, source)
	}

	for _, batch := range chunk(originals, maxBatchItems, maxBatchChars) {
		items := make([]string, len(batch))
		for i, idx := range batch {
			items[i] = originals[idx]
		}

		reply, err := c.gen.Generate(ctx, translateSystemPrompt(source, target), numberedBlock(items))
		if err != nil {
			// One failed batch is not a failed transcript: the rest of the
			// translation still lands, and the report says what is missing.
			report.Untranslated += len(batch)
			continue
		}

		got := parseNumberedBlock(reply, len(items))
		for i, idx := range batch {
			text, ok := got[i+1]
			if !ok {
				report.Untranslated++
				continue
			}
			r.Segments[idx].Text[target] = text
			report.Translated++
		}
	}

	if report.Translated > 0 {
		r.Metadata.Translated = true
		r.Metadata.Languages = r.Languages()
	}
	return report, nil
}

// NameSpeakers replaces the model's spk:N labels with real names.
//
// hints are candidate names. With them the task is an assignment among known
// people; without them the model has to find names the speakers used for each
// other, which it will sometimes get wrong — so a label it does not resolve
// stays exactly as it was.
func (c *Client) NameSpeakers(ctx context.Context, r *transcript.Result, hints []string) (int, error) {
	labels := r.Speakers()
	if len(labels) == 0 {
		return 0, transcript.ErrEmpty
	}
	if len(labels) == 1 && labels[0] == transcript.SingleSpeaker {
		return 0, fmt.Errorf("the transcript has no speaker labels to name; transcribe with diarization first")
	}

	reply, err := c.gen.Generate(ctx, nameSystemPrompt(hints), speakerSamples(*r, labels))
	if err != nil {
		return 0, fmt.Errorf("name speakers: %w", err)
	}

	names := parseSpeakerMap(reply, labels)
	if len(names) == 0 {
		return 0, nil
	}
	for i := range r.Segments {
		if name, ok := names[r.Segments[i].Speaker]; ok {
			r.Segments[i].Speaker = name
		}
	}
	if len(hints) > 0 {
		r.Metadata.SpeakerHints = hints
	}
	return len(names), nil
}

// parseSpeakerMap reads "spk:0 = 田中" pairs, accepting only labels that were
// actually asked about.
//
// Refusing unknown labels is the guard that matters: a model that invents
// "spk:7" for a two-speaker recording would otherwise have its invention
// applied to nothing, and a model that echoes the label unchanged would rename
// a speaker to itself.
func parseSpeakerMap(reply string, labels []string) map[string]string {
	known := make(map[string]bool, len(labels))
	for _, l := range labels {
		known[l] = true
	}

	out := map[string]string{}
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		label, name, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		label = strings.TrimSpace(label)
		name = strings.TrimSpace(name)
		if !known[label] || name == "" || name == label {
			continue
		}
		// An unresolved speaker must stay labelled, not be renamed to a word
		// meaning "unknown" — the label is at least honest.
		if isUnknownMarker(name) {
			continue
		}
		out[label] = name
	}
	return out
}

func isUnknownMarker(name string) bool {
	switch strings.ToLower(strings.Trim(name, "?？「」\"'")) {
	case "unknown", "unclear", "n/a", "none", "null", "-", "不明", "未特定":
		return true
	}
	return false
}

// speakerSamples gives the model the evidence a name can be found in: what each
// speaker said, capped so one talkative person cannot crowd out the rest.
func speakerSamples(r transcript.Result, labels []string) string {
	const perSpeaker = 6

	lang := primaryLanguage(r)
	lines := map[string][]string{}
	for _, s := range r.Segments {
		if len(lines[s.Speaker]) < perSpeaker {
			lines[s.Speaker] = append(lines[s.Speaker], collapseNewlines(textIn(s, lang)))
		}
	}

	var b strings.Builder
	for _, label := range labels {
		fmt.Fprintf(&b, "%s:\n", label)
		for _, l := range lines[label] {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	return b.String()
}

func translateSystemPrompt(source, target string) string {
	return fmt.Sprintf(`You are translating one speaker turn per line from %s into %s.

Rules:
- Reply with the same numbers, one line each, in the same order: "<number>\t<translation>".
- Translate every line you are given. Never merge, split, drop or reorder lines.
- Output nothing but the numbered lines — no preamble, no commentary, no code fences.
- A line that is a fragment stays a fragment. Do not complete it.
- Keep proper nouns as they are unless the target language has an established form.`, source, target)
}

func nameSystemPrompt(hints []string) string {
	var b strings.Builder
	b.WriteString(`You are assigning real names to the speaker labels in a transcript.

Rules:
- Reply with one "<label> = <name>" pair per line, and nothing else.
- Use only the labels you are given. Never invent a label.
- Base each name on evidence in the transcript: a self-introduction, or another
  speaker addressing them.
- If a label's name is not established by the transcript, omit that line
  entirely. Omitting is correct; guessing is not.`)
	if len(hints) > 0 {
		fmt.Fprintf(&b, "\n- The people present are: %s. Assign from this list only.",
			strings.Join(hints, ", "))
	}
	return b.String()
}

// primaryLanguage is the language a transcript is considered to be in.
func primaryLanguage(r transcript.Result) string {
	if len(r.Metadata.Languages) > 0 {
		return r.Metadata.Languages[0]
	}
	if langs := r.Languages(); len(langs) > 0 {
		return langs[0]
	}
	return ""
}

// textIn returns a segment's text in lang, falling back to whatever it has.
func textIn(s transcript.Segment, lang string) string {
	if t, ok := s.Text[lang]; ok {
		return t
	}
	keys := make([]string, 0, len(s.Text))
	for k := range s.Text {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return s.Text[keys[0]]
}

func baseLanguage(tag string) string {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	return strings.ToLower(tag)
}
