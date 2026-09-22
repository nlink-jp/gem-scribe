# ADR-0004: パスの判定は nlink-jp/pathguard に任せる — 写しを持たない

- Status: Accepted
- Date: 2026-09-22

## Context

ADR-0002 以来、`work_dir` の検証と、読み取りのブラックリスト（`workdir.Sensitive`）は
`internal/mcp/workdir` にあった。voice-scribe（組織 ADR-021 の参照実装）からの写しで、同じ写しが
ほかの 7 サーバーにもあった。どの写しも場所を**名前で**比べていた。APFS は既定で大文字小文字を区別しないので、
`~/.SSH`、`.ENV`、`/USR/local` が同じ場所を指しながら検査を通った。ホームディレクトリが分からない
ときは `Sensitive` が "" を返し、すべてを通した。

組織はこの判定を 1 つのモジュールにまとめた（`nlink-jp/pathguard`、lib-series）。場所を
ファイルの実体と、ディスクと同じやり方で同一視した名前の両方で比べ、まだ存在しない場所も
その親の実体で捕まえる。一覧は gem-agent・lagent と同じものを 1 つ持つ。

## Decision

- `github.com/nlink-jp/pathguard` v0.1.0 を依存に加える。この org の外のコードは入らない。
- `internal/mcp/workdir` は**薄いアダプタ**にする。持つのは次だけ:
  - リクエストの `_meta` を文脈から取り出して `pathguard/workdir` の `Resolve` に渡すこと、
  - その `*workdir.Error` を `toolerr` の同じ code・message・details に移すこと、
  - `NewResolver(serverDirs...)` —— このサーバー自身のディレクトリ（今は設定ディレクトリ
    `~/.config/gem-scribe` だけ）を守る場所（`pathguard.ServerDir`）として渡し、`work_dir_required` の
    1 文を `RequiredHint` で添える。空のパスは何も守らないのではなく、すべての呼び出しを拒ませる
    （設定ディレクトリが決まらないのはホームが分からないときで、そのときは pathguard もすべてを拒む）、
  - `Sensitive` —— `pathguard/workdir.Sensitive`（Local の方針）をそのまま出す。
- 呼び出し箇所（`Resolve`・`Validate`・`Sensitive`）は変えない。変わるのは組み立ての 1 行
  （`cmd/mcp_wiring.go` の `workDirResolver`）と、ゼロ値で組み立てていたテストだけである。
- 判定そのもののテストは pathguard にある。ここに残すのはアダプタのテスト（`_meta` の取り出し、
  エラーの写し、守る場所、ゼロ値が拒むこと）と、既存の契約テストである。

## Consequences

`transcribe` の挙動が変わる（CHANGELOG に書く）:

- **新たに拒む**: ランタイムと同じ一覧のうち、自分のホームにある本物の場所
  （`~/.kube`、`~/.config/gh`、`~/.azure`、`~/.terraform.d`、`~/.gemini`、`~/.config/mcp-bridge`、
  `~/.netrc`、`~/.npmrc`、`~/.pypirc`、`~/.git-credentials`、`~/.vault-token`、`~/.docker/config.json`、
  `~/.claude.json`、`~/.bash_history`、`~/.zsh_history`）。床のどの場所についても、大文字小文字の違い・
  リンク・ファームリンクなど、あらゆる綴り。それらのディレクトリの直下にあるリンクの指す先（同期フォルダへの
  リンクになった `~/.ssh/config` なら、その指す先のファイル）。`$HOME` がアカウントのホームと違うときは、
  両方を守る。Linux の `/etc` を `work_dir` にすること。
- **新たに通す**: `.env.example`、`.env.sample`、`.env.template`、`.env.dist`（ひな形であって秘密ではない）。
- **ホームが分からなければ、音声のパスもどの `work_dir` も拒む**。以前はすべてを通していた。
- `work_dir_denied` の `details` に `reason` が加わる。
- 1 回の検査は約 2 ms（pathguard の実測）。文字起こしの時間に比べて無視できる。

写しを持たないので、判定の修正は pathguard のリリースと、ここでの依存の更新 1 行になる。

## Amendment (2026-09-22, v0.5.1): 実際に使うディレクトリも判定する

`work_dir` だけを検査していたので、`work_dir=~/.config` と `workspace_id=gh` でワークスペースが `~/.config/gh`
（資格情報のディレクトリ）になり、転記がそこへ書かれた。ADR-0002 の写しの頃からの穴で、image-forge の独立
レビューで見つかった。

- `workspace.NewManager(check)` は判定を必須の引数として受け取り、`EnsureUnder` は `<work_dir>/<workspace_id>`
  を作る前・使う前に判定する。サーバーでは `workdir.Resolver.CheckBeneath`（pathguard v0.2.0）を渡す。判定の
  無い Manager はすべてのワークスペースを拒む。配線は `newToolDeps` 1 か所で、テストはそれを使う。
- pathguard v0.2.0 は NUL バイトを含むパスも拒む（C に渡すと NUL で切れ、判定した文字列と開く文字列が違う）。

## Amendment (2026-09-22, v0.5.2): ファイルの有無で答えを変えない

`audio` の絶対パスは `filepath.EvalSymlinks` で解決してから床に掛けていた。そのため、資格情報の位置にある
ファイルは、あれば `path_not_allowed`、無ければ `input_not_found` になり、答えがどの秘密が存在するかを
呼び出し側に教えていた。相対名の `work_dir` 側の候補も「あるか」を先に見てから判定し、「この名前のファイルが
ここにある」という案内も床が拒む場所（`~/.docker/config.json` など）を名指した。ワークスペース内の `.env` は、
あれば `unsupported_format`、無ければ見つからないと答えていた。slack-mcp-extender と chrome-pilot-mcp の
独立レビューで見つかった型で、ここでは HOME を一時ディレクトリにしたテストで実測して確認した（16 組中 14 組で
答えが違った）。コードは voice-scribe と同一で、同じ修正を入れた（voice-scribe ADR-0013 の追補）。

- 読む可能性のある場所はすべて、まず置き場所を決める（`workdir.Where` = pathguard の `Forms` の末尾。リンクは
  すべて辿り、宙に浮いたリンクはその先で。存在するパスなら `EvalSymlinks` と同じ）。床（Local 方針）は、渡された
  綴りと置き場所の両方でその場所に掛け（`refusal`）、存在はその後にだけ問う。絶対パス、相対名の 2 つの候補
  （ワークスペース、`work_dir`）、案内の候補、の全部が同じ 1 つの判定を通る。
- 存在は置き場所に対して問う（`EvalSymlinks(where)`）。綴りから辿り直すと、置き場所が飛ばした構成要素
  （`..` の手前がファイルか存在しないリンク先）を通って、その先に何があるかを答えてしまう。解決先が置き場所と
  違えば（その間に変わった）、そこでもう一度判定する。
- 解決できないパスに専用の分岐を作らない。slack では専用の分岐を直すたびに別の組の答えが割れた。
- `TestExistenceIsNotRevealed` は、同じパスをファイルがある状態と消した状態で呼び、答え全体（code・message・
  details）を比べる。仕掛けたリンクと宙に浮いたリンク、dotfiles へリンクした `~/.config`、`~/.ssh/config` の
  リンク先を含む。`TestPlacementCorners` は `..` でディレクトリ・ファイル・存在しないものを越えるリンクと
  リンクの輪を固定する。7 つの変異（判定の順序を戻す・候補の判定を外す・案内の絞り込みを外す・綴りから
  辿り直す・置き場所を決めない）はすべてアサーションで落ちた。
- 既知の例外は slack と同じ: 資格情報ファイルへのハードリンクを誰かが別の場所に作れば、同一性で拒むのは
  それが存在するときだけになる。作れる者はすでにそのファイルに届いている。

## References

- 組織 ADR-021（ファイル渡し MCP サーバーの work dir 契約）
- ADR-0002（work dir 契約）: 検証の閉じた一覧と、読み取りのブラックリスト —— その実装をここで置き換える
- nlink-jp/pathguard の RFP（`docs/ja/pathguard-rfp.ja.md`）
