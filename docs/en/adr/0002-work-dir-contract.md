# ADR-0002: Take the work directory as a per-call `work_dir`, with no default root

> Status: Accepted — its implementation (the checks and the read blacklist) is replaced by
> ADR-0004 (nlink-jp/pathguard)
> Date: 2026-09-13

## Context

This applies organization ADR-021 (the work-directory contract for file-mediated
MCP servers) to this server. voice-scribe is the reference implementation (its
ADR-0010); pcap-analyzer-mcp settled absolute-path inputs and the blacklist (its
ADR-0008). This server is a recipient of that shape, not an author of it.

`workspace_root` here was optional, and omitting it wrote under a server-owned
root (`~/.local/share/gem-scribe/mcp-workspaces`). No calling agent's file tools
can read that, so the failure only ever appeared as **a successful job returning
a path that cannot be opened**. Measurement of the four calling runtimes (Claude
Code, ChatGPT Codex, gem-agent, lagent) showed that neither MCP `roots` nor the
environment reaches half of them: **the per-call argument is the only common
channel.**

## Decision

1. **The argument is `work_dir`, required by `transcribe`.** It means the
   absolute path of a directory the caller can read back; the workspace is
   `<work_dir>/<workspace_id>/`.
2. **Resolution is argument → `_meta["jp.nlink/work_dir"]` → error.** No
   server-owned default: `defaultWorkspaceRoot()` and the manager's default-root
   operations are deleted.
3. **Validation is a closed list** (absolute, no `~`, no `..`, exists and is a
   directory, writable, not a system or credential location), with five
   `work_dir_*` codes. The directory is not created.
4. **`audio` may be an absolute path**, read in place and never copied: staging
   an hour of audio into the workspace to transcribe it is waste, and the caller
   could have read the file itself. Relative paths stay workspace-relative with
   kernel-enforced containment.
5. **What is refused there** is a credential or agent-control location (`~/.ssh`,
   `~/.aws`, `~/.gnupg`, `~/.config/gcloud`, `~/Library/Keychains`, `~/.claude`,
   `~/.codex`, any `.env`), checked on **both spellings of the path — as given and
   symlink-resolved — against both spellings of every entry**. Resolving alone
   walks past a link such as `~/.ssh/config` pointing into a cloud-sync folder,
   which pcap-analyzer found by being driven for real. The blacklist is a floor,
   not a boundary.
6. **Results echo `work_dir` and `workspace_id`.** A caller whose runtime supplied
   the directory through `_meta` learns the destination from the result and
   nowhere else.
7. Enforcement is a test: no retired spelling in any schema, `work_dir` required
   wherever it is declared, and the `_meta` channel exercised end to end.

## Consequences

- **Breaking.** A call sending `workspace_root` is refused with the new name.
- The default root is gone; `internal/mcp/workspace` only materializes workspaces
  under the caller's directory.
- `internal/mcp/workdir` is byte-identical to voice-scribe's and pcap-analyzer's
  copies — it is a transplant, not a local design.

## References

- Organization ADR-021 (the work-directory contract), voice-scribe ADR-0010 (the
  reference implementation), pcap-analyzer-mcp ADR-0008 (absolute inputs and the
  blacklist shape)
