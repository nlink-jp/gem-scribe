# ADR-0005: MCP のプロトコル層を nlink-jp/mcp にする — 新旧両方の時代を話す

| 項目 | 値 |
|-------|-----|
| Status | **Accepted**（2026-11-28） |
| Date | 2026-11-28 |
| Binds | gem-scribe（`gem-scribe mcp`） |
| Decision makers | nlink-jp maintainers |
| Triggered by | 組織 ADR-030（MCP の実装は新旧両方の時代を話し、プロトコル層は nlink-jp/mcp だけから取る）。issue nlink-jp/gem-scribe#1 |

## Context

`internal/mcp/mcpserver`・`internal/mcp/transport`・`internal/mcp/jsonrpc` は、手書きの stdio MCP サーバだった。
版は固定の 2024-11-05 で、旧方式（`initialize` の握手）だけを話す。そのため、2026-07-28 版の新方式専用のクライアント
からはつながらない。gem-scribe は家系 C（mcpserver。nlink-jp/mcp の文書 server-migration §1）に属し、兄弟の
voice-scribe・image-forge も同じ決定をする。家系の参照実装は data-toolbox-mcp（同リポの ADR-0013）である。実際の
相手の測り直し（2026-11-27、ADR-030 §7）は asn-lookup の ADR-0001 にある。Claude Code 2.1.311・gem-agent・lagent は、
どれも両対応だった。

## Decision

共通の方針（nlink-jp/mcp の `docs/ja/server-migration.ja.md`、v0.3.0）に従う。このリポに当てはまる項目は次のとおり。

1. **外から見える形は変えない（方針 §2）。** `mcpserver.New(name, version, transport, logger)`・`RegisterTool`・
   `Tools`・`Call`・`SetInstructions`・`RequestMeta`・`WithRequestMeta`・`RawResult`・`ContentBlock` を残し、足す
   ものは無い。`Serve(ctx)` は、登録順に `AddTool` してから `ToolServer.Serve` を呼ぶ。ハンドラの値は、これまでと
   同じ形の結果に写す。`req.Meta` は ctx に載せる。`Call` は、ツールの試験が使うプロセス内の道として残す。
2. **transport（方針 §2）**: `transport.NewStdioTransport(in, out)` は、入出力の入れ物として残す（`In`・`Out`）。
   1 MiB の `bufio.Scanner` と、読み書きの処理は消す。stdout の確保（fd 1 を stderr に向け、プロトコルは専用の複製に
   流す）はそのまま。`internal/mcp/jsonrpc` は消す。
3. **選択肢（方針 §2）**: `Workers` は 1（呼び出しは 1 つずつ処理していた）、`QueueDepth` は 256。ロガーが無ければ、
   `New` は Warn 以上を stderr に出す（これまでは捨てていた）。
4. **信号（方針 §2）**: `mcp` サブコマンドは信号をひとつも扱っておらず、SIGINT・SIGTERM でプロセスは、動いている文字起こしごと、すぐに終わっていた。いまは、それらがサーバの ctx（`signal.NotifyContext`）を、ひいてはジョブのマネージャの ctx を、読み取りの途中でも取り消す。`Serve` はモジュールのシャットダウンの猶予（5 秒）のうちに戻る。
5. **変わる振る舞い（方針 §4）**:
   - 未知のツールは -32601 から -32602 になる。`ping` には -32601 ではなく `{}` を返す。
   - initialize の前の `tools/*` は -32602 になる。オブジェクトでない `arguments` も -32602 になる。
   - `"id":null` の要求は -32600 になる（これまでは通知として黙っていた）。
   - 1 行の上限は 1 MiB から 16 MiB になる。上限を超えた行は -32600 を返して読み続ける（これまでは止まった）。
     空行には答えなくなる（これまでは -32700）。
   - stdout への書き込みの失敗で止まる（これまではログに出して読み続けた）。
   - ツールの panic には -32603 を返し、サーバは動き続ける（これまではプロセスが終わった）。
   - 版は 2024-11-05 固定から、旧方式の交渉と新方式に変わる。
6. **取り消し（方針 §5）**: `notifications/cancelled` で呼び出しの ctx が取り消され、モジュールはその呼び出しには
   答えない。`transcribe` は、いつもサーバの寿命の ctx のジョブとして動くので、それを出した呼び出しを取り消しても
   止まらない。同期のツールは読むだけである。信号はジョブを取り消す。
7. **依存**: `github.com/nlink-jp/mcp` を足す。
8. **版**: minor を上げる（v0.7.0）。

## 見つけた不具合（別の fix コミットで直した）

- **信号や入力の終わりで、ステージングした録音がバケットに残っていた。** `mcp` サブコマンドは信号を扱っておらず、SIGINT・
  SIGTERM で文字起こしの途中に殺されていた。EOF でも、ジョブが動いている間にプロセスが終わっていた。どちらでも、ジョブの
  後回しにした後始末（Cloud Storage にステージングした録音を消すこと）が走らなかった。いまは、`Serve` が戻ったら、
  信号でも EOF でも（stdin を閉じたクライアントは、もう結果を受け取れない）ジョブを取り消し、動いているものが片づく
  まで最大 30 秒待つので、後始末が走る（`drainJobs`・`Manager.Wait`。別のコミットで直した。試験つき）。
- **待ち行列のジョブが、取り消しの後に始まることがあった。** 働き手の `select` は、空いた待ち行列と取り消された ctx の
  どちらも選びうるので、取り消しの後に待ち行列のジョブを取って始めることがあった。そのとき取ったジョブは、もう始めない
  （50 回まわす試験つき）。
- **文字起こしの途中で取り消されたジョブが、文字起こしを書いていた。** 書く前に ctx を確かめるようにした。

## Consequences

- 新方式専用のクライアントからもつながる。旧方式のクライアントには、これまでどおり答える。
- 試験はモジュールの `client` で動かす（`internal/mcp/mcpserver` と、`mcp` サブコマンドの試験）。探りのクライアント
  3 種の組み合わせと、信号の試験を足した。ツールの試験は、これまでどおり `Call` でハンドラをプロセス内で呼ぶ。

## Alternatives considered

| 案 | 採らない理由 |
|---|---|
| `mcpserver` を消し、ツールを直接モジュールに登録する | ツールと試験の登録の形がすべて変わる。薄い層を残せば、ツールは変わらない |

## References

- 組織 ADR-030、ADR-025
- nlink-jp/mcp v0.3.0: `docs/ja/server-migration.ja.md`
- data-toolbox-mcp ADR-0013（家系 C の参照実装）
- asn-lookup ADR-0001（実際の相手の測り直し）
- issue nlink-jp/gem-scribe#1
