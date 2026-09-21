# gem-scribe

Vertex AI の文字起こし専用モデル（`gemini-3.5-transcribe`）を使った音声認識 CLI +
MCP サーバ。

話者ターンと語単位タイムスタンプをモデルが構造化して返すため、LLM に JSON を
書かせる必要も、壊れた JSON を修復する必要もない。出力は
[voice-scribe](https://github.com/nlink-jp/voice-scribe) とエンベロープ互換なので、
下流ツールはクラウド版とローカル版を同じパーサで読める。

## gem-scribe と voice-scribe の使い分け

どちらが上位でもない。録音ごとに選ぶ:

| 優先すること | 選ぶツール | 理由 |
|-------------|-----------|------|
| コスト、音声を外に出さないこと | [voice-scribe](https://github.com/nlink-jp/voice-scribe) | whisper.cpp をローカル実行。API コストゼロ |
| 精度・速度・5人以上の話者 | **gem-scribe** | 話者は最大8名まで分離。モデルの管理も不要。音声1時間あたり約 $0.30 |

## 前提条件

- **Google Cloud プロジェクト** — Vertex AI API が有効であること
- **Application Default Credentials** — `gcloud auth application-default login`

## インストール

```bash
brew install nlink-jp/tap/gem-scribe
```

ソースから:

```bash
git clone https://github.com/nlink-jp/gem-scribe.git
cd gem-scribe
make build          # → dist/gem-scribe
```

## 使い方

```bash
# JSON を標準出力へ
gem-scribe meeting.m4a

# 日本語、字幕として出力
gem-scribe meeting.m4a --lang ja-JP -f srt -o meeting.srt

# すでに Cloud Storage にある音声
gem-scribe gs://my-bucket/interview.flac -f md -o interview.md

# 逐語ではなく整形された文章（話者・時刻は付かない）
gem-scribe talk.wav --smart -f text

# 話者に実名を割り当て、原文の隣に英訳を付ける
gem-scribe meeting.m4a --lang ja-JP --speaker-hint 田中,佐藤 --translate en
```

### フラグ

| フラグ | 既定 | 説明 |
|--------|------|------|
| `-o, --output-file` | 標準出力 | ファイルへ出力。多言語時は言語ごとに分割 |
| `-f, --format` | `json` | `json` / `text` / `md` / `srt` / `vtt` |
| `--lang` | 自動検出 | BCP-47 のヒント。例 `--lang ja-JP` |
| `--diarize` | `true` | 話者ターンにラベルを付ける |
| `--word-timestamps` | `true` | 語単位タイムスタンプ。`srt`/`vtt` には必須 |
| `--smart` | `false` | フィラー除去と軽い整形。上記2つとは併用不可 |
| `-m, --model` | 設定値 | 文字起こしモデル |
| `--location` | `global` | Vertex AI のロケーション |
| `-c, --config` | — | 設定ファイルのパス |
| `--translate` | — | 原文の隣に翻訳を追加。例 `--translate en` |
| `--speaker-hint` | — | 話者名の候補。`spk:N` に割り当てる（複数指定可） |
| `-q, --quiet` | `false` | stderr への進捗表示を止める |

### 出力

```json
{
  "metadata": {
    "source": "meeting.m4a",
    "model": "gemini-3.5-transcribe-preview",
    "duration_seconds": 13.9,
    "languages": ["ja"],
    "diarized": true
  },
  "segments": [
    { "start": 0.1, "end": 4.4, "speaker": "spk:0", "text": { "ja": "…" } }
  ]
}
```

`text` が文字列ではなく「言語コード → テキスト」のマップなのは、原文の隣に翻訳を
置けるようにするため。voice-scribe も同じ形を使う。

### 第2パス

文字起こしモデルは翻訳をせず、話者も `spk:0` としか返さない。`--translate` と
`--speaker-hint` は、**転写テキストに対する**汎用モデルへの2回目の呼び出しで、
文字起こしが完成した後に走る。

この順序が安全性の要で、本ツールが取り除いた脆さを持ち込まない理由でもある。
セグメントは既に確定しており、第2パスはその中のスロットを埋めるだけ。返ってこなかった
行は原文のまま残り、特定できなかった話者はラベルのまま残り、どちらの場合も**どれだけ
残ったかが通知される**。失敗が失うのは付加情報であって、文字起こし本体ではない。

## MCP サーバ

```bash
gem-scribe mcp
```

stdin/stdout で MCP を話し、音声を扱えないモデルのエージェントに録音を読む手段を
与える。ツールは `get_usage` / `transcribe` / `check_job` の3つで、voice-scribe と
同名・同型。エージェントは両者を同じ手順で使える。

文字起こしは非同期で、`transcribe` が返す `job_id` を `check_job` でポーリングする。
呼び出しには必ず `work_dir`（エージェント自身が読み戻せるディレクトリの絶対パス）を
渡し、転記はその配下に書かれる。録音はそこに置いても、読める場所なら絶対パスで
どこを指してもよい（その場で読み、コピーしない。`~/.ssh` のような資格情報の位置は拒否）。
システム領域、ホームディレクトリそのもの、資格情報/エージェント制御の場所、および
**このサーバ自身の設定ディレクトリ（`~/.config/gem-scribe`）**を `work_dir` に
指定した呼び出しは、サブディレクトリを含めて `work_dir_denied` で拒否する。
work_dir は呼び出し側のものであり、サーバ自身のディレクトリはワークスペースではない。
サーバが書くのは `output/` 配下だけで、書き込みパスは workspace の外に出られない。最初に `get_usage` を呼ぶと、
エラー復旧表を含む完全なマニュアルが返る。

クライアントへの登録例:

```json
{ "mcpServers": { "gem-scribe": { "command": "gem-scribe", "args": ["mcp"] } } }
```

## 設定

| 変数 | 既定 | 説明 |
|------|------|------|
| `GEMSCRIBE_PROJECT` | — | GCP プロジェクト ID（必須） |
| `GEMSCRIBE_LOCATION` | `global` | Vertex AI のロケーション |
| `GEMSCRIBE_MODEL` | `gemini-3.5-transcribe-preview` | 文字起こしモデル |
| `GEMSCRIBE_SECOND_PASS_MODEL` | `gemini-3.7-flash` | 翻訳・話者実名用の汎用モデル |
| `GEMSCRIBE_STAGING_BUCKET` | — | inline 上限を超える音声用のバケット |

未設定時は `GOOGLE_CLOUD_PROJECT` / `GOOGLE_CLOUD_LOCATION` にフォールバックする。
設定ファイル `~/.config/gem-scribe/config.toml` も使える
（[config.example.toml](config.example.toml) 参照）。

音声は既定で inline 送信し、20MB を超えるファイルだけが staging バケットを必要と
する。短い録音なら Cloud Storage の設定なしで使い始められる。staging したオブジェクトは
文字起こし後に削除する。

## 知っておくべき制約

- **モデルは `global` エンドポイントでのみ提供される。** リージョナルな `location`
  を指定すると 404 になるが、その本文は location が原因だと言わない。gem-scribe は
  この 404 にヒントを付加する。
- **文字起こしモデルは preview** で、GA 版が存在しない。差し替えられるよう
  モデル名は設定可能にしてある。
- **diarization / word timestamp 有効時は 30 分が上限**と Google は文書化している。
  それより長い音声が通った実測はあるが、保証ではない。
- **3名以上の話者帰属は experimental** と明記されており、音響的に似た声は
  エラーなしで1人として返る。この形の結果には MCP の結果に警告が付く。
- **語彙バイアス（vocabulary biasing）は意図的に公開していない。** 指定すると
  文字起こしが最初の1ターンで打ち切られ、しかも正常終了扱いになる挙動が
  実測で毎回再現したため。

## セキュリティ

- **パス封じ込め** — workspace 内のパスはすべて検証し `os.Root` で強制する。
  エージェントが書き込める workspace に symlink を置かれても、サーバが外部を
  読み書きすることはない。workspace ディレクトリ自体も実パスで検証する:
  `<work_dir>/<workspace_id>` に symlink を仕掛けられていれば、辿らずに拒否する
  — このパスは root の外のコードに渡されるため
- **stdout は MCP トランスポートが占有する** — fd 1 を stderr に向け直すので、
  依存ライブラリの想定外の書き込みはプロトコルを壊さずログに落ちる
- **シークレット非出力** — プロジェクト ID やトークンをログに出さない
- 認証は ADC。gem-scribe 自身が資格情報を扱うことはない

## ライセンス

MIT
