package transcript

import (
	"strings"
	"testing"
)

func base(speakers ...string) Result {
	r := Result{Metadata: Metadata{Source: "x", Model: "m", Languages: []string{"ja"}, Diarized: true}}
	for i, sp := range speakers {
		r.Segments = append(r.Segments, Segment{
			Start: float64(i), End: float64(i) + 1, Speaker: sp,
			Text: map[string]string{"ja": "line"},
		})
	}
	r.Normalize()
	return r
}

func codes(ds []Diagnosis) map[string]bool {
	out := map[string]bool{}
	for _, d := range ds {
		out[d.Code] = true
	}
	return out
}

func TestDiagnose_Clean(t *testing.T) {
	if ds := Diagnose(base("spk:0", "spk:1")); len(ds) != 0 {
		t.Errorf("a healthy two-speaker transcript was diagnosed: %v", ds)
	}
}

// Two similar voices come back as one speaker with no error. That is the
// failure this diagnosis exists for.
func TestDiagnose_DiarizationDidNotSplit(t *testing.T) {
	if !codes(Diagnose(base("spk:0", "spk:0")))[DiagDiarizationDidNotSplit] {
		t.Error("a single speaker across a multi-segment diarized transcript was not diagnosed")
	}
	// Without diarization one speaker is the expected shape, not a problem.
	r := base("spk:0", "spk:0")
	r.Metadata.Diarized = false
	if codes(Diagnose(r))[DiagDiarizationDidNotSplit] {
		t.Error("an un-diarized transcript should not be diagnosed for having one speaker")
	}
}

func TestDiagnose_ExperimentalSpeakerCount(t *testing.T) {
	if !codes(Diagnose(base("spk:0", "spk:1", "spk:2")))[DiagExperimentalSpeakerCount] {
		t.Error("three speakers should be flagged as beyond reliable attribution")
	}
	if codes(Diagnose(base("spk:0", "spk:1")))[DiagExperimentalSpeakerCount] {
		t.Error("two speakers are within the reliable range")
	}
}

func TestDiagnose_NoTimings(t *testing.T) {
	r := base("spk:0", "spk:1")
	for i := range r.Segments {
		r.Segments[i].Start, r.Segments[i].End = 0, 0
	}
	if !codes(Diagnose(r))[DiagNoTimings] {
		t.Error("a transcript with no timings was not diagnosed")
	}
}

// A partial translation is the failure that looks most like success: the
// transcript is complete and valid, and part of it silently lacks the language
// the caller asked for.
func TestDiagnose_PartialTranslation(t *testing.T) {
	r := base("spk:0", "spk:1")
	r.Segments[0].Text["en"] = "line"
	r.Metadata.Languages = r.Languages()

	ds := Diagnose(r)
	if !codes(ds)[DiagPartialTranslation] {
		t.Fatalf("a half-translated transcript was not diagnosed: %v", ds)
	}
	var msg string
	for _, d := range ds {
		if d.Code == DiagPartialTranslation {
			msg = d.Message
		}
	}
	if !strings.Contains(msg, "1 of 2") {
		t.Errorf("the message should say how much is missing: %q", msg)
	}

	// A fully translated transcript is fine.
	r.Segments[1].Text["en"] = "line"
	if codes(Diagnose(r))[DiagPartialTranslation] {
		t.Error("a fully translated transcript was diagnosed")
	}
}

// Unnamed speakers are only a finding when naming was actually asked for.
func TestDiagnose_UnnamedSpeakersOnlyWhenHintsWereGiven(t *testing.T) {
	r := base("田中", "spk:1")
	if codes(Diagnose(r))[DiagUnnamedSpeakers] {
		t.Error("labels are not a problem when no naming was requested")
	}

	r.Metadata.SpeakerHints = []string{"田中", "佐藤"}
	ds := Diagnose(r)
	if !codes(ds)[DiagUnnamedSpeakers] {
		t.Fatalf("an unresolved label after a naming pass was not diagnosed: %v", ds)
	}
	for _, d := range ds {
		if d.Code == DiagUnnamedSpeakers && !strings.Contains(d.Message, "spk:1") {
			t.Errorf("the message should name the label that survived: %q", d.Message)
		}
	}

	// Everyone named: nothing to report.
	r.Segments[1].Speaker = "佐藤"
	if codes(Diagnose(r))[DiagUnnamedSpeakers] {
		t.Error("a fully named transcript was diagnosed")
	}
}
