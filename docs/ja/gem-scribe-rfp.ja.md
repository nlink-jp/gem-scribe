# RFP: gem-scribe

> Generated: 2026-08-30
> Status: Draft

## 1. Problem Statement

音声を扱えない LLM エージェントと、シェルパイプラインの双方に、Vertex AI の
**文字起こし専用モデル**（`gemini-3.5-transcribe`）による高精度な文字起こしを提供する。

話者分離・語単位タイムスタンプ・多言語検出をモデル側が構造化して返すため、LLM に
JSON を書かせる必要がない。これが既存の `gem-transcribe` との決定的な違いで、同ツールが
長尺音声で JSON 構造を壊していた破綻モードが原理的に消える。

`voice-scribe`（ローカル whisper.cpp）のクラウド対として位置づける。**どちらが上位でもない**:

| 優先事項 | 選ぶツール | 理由 |
|---------|-----------|------|
| コスト | voice-scribe | API コストゼロ。音声がマシンから出ない |
| 精度・速度・話者数 | **gem-scribe** | 話者数の上限が voice-scribe の 4 名に対し 8 名。モデル管理も不要 |

対象利用者は nlink-jp の運営者（単独）と、MCP 経由で文字起こしをさせたいエージェント。
`gem-transcribe` の後継であり、その CLI 用途も引き継いだうえで `gem-transcribe` は
アーカイブする。

## 2. Functional Specification

### Commands / API Surface

```
gem-scribe <audio-file|gs://...> [flags]    # 文字起こし
gem-scribe mcp                              # stdio MCP サーバ（同一バイナリに同居）
gem-scribe --version
```

主要フラグ（gem-transcribe / voice-scribe と整合させる）:

| フラグ | 既定 | 説明 |
|--------|------|------|
| `-o, --output-file` | stdout | 出力先。多言語時は言語ごとにファイル分割 |
| `-f, --format` | `json` | `json` / `text` / `md` / `srt` / `vtt` |
| `--lang` | 自動検出 | BCP-47 の検出ヒント。複数指定で原文＋翻訳（第2パス） |
| `--diarize` / `--no-diarize` | 有効 | 話者分離 |
| `--word-timestamps` | 有効 | 語単位タイムスタンプ（SRT/VTT の精度に直結） |
| `--smart` | 無効 | SMART モード（フィラー除去・整形）。タイムスタンプ／話者分離とは併用不可 |
| `--speaker-hint` | なし | 話者実名の候補（第2パスで `spk:N` に割当） |
| `-m, --model` / `-c, --config` | — | モデル・設定ファイルの上書き |

MCP ツール（voice-scribe と同名・同型に揃え、エージェントが両者を同じ手順で使えるようにする）:

- `transcribe` — 文字起こしを開始し `job_id` を返す（非同期）
- `check_job` — ジョブの進行状況・結果を取得
- `get_usage` — ツールリファレンスとエラー復旧表

`list_models`（voice-scribe 側に存在）は、ローカルモデルの概念が無いため設けない。

### Input / Output

**入力**: ローカル音声ファイル、または `gs://` URI。ローカルファイルは
**inline 優先、リクエストサイズ上限を超える場合のみ GCS staging へ自動アップロード**し、
処理後に削除する。staging バケット未設定でも短い音声はそのまま使えるため、導入負担が下がる。

**出力エンベロープ**: `gem-transcribe` / `voice-scribe` と**完全互換**を維持する。
後段の `meeting-notes` が 3 ツールすべてを同一パーサで読めることが要件。

```json
{
  "metadata": { "source": "...", "model": "...", "duration_seconds": 0.0, "languages": ["ja"] },
  "segments": [
    { "start": 0.1, "end": 4.4, "speaker": "spk:0", "text": { "ja": "..." } }
  ]
}
```

`Segment.text` が「言語コード → テキスト」のマップである点が、翻訳を第2パスで足す設計と
一致する。API の `parts[].audioTranscription` からの写像は素直:

| API 応答 | Segment |
|---------|---------|
| `speakerLabel` | `speaker` |
| `text` | `text[<lang>]` |
| `words[0].startOffset` / `words[-1].endOffset` | `start` / `end` |

### Configuration

`~/.config/gem-scribe/config.toml`。組織の Vertex AI ツール共通 schema に従う。

```toml
[gcp]
project  = "your-project-id"
location = "global"          # Gemini 3 系は global 専用

[model]
name = "gemini-3.5-transcribe-preview"

[transcribe]
diarization     = true
word_timestamp  = true

[second_pass]
model    = "gemini-3.7-flash"   # 翻訳・話者実名用の汎用モデル（GA）
location = ""                   # 空なら文字起こしと同じ location

[staging]
bucket = ""                  # 空ならサイズ上限超過時にエラー（inline のみで動作）
```

優先順位: CLI フラグ > 環境変数（`GEMSCRIBE_*` > `GOOGLE_CLOUD_*`）> 設定ファイル > 既定値。

### External Dependencies

- Vertex AI（`gemini-3.5-transcribe-preview`、および第2パス用の汎用 Gemini モデル。
  文字起こしモデルは翻訳も話者実名もしないため、モデル指定は 2 つ必要になる）
- Google Cloud Storage（inline 上限を超える音声の staging のみ）
- 認証は ADC（`gcloud auth application-default login`）
- `google.golang.org/genai` v1.70.0 以降（`AudioTranscriptionConfig` 対応版）

## 3. Design Decisions

### なぜ Go か

`voice-scribe` と同じ配布形態（Homebrew tap + Developer ID 署名 + notarize +
`make build-all` によるクロスコンパイル）が、対のツールとして必要だから。`gem-transcribe` は
Python/uv でバイナリ配布を持たなかった。正本は CLI インタフェースなので、後継が
別言語になること自体は利用者に対する破壊的変更にならない。

前提条件（Go SDK の対応）は着手前に実測で確認済み。`google.golang.org/genai` v1.70.0 は
リクエスト側 `GenerateContentConfig.AudioTranscriptionConfig`、応答側
`Part.AudioTranscription`（`SpeakerLabel` / `Words`）を備え、30 行の Go で期待どおりの
話者付きセグメントが取得できた。

### 骨格の移植元

`voice-scribe`。CLI に `mcp` サブコマンドを同居させる構成、非同期ジョブ、出力エンベロープを
引き継ぐ。骨格移植ではドメイン語彙まで書き換える（ローカルモデル管理・GGUF・Metal などの
語が残ると読み手が別物と誤認する）。

### 補完関係

- `voice-scribe` — ローカル対。上表の使い分け軸で選ぶ
- `meeting-notes` — 下流。エンベロープ互換の受益者
- `voice-studio-mcp` — 逆方向（TTS）

### 明示的にスコープ外

- **リアルタイム／ストリーミング文字起こし** — `gemini-3.5-transcribe-live-preview` は
  接続モデルが根本的に異なる。必要になれば別ツールとして起こす
- **議事録の構造化・要約・アクションアイテム抽出** — `meeting-notes` の領域
- **話者プロファイルの永続化**（話者を回をまたいで同定する）
- **`customVocabulary`** — 実測で壊れているため実装しない（§7 参照）
- **Cloud Translation API の Translation LLM** — 翻訳専用モデルとして検討したが不採用。
  理由は §3「翻訳に専用モデルを使わない理由」
- 音声合成（`voice-studio-mcp`）

### 翻訳に専用モデルを使わない理由

Google は翻訳特化モデル **Translation LLM**（`general/translation-llm`）を提供しており、
翻訳品質だけを見れば汎用 Gemini を上回る可能性が高い。専用 ASR モデルを採る本ツールの
思想とも整合する。それでも採らない:

- **Vertex AI のモデルではない。** `publishers/google/models` に翻訳系は 1 件も無く、
  実体は Cloud Translation API v3（`translate.googleapis.com`）。Model Garden の
  コンソール URL に現れるため Vertex のモデルに見えるが、API は別系統
- したがって**第2のサービス・第2のクライアント・第2の IAM ロール**
  （`roles/cloudtranslate.user`）・**リージョナルエンドポイント**（`us-central1`）が
  増える。gem-scribe は他に global エンドポイント 1 つとしか話さない
- **話者実名の割当はできない。** 第2パスの片割れは結局 汎用 Gemini が要る

翻訳品質が実運用で不足した場合に再検討する。その時点でも判断材料は
「サービスを 1 つ増やす価値があるか」であって、モデル単体の品質ではない。

## 4. Development Plan

### Phase 1: Core（CLI + MCP）

- 設定・認証（config.toml + env + フラグ）
- 入力経路: inline 優先 / 上限超過時の GCS staging と後始末
- ASR 呼び出しと `Segment` への写像（`"0.100s"` 形式のオフセット文字列のパースを含む）
- 出力フォーマッタ: JSON / text / Markdown / SRT / VTT
- `mcp` サブコマンド: `transcribe` / `check_job` / `get_usage`
- テスト（API 応答のフィクスチャによるユニットテスト中心）

**Phase 1 の検証項目**（いずれも未実測、設計を左右する）:
- 3 名以上の話者帰属の実精度（ドキュメント上 experimental）
- inline リクエストの実サイズ上限 → GCS へ切り替える閾値の決定
- 文書上の 30 分上限を超えた場合の実挙動（31 分は成功済み。どこで落ちるか）

### Phase 2: Features

- 第2パス（転写テキストのみを汎用 Gemini に渡す）
  - 翻訳（`--lang=en,ja` で原文＋翻訳）
  - 話者実名の割当（`spk:N` → `--speaker-hint` の候補名）
- SMART モードの CLI 公開

### Phase 3: Release

- README.md / README.ja.md / AGENTS.md / CLAUDE.md / ADR
- 署名・notarize・`make verify-release`・Homebrew tap
- util-series への submodule 登録、カタログ 3 面同期
- **`gem-transcribe` のアーカイブ**（後継として README に gem-scribe を明記してから）
- knowledge への還元

**独立レビュー可能な単位**: Phase 1 は CLI と MCP でレビューを分けられる（MCP は CLI の
コア関数を呼ぶだけの薄い層にする）。Phase 2 の第2パスは Phase 1 に一切影響しない
（スキップ可能な後段）ため、単独でレビューできる。

## 5. Required API Scopes / Permissions

| 対象 | 権限 | 用途 |
|------|------|------|
| Vertex AI | `roles/aiplatform.user` | `generateContent` の呼び出し |
| GCS staging バケット | `roles/storage.objectAdmin` | 音声のアップロードと処理後の削除 |

認証は Application Default Credentials（`gcloud auth application-default login`）。
GCS 権限は inline 上限を超える音声を扱う場合にのみ必要。

## 6. Series Placement

Series: **util-series**

Reason: パイプライン親和の変換系 CLI であり、対になる `voice-scribe`、前身の
`gem-transcribe`、下流の `meeting-notes`、逆方向の `voice-studio-mcp` がすべて
util-series にある。

## 7. External Platform Constraints

2026-08-30 に実測で確認した制約（実測はすべて `gemini-3.5-transcribe-preview`、
global エンドポイント）。

| 制約 | 内容 | 設計への影響 |
|------|------|-------------|
| **preview のみ** | GA 版の文字起こし専用モデルが存在しない | 廃止スケジュールに晒される。モデル名を config で差し替え可能にし、README に明記する |
| **global 専用** | `us-central1` / `asia-northeast1` は 404 | `location` 既定を `global` にする。リージョナル指定時の 404 にヒントを付す |
| **30 分上限（文書上）** | diarization / word timestamp 有効時。31 分は実際には成功した | 文書上の契約を上限として扱い、超過時は警告する。成功に依存しない |
| **`customVocabulary` が壊れている** | 指定すると転写が最初の 1 ターンで打ち切られ、しかも `finishReason: STOP`（エラーにならず静かに欠落）。2 回再現 | 実装しない。Phase 1 の検証で GA 時に再評価 |
| **翻訳しない** | 日本語音声に `languageCodes: ["en-US"]` を与えても日本語のまま。`languageCodes` は検出ヒント | 翻訳は第2パスに分離（Phase 2） |
| **話者は `spk:N` のみ** | 実名の推定は ASR の守備範囲外 | 実名化は第2パスに分離（Phase 2） |
| **3 名以上は experimental** | 上限 8 名だが、3 名以上の帰属はドキュメント上 experimental | Phase 1 で実測する。実用範囲を README に書く |
| **音響的に似た話者は分離されない** | macOS TTS の 2 音声は同一話者と判定された。ピッチを明確に変えると正しく分離 | 期待値として README に明記。ユーザーの誤解を招きやすい |
| **リクエストサイズ上限** | 31 分の mp3（約 7.5MB、base64 約 10MB）は inline で成功 | 閾値を Phase 1 で実測し、超過分を GCS に回す |
| コスト | 31 分で約 $0.16 ≒ **$0.30/時間** | voice-scribe との使い分け軸そのもの |

---

## Discussion Log

**発端（2026-08-30）**: `gem-transcribe` を新しい文字起こし専用モデルへ移行する相談として
始まった。動機は 2 つ — モデル世代の更新と、**長尺音声で JSON 構造エラーが出て停止する
安定性の不安**。

**調査で判明した中心的事実**: `gemini-3.5-transcribe-preview` は Vertex AI では通常の
`:generateContent` で動き、`parts[].audioTranscription`（`text` / `speakerLabel` / `words`）
という専用の応答パートを返す。**話者ターンごとに part が分かれ、構造は API が保証する**。
つまり LLM に JSON を書かせる層そのものが不要になり、報告された破綻モードが原理的に消える。
31 分の音声でも 209 parts・話者 2 名を通して一貫し、タイムスタンプは末尾まで完走した。

**当初の方針（採用せず）**: `gem-transcribe` の LLM 層だけを入れ替える改造案を提示した。
データモデルとフォーマッタが残るため妥当ではあったが、ユーザーが
**「voice-scribe の gemini 版の新規作成」への転換**を選択した。既存の設計負債
（プロンプト層・スキーマ層・サルベージ層、翻訳前提の RFP）を引き継がず、実証済みの
voice-scribe 骨格から起こすほうが速いという判断。

**gem-transcribe の処遇**: 併存させるとクラウド文字起こしが 2 本並ぶため、
**後継としてアーカイブ**を選択（`csv-editor`→`grid-edit`、`quick-translate`→
`instant-translate` と同じパターン）。したがって gem-scribe は CLI も同居させ、
機能を落とさないことが要件になった。

**voice-scribe との関係**: 当初は「gem-scribe を第一選択、voice-scribe を退避先」と
位置づける案を出したが、ユーザーが**対等な二択**として整理した — コスト優先なら
voice-scribe、精度・速度・話者数（voice-scribe は 4 名上限）が要るなら gem-scribe。
README と `mcp-tactics` スキルの記述もこの軸に揃える。

**ASR が持たない機能**: 実測により、このモデルは翻訳せず、話者実名も出さないことが
確定した（`languageCodes: ["en-US"]` を日本語音声に与えても日本語のまま）。
`gem-transcribe` の `--lang=en,ja` と `--speaker-hint` を落とすかが論点になり、
**転写テキストのみを入力とする第2パス**として引き継ぐことを選択。入力が数時間の音声から
有界なテキストに変わるため、LLM を使う部分も現行より壊れにくくなり、不要なら丸ごと
スキップできる。

**言語選択**: Go。`voice-scribe` と同じ配布形態が対のツールとして必要なため。着手前に
Go SDK が `AudioTranscriptionConfig` に対応していることを実機で確認し、30 行の Go で
話者付きセグメントが取れるところまで実証した（前提条件が崩れないことを Phase 1 開始前に
確定させる意図）。

**入力経路**: 常時 GCS（`gem-transcribe` 踏襲）ではなく **inline 優先＋上限超過時のみ GCS**
を選択。staging バケット未設定でも短い音声が使えるほうが導入負担が低いため。

**MCP のフェーズ**: `voice-scribe` は MCP を Phase 2b に置いたが、gem-scribe は
「voice-scribe の gemini 版」として最初から使える形が要るため **Phase 1 に CLI と MCP の
両方**を含める。

**実装しないと決めたもの**: `customVocabulary`。語彙バイアスは魅力的だが、実測で
転写が最初の 1 ターンに打ち切られ、しかもエラーではなく `finishReason: STOP` で
静かに欠落する（2 回再現）。preview 品質の地雷として記録し、GA 時に再評価する。
