# ADR-0005: The MCP protocol layer becomes nlink-jp/mcp — both eras

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-28) |
| Date | 2026-11-28 |
| Binds | gem-scribe (`gem-scribe mcp`) |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-030 (every MCP implementation speaks both eras and takes its protocol layer only from nlink-jp/mcp); issue nlink-jp/gem-scribe#1 |

## Context

`internal/mcp/mcpserver`, `internal/mcp/transport` and `internal/mcp/jsonrpc` were a hand-written stdio MCP server,
fixed at 2024-11-05 and speaking the legacy era only (the `initialize` handshake), so a modern-only client of the
2026-07-28 revision could not reach it. gem-scribe belongs to family C (mcpserver; nlink-jp/mcp's server-migration §1), with
its siblings voice-scribe and image-forge, which take the same decision; the family's reference implementation is
data-toolbox-mcp (its ADR-0013). The re-measurement of the real peers (2026-11-27, ADR-030 §7) is recorded in
asn-lookup's ADR-0001: Claude Code 2.1.311, gem-agent and lagent are all dual-era.

## Decision

Follow the common policy (nlink-jp/mcp's `docs/en/server-migration.md`, v0.3.0), with these items for this
repository:

1. **The shape seen from outside stays (policy §2).** `mcpserver.New(name, version, transport, logger)`,
   `RegisterTool`, `Tools`, `Call`, `SetInstructions`, `RequestMeta`, `WithRequestMeta`, `RawResult` and
   `ContentBlock` stay; nothing is added. `Serve(ctx)` calls `AddTool` in registration order, then
   `ToolServer.Serve`. A handler's value becomes the same content as before; `req.Meta` goes into the ctx. `Call`
   stays the in-process path the tool tests use.
2. **Transport (policy §2)**: `transport.NewStdioTransport(in, out)` stays as the holder of the two streams (`In`,
   `Out`); its 1 MiB `bufio.Scanner` and read/write code go. The claim on stdout (fd 1 pointed at stderr, the protocol
   on a private duplicate) is untouched. `internal/mcp/jsonrpc` goes.
3. **Options (policy §2)**: `Workers` 1 (calls were served one at a time), `QueueDepth` 256. Without a logger of its
   own, `New` logs Warn and above to stderr (it discarded them).
4. **Signals (policy §2)**: The `mcp` subcommand handled no signal, so SIGINT and SIGTERM killed the process at once, a running transcription with it. They now cancel the server's ctx (`signal.NotifyContext`), and with it the job manager's, even mid-read; `Serve` returns within the module's shutdown grace (5 s).
5. **Behaviour that changes (policy §4)**:
   - An unknown tool is -32602 instead of -32601; `ping` gets `{}` instead of -32601.
   - `tools/*` before initialize is -32602, and so are `arguments` that is not an object.
   - A request with `"id":null` gets -32600 (it was silent, as a notification).
   - The line cap goes from 1 MiB to 16 MiB; a line over it gets -32600 and reading goes on (it stopped). A blank line
     is no longer answered (it got -32700).
   - A failed write to stdout stops the server (it was logged and reading went on).
   - A panic in a tool is answered -32603 and the server goes on (it ended the process).
   - The version moves from a fixed 2024-11-05 to legacy negotiation and the modern era.
6. **Cancellation (policy §5)**: `notifications/cancelled` cancels a call's ctx and the module sends no answer for
   it. `transcribe` always run as jobs under the server's lifetime ctx, so cancelling the call that submitted one does
   not stop it; the synchronous tools only read. A signal cancels the jobs.
7. **Dependency**: `github.com/nlink-jp/mcp` is added.
8. **Version**: minor (v0.7.0).

## Defects found (fixed in their own fix commit)

- **A signal or the end of input left a staged recording in the bucket.** The `mcp` subcommand handled no signal, so
  SIGINT and SIGTERM killed it mid-transcription; and at EOF the process exited while a job was still running. Either
  way the job's deferred cleanup — deleting the recording it staged in Cloud Storage — never ran. Now the server
  cancels its jobs once `Serve` returns, for a signal or for EOF (a client that closed stdin can no longer collect a
  result), and waits up to 30 s for the running one to unwind, so the cleanup runs (`drainJobs`, `Manager.Wait`;
  fixed in its own commit, with tests).
- **A job still queued could start after the cancellation.** The worker's `select` picks at random between a ready
  queue and the cancelled ctx, so it could take a queued job after the cancellation and start it. A job taken then is
  no longer started, with a test run 50 times.
- **A job cancelled while it transcribed still wrote its transcript.** It now checks the ctx before writing.

## Consequences

- Modern-only clients reach it too; legacy clients are answered as before.
- Tests run through the module's `client` (`internal/mcp/mcpserver` and the `mcp` subcommand's tests); a test
  against the three probe clients and a signal test were added. The tool tests keep calling handlers in-process
  through `Call`.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Drop `mcpserver` and register the tools on the module directly | Every tool's and test's registration would change; the thin layer leaves the tools unchanged |

## References

- Organization ADR-030, ADR-025
- nlink-jp/mcp v0.3.0: `docs/en/server-migration.md`
- data-toolbox-mcp ADR-0013 (family C's reference implementation)
- asn-lookup ADR-0001 (the re-measured peers)
- Issue nlink-jp/gem-scribe#1
