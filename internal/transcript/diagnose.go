package transcript

import "fmt"

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

	if len(r.Segments) > 0 && r.Duration() == 0 {
		out = append(out, Diagnosis{
			Code: DiagNoTimings,
			Message: "the transcript carries no timings; ask for word timestamps if you need " +
				"to locate anything in the audio.",
		})
	}

	return out
}
