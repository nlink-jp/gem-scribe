# ADR-0004: Leave path judgement to nlink-jp/pathguard — keep no copy

- Status: Accepted
- Date: 2026-09-22

## Context

Since ADR-0002, `work_dir` validation and the read blacklist (`workdir.Sensitive`) lived in
`internal/mcp/workdir`, a copy of voice-scribe's (the reference implementation of organization
ADR-021); seven other servers held the same copy. Every copy compared places **by name**. APFS is
case-insensitive by default, so `~/.SSH`, `.ENV` and `/USR/local` named the same places and passed
the checks. When the home directory could not be determined, `Sensitive` returned "" and passed
everything.

The organization moved this judgement into one module (`nlink-jp/pathguard`, lib-series). It compares
places by file identity and by names folded the way the disk folds them, and it catches a place that
does not exist yet through the identity of its parent. It holds one list, the same as gem-agent's and
lagent's.

## Decision

- Depend on `github.com/nlink-jp/pathguard` v0.1.0. No code from outside this organization comes
  with it.
- `internal/mcp/workdir` becomes a **thin adapter**. It keeps only:
  - taking the request's `_meta` from the context and passing it to `pathguard/workdir`'s `Resolve`,
  - moving that `*workdir.Error` onto `toolerr` with the same code, message and details,
  - `NewResolver(serverDirs...)` — passing this server's own directories (today only its config
    directory, `~/.config/gem-scribe`) as protected places (`pathguard.ServerDir`), and its one
    sentence for `work_dir_required` as `RequiredHint`. An empty path refuses every call rather than
    protecting nothing (the config directory is undetermined only when the home directory is, and
    then pathguard refuses everything anyway),
  - `Sensitive` — `pathguard/workdir.Sensitive` (the Local policy), passed through.
- The call sites (`Resolve`, `Validate`, `Sensitive`) do not change. What changes is the one line that
  builds the resolver (`workDirResolver` in `cmd/mcp_wiring.go`) and the tests that built it as a zero value.
- The tests of the judgement itself are in pathguard. What stays here are the adapter's tests (taking
  `_meta`, carrying the error across, the protected place, a zero value refusing) and the existing
  contract tests.

## Consequences

`transcribe` behaves differently (the CHANGELOG says so):

- **Refused now**: the real places under your home from the runtimes' list (`~/.kube`,
  `~/.config/gh`, `~/.azure`, `~/.terraform.d`, `~/.gemini`, `~/.config/mcp-bridge`, `~/.netrc`,
  `~/.npmrc`, `~/.pypirc`, `~/.git-credentials`, `~/.vault-token`, `~/.docker/config.json`,
  `~/.claude.json`, `~/.bash_history`, `~/.zsh_history`); every spelling of any floor place — case
  variants, links, firmlinks; wherever a link directly inside one of those directories points (a
  `~/.ssh/config` that links into a sync folder protects the file it points at); when `$HOME` names
  another directory than the account's home, both; Linux `/etc` as a `work_dir`.
- **Accepted now**: `.env.example`, `.env.sample`, `.env.template`, `.env.dist` (templates, not
  secrets).
- **An unknown home refuses audio paths and every `work_dir`.** It used to pass everything.
- `work_dir_denied` carries `reason` in its `details`.
- One check costs about 2 ms (measured in pathguard) — nothing next to a transcription.

With no copy here, a fix to the judgement is a pathguard release and a one-line dependency update.

## Amendment (2026-09-22, v0.5.1): judge the directory actually used

Only `work_dir` was checked, so `work_dir=~/.config` with `workspace_id=gh` made the workspace
`~/.config/gh` — a credential directory — and transcripts were written into it. The hole dates from
the ADR-0002 copy; image-forge's independent review found it.

- `workspace.NewManager(check)` takes the judgement as a required argument, and `EnsureUnder` judges
  `<work_dir>/<workspace_id>` before making or using it. The server passes
  `workdir.Resolver.CheckBeneath` (pathguard v0.2.0); a Manager without one refuses every workspace.
  The wiring is one function, `newToolDeps`, which the tests use.
- pathguard v0.2.0 also refuses a path holding a NUL byte (a path handed to C ends at the NUL, so the
  judged string and the opened one differ).

## Amendment (2026-09-22, v0.5.2): whether a file exists never changes the answer

An absolute `audio` path was resolved with `filepath.EvalSymlinks` before the floor judged it, so a
file in a credential location got `path_not_allowed` when it was there and `input_not_found` when it
was not — the answer told the caller which secrets exist. The `work_dir` candidate of a relative name
was looked at before it was judged too, the "there is a file of that name at …" hint named places the
floor refuses (`~/.docker/config.json`), and a `.env` in the workspace got `unsupported_format` when
present and was reported missing when not. It is the class the independent reviews of
slack-mcp-extender and chrome-pilot-mcp found; here it was measured with the home directory redirected
to a temporary one (13 of 15 pairs got different answers). The code is voice-scribe's, and so is the
fix (voice-scribe ADR-0013, amendment).

- Every place a recording may be read from is placed first (`workdir.Where`, the last of pathguard's
  `Forms`: every link followed, a dangling one by its target — for a path that exists, what
  `EvalSymlinks` returns). The floor (the Local policy) judges it there, as named and as placed
  (`refusal`), and only then is existence asked. An absolute path, both candidates of a relative name
  (the workspace, then `work_dir`, each judged before it is looked at) and the hint's candidates all go
  through that one judgement.
- For an absolute path, existence is asked of the place (`EvalSymlinks(where)`), not re-walked from the spelling, which could
  step through a component the place skipped (a file or a missing entry before a `..`) and answer for
  what lies beyond it. When it resolves elsewhere than it was placed (it changed in between), it is
  judged again there.
- A path that does not resolve gets no branch of its own: in slack-mcp-extender each fix to such a
  branch left another pair of answers apart.
- `TestExistenceIsNotRevealed` calls the same path while a file is there and after it is removed and
  compares the whole answer (code, message, details) — planted and dangling links, a dotfiles-linked
  `~/.config` and the target of `~/.ssh/config` among the cases. `TestPlacementCorners` pins a link
  climbing with `..` past a directory, a file and nothing, and a loop. Seven mutations (the old order,
  a candidate left unjudged, the hint unfiltered, existence re-walked from the spelling, no placement)
  all fail by assertion.
- Known limits in pathguard, recorded for its next release:
  - A `..` that climbs out through an entry of a credential directory — in the path, or in the target
    of a planted link — is judged where it leads, not where it passes, so the answer can still show
    whether that entry is a link and where its target lies: pathguard judges cleaned forms, not the
    directories a walk passes through.
  - The place is the last of pathguard's forms. When a chain of links comes back to a spelling already
    met, that is an earlier hop rather than the end; every hop has been judged, so nothing unjudged is
    opened, but a file reached that way can be reported missing or read from the earlier hop.
    pathguard does not expose the final place.
  - `work_dir` is validated by pathguard/workdir in the order organization ADR-022 §4 sets (not found
    before denied), so a `work_dir` naming a credential directory is answered by whether it exists.
  - A link target with a non-ASCII name spelled in another Unicode normalisation is found by identity
    only while it exists (pathguard does not normalise). A hard link made elsewhere is refused only when
    it is to a file that is itself a place on the floor (`~/.netrc`, `~/.docker/config.json`, …), and
    only while it exists; one to a file inside a credential directory (`~/.ssh/id_rsa`) or to a `.env`
    is not refused at all — a directory is compared by its own identity, not by its files'.
- The judgement and the read are two steps, and a link swapped in between them is followed: a
  check-to-use race, not closed here (closing it means judging what was opened, by its descriptor).

## References

- Organization ADR-021 (the work-dir contract of the file-mediated MCP servers)
- ADR-0002 (work-dir contract): the closed list of checks and the read blacklist — whose
  implementation this replaces
- nlink-jp/pathguard's RFP (`docs/en/pathguard-rfp.md`)
