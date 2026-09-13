# ADR-0002: work dir は呼び出しごとの `work_dir` で受け取り、既定ルートを持たない

> Status: Accepted
> Date: 2026-09-13

## 背景

組織 ADR-021（ファイル渡し MCP サーバーの work dir 契約）をこのサーバーにも適用する。
参照実装は voice-scribe（同 ADR-0010）、絶対パス入力とブラックリストの形は
pcap-analyzer-mcp（同 ADR-0008）で確定済みで、ここはそれを受け取る側である。

このサーバーの `workspace_root` は省略可で、省略するとサーバー既定のルート
（`~/.local/share/gem-scribe/mcp-workspaces` 相当）に書いていた。呼び出し側の
ファイルツールはそこを開けないので、**ジョブは成功し、返ったパスは開けない**という
形でしか失敗が出ない。4 ランタイム（Claude Code / ChatGPT Codex / gem-agent / lagent）
の実測では、MCP の `roots` も環境変数も半数には届かず、**呼び出しごとの引数だけが
共通の経路**である。

## 決定

1. **引数は `work_dir`、`transcribe` で必須。** 意味は「呼び出し側が読み戻せる絶対
   パス」。ワークスペースは `<work_dir>/<workspace_id>/`。
2. **解決順は 引数 → `_meta["jp.nlink/work_dir"]` → エラー。** サーバー既定は持たない。
   `defaultWorkspaceRoot()` と `Manager` の既定ルート操作は削除する。
3. **検証は閉じた一覧**（絶対 / `~` 無し / `..` 無し / 存在する dir / 書込可 /
   システム・資格情報の位置でない）。コードは `work_dir_*` の 5 つ。dir は作らない。
4. **`audio` は絶対パスを受け付ける。** その場で読み、コピーしない。1 時間の録音を
   ワークスペースへ複製してから文字起こしするのは純粋な無駄で、呼び出し側はその
   ファイルを自分でも読める。相対パスは従来どおりワークスペース相対
   （`os.Root` によるカーネル封じ込め）。
5. **拒否するのは資格情報・エージェント制御ファイルの位置**（`~/.ssh`、`~/.aws`、
   `~/.gnupg`、`~/.config/gcloud`、`~/Library/Keychains`、`~/.claude`、`~/.codex`、
   任意の `.env`）。**照合はパスの両方の綴り（渡されたまま／symlink 解決後）×
   項目側の両方の綴り**で行う —— 解決してからだけ照合すると、`~/.ssh/config` のような
   リンク経由のパスが素通りする（pcap-analyzer が実機で発見）。
   ブラックリストは**床であって境界ではない**。
6. **結果は `work_dir` と `workspace_id` を反響する。** `_meta` 経由で渡された
   呼び出し側は、結果からしか行き先を知れない。
7. 強制はテスト: 旧綴りがスキーマに無いこと、`work_dir` を宣言したら必須であること、
   `_meta` 経路が通ること。

## 影響

- **破壊的。** `workspace_root` を送る呼び出しは新しい名前を告げて拒否される
- 既定ルートが消え、`internal/mcp/workspace` は「呼び出し側 dir の下に作る」だけになる
- `internal/mcp/workdir` は voice-scribe / pcap-analyzer と**同一ファイル**（移植物）

## 参照

- 組織 ADR-021（work dir 契約）、voice-scribe ADR-0010（参照実装）、
  pcap-analyzer-mcp ADR-0008（絶対パス入力とブラックリストの形）
