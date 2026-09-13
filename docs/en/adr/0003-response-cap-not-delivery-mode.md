# ADR-0003: Cap the response with `max_bytes`; do not switch delivery mode

- Status: Accepted
- Date: 2026-09-14

## Context

`inline_threshold` (default 8192 bytes) switched the *delivery mode* of a
finished transcript: at or below the threshold the result carried the whole
text, above it the text was dropped and replaced by an `excerpt` plus a path.
The file was written either way, so this was never a spill — but what it decided
is the same kind of thing a spill decides: **whether this result fits in the
caller's context**, which the server cannot observe.

The organization settled that on 2026-09-06: file-ising an oversized response is
the runtime's job. splunk-mcp (ADR-0004) and pcap-analyzer-mcp (ADR-0009) acted
on it the same day, replacing their thresholds with an explicit cap and an exact
count of what the cap left out. Their reasoning applies here unchanged: a
threshold is a guess that is wrong for every caller but the one it was tuned
against, and 8 KB — forty minutes of plain text, a few minutes of JSON — is
small enough that a long transcript reached the model as no text at all.

One thing differs from those two. **The transcript file is a product.** An srt
goes to a video player; a json is read by the next tool. It is the counterpart
of pcap-analyzer's extracted objects, which is exactly why that server kept both
its files and `work_dir`. What has to go is not the file: it is the idea that
the size of a response decides what the response contains.

This server is voice-scribe's cloud counterpart and shares its result type
verbatim; the decision is voice-scribe ADR-0011, transplanted.

## Decision

1. Replace `inline_threshold` with **`max_bytes`** (tool argument, default
   65536, `0` means no cap). 65536 matches pcap-analyzer-mcp's
   `output.max_bytes`, so a model meeting a second nlink-jp server meets the
   same number.
2. **The result always carries `text`**, cut at a rune and line boundary so
   Japanese is never corrupted. The `excerpt` field is removed: a preview
   standing in for withheld text has nothing to stand in for.
3. **Count what the cap dropped**: `truncated: true`, an exact `omitted_bytes`,
   and a `note`. `bytes` remains the exact full size and `path` /
   `absolute_path` reach all of it.
4. **The file is written either way.** The cap bounds the response and never
   decides whether the artifact exists, so `work_dir` stays required (ADR-0002).

## Consequences

- Breaking: a call sending `inline_threshold` is refused by strict decoding.
- A long transcript now reaches the model as text rather than as a path alone.
- Callers reading `excerpt` read `text` instead; for short transcripts nothing
  changes.
- voice-scribe carries the same change (its ADR-0011); the two result types stay
  identical, which is what lets downstream tools parse cloud and local
  transcripts alike.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Drop the transcript file too, matching splunk-mcp and pcap-analyzer-mcp exactly | The transcript is a product: an srt handed to a player, a json read downstream. Removing it would make the full text reachable only for the lifetime of the job, and would strip `work_dir` from a server whose output genuinely is a file. pcap-analyzer kept files for `extract_objects` for the same reason |
| Raise `inline_threshold`'s default and keep the name | The name goes on telling the model that crossing a threshold changes the delivery mode. The model reads the name and the description, not the implementation |
| Return the whole transcript always, with no cap | Lets the server flood the caller's context. The knob belongs to the caller — that is the whole point |
| Cap in runes rather than bytes | `bytes` is already reported and the rest of the fleet counts bytes. One unit |
