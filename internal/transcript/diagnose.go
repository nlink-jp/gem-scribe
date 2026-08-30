package transcript

import (
	"fmt"
	"strings"
)

// Diagnosis is a transcript that is well-formed but probably not what the
// caller wanted. An agent cannot see or hear the audio, so a result that is
// structurally perfect and substantively wrong is indistinguishable from a
// good one unless something says so.
type Diagnosis struct {
	Code    string
	Message string
}

func (d Diagnosis) String() string { return d.Message }

// Diagnosis codes.
const (
	// DiagExperimentalSpeakerCount marks a transcript with more speakers than
	// the model's attribution is documented to be reliable for.
	DiagExperimentalSpeakerCount = "experimental_speaker_count"
	// DiagDiarizationDidNotSplit marks diarization that returned everyone as
	// one speaker.
	DiagDiarizationDidNotSplit = "diarization_did_not_split"
	// DiagNoTimings marks a transcript whose segments carry no time at all.
	DiagNoTimings = "no_timings"
	// DiagPartialTranslation marks segments the translation pass did not reach.
	DiagPartialTranslation = "partial_translation"
	// DiagUnnamedSpeakers marks speaker labels the naming pass did not resolve.
	DiagUnnamedSpeakers = "unnamed_speakers"
)

// reliableSpeakerCount is how many speakers the model's documentation treats as
// non-experimental. Attribution for three or more is documented as experimental,
// so a cast of five is a result to double-check rather than to trust.
const reliableSpeakerCount = 2

// Diagnose inspects a finished transcript for the failure shapes this model
// actually produces. Each one was observed, not imagined.
func Diagnose(r Result) []Diagnosis {
	var out []Diagnosis

	speakers := r.Speakers()
	if r.Metadata.Diarized {
		// Two acoustically similar voices come back as a single speaker, with
		// no error and a perfectly valid transcript — measured with two
		// synthetic voices that a listener separates immediately.
		if len(speakers) == 1 && len(r.Segments) > 1 {
			out = append(out, Diagnosis{
				Code: DiagDiarizationDidNotSplit,
				Message: "diarization returned a single speaker for the whole recording; " +
					"if you expected several, their voices may be too similar for the model to separate.",
			})
		}
		if len(speakers) > reliableSpeakerCount {
			out = append(out, Diagnosis{
				Code: DiagExperimentalSpeakerCount,
				Message: fmt.Sprintf("%d speakers were labelled; the model documents attribution beyond %d as experimental, "+
					"so check the assignments before relying on them.", len(speakers), reliableSpeakerCount),
			})
		}
	}

	// A partial second pass is the failure mode that looks most like success:
	// the transcript is complete and valid, and part of it is simply missing
	// the language the caller asked for.
	if missing := missingTranslations(r); missing > 0 {
		out = append(out, Diagnosis{
			Code: DiagPartialTranslation,
			Message: fmt.Sprintf("%d of %d segments were not translated and carry only the original; "+
				"the transcript is complete but the translation is not.", missing, len(r.Segments)),
		})
	}

	if unnamed := unnamedSpeakers(r); len(r.Metadata.SpeakerHints) > 0 && len(unnamed) > 0 {
		out = append(out, Diagnosis{
			Code: DiagUnnamedSpeakers,
			Message: fmt.Sprintf("%d speaker label(s) could not be matched to a name and kept theirs (%s); "+
				"the transcript did not establish who they are.", len(unnamed), strings.Join(unnamed, ", ")),
		})
	}

	if len(r.Segments) > 0 && r.Duration() == 0 {
		out = append(out, Diagnosis{
			Code: DiagNoTimings,
			Message: "the transcript carries no timings; ask for word timestamps if you need " +
				"to locate anything in the audio.",
		})
	}

	return out
}

// missingTranslations counts segments that lack a language the transcript as a
// whole carries. It is derived from the segments rather than recorded by the
// translator so that it stays true no matter which path produced the result.
func missingTranslations(r Result) int {
	langs := r.Languages()
	if len(langs) < 2 {
		return 0
	}
	var missing int
	for _, s := range r.Segments {
		if len(s.Text) < len(langs) {
			missing++
		}
	}
	return missing
}

// unnamedSpeakers lists labels that still look like the model's own, which is
// what a naming pass leaves behind when the transcript does not say who
// someone is.
func unnamedSpeakers(r Result) []string {
	var out []string
	for _, s := range r.Speakers() {
		if strings.HasPrefix(s, "spk:") {
			out = append(out, s)
		}
	}
	return out
}
