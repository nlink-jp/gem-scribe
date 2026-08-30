# ADR-0001: Take the transcript from a structured ASR response, not from LLM-authored JSON

> Status: Accepted
> Date: 2026-08-30

## Context

gem-transcribe, this tool's predecessor, asked a general Gemini model to
transcribe audio and to reply with a JSON document of segments. On long audio
the model produced malformed JSON and the run stopped. Its v0.2.1 added two
mitigations: a per-segment salvage pass that dropped whatever failed to parse,
and a `response_schema` that constrained generation. The release notes record
what happened next — malformed JSON still occurred, the repair usually worked,
and the conclusion drawn at the time was that prevention is not a guarantee.

That is the correct conclusion, and it is also the end of the road for that
architecture. Every mitigation is a way of tolerating a document the model
authored freehand. None of them make the document correct, and the salvage pass
buys its survival by silently discarding content: a transcript that lost forty
seconds of a meeting and a transcript that did not look identical to the caller
apart from a counter.

Vertex AI now serves a dedicated transcription model,
`gemini-3.5-transcribe-preview`.

## Decision

Take the transcript from the model's structured response, and never ask any
model to author the transcript's structure.

The transcription model returns each speaker turn as its own response part:

```
parts[].audioTranscription: { text, speakerLabel, words[]{word, startOffset, endOffset} }
```

The mapping onto the output envelope is mechanical — `speakerLabel` to
`speaker`, the words' first and last offsets to `start` and `end` — and it is
implemented as a pure function (`asr.Parse`) tested against recorded real
responses.

Consequently, gem-scribe has no prompt layer, no response schema, no JSON
repair, and no salvage pass. There is nothing for them to do.

## Consequences

**The failure mode is gone rather than mitigated.** A 31-minute recording came
back as 209 parts with consistent speaker labels and timestamps running to the
end of the audio. There is no path by which a longer recording produces a
structurally invalid transcript, because the structure is not something a model
writes.

**`metadata.dropped_segments` survives as a compatibility field and stays 0.**
It exists in the shared envelope because gem-transcribe needed it. Removing it
would break a consumer written against three tools; nothing increments it here.

**Two capabilities left with the general model.** The dedicated model does not
translate — Japanese audio given `languageCodes: ["en-US"]` comes back in
Japanese, because the field is a detection hint — and it labels speakers
`spk:0`, not by name. Both move to a second pass over the transcript *text*,
which is bounded input rather than hours of audio, and which can be skipped
entirely. This is the same class of work that used to fail, done on a far
smaller input and on a path where failure loses an enrichment rather than the
transcript.

**A new class of silent wrongness appears, and needs its own answer.** The model
returns a perfectly valid transcript when it fails to separate two similar
voices, and its documentation calls attribution beyond two speakers
experimental. Structural validity no longer implies substantive correctness, so
`transcript.Diagnose` names these shapes and the MCP result carries them as
`warning`. An agent cannot hear the audio; without that field it has no way to
tell a good transcript from a confident wrong one.

## Alternatives considered

**Keep gem-transcribe and swap only its LLM layer.** Defensible — the data model
and formatters would have survived. Rejected in favour of starting from
voice-scribe's proven skeleton, which already carries the MCP server, the job
model and the shared envelope, rather than carrying forward a prompt layer, a
schema layer and a salvage layer that all become dead code.

**Use the model's `customVocabulary` for domain terms.** Rejected: supplying it
truncated the transcript to its first turn in every measured run, and reported
`finishReason: STOP` while doing so. A feature that silently discards most of
the content is worse than an absent one.
