[English](README.md) | [中文](README_zh.md) | **日本語** | [한국어](README_ko.md) | [Tiếng Việt](README_vi.md) | [Français](README_fr.md) | [Русский](README_ru.md)

# sage-wiki

[Andrej Karpathyのアイデア](https://x.com/karpathy/status/2039805659525644595)に基づく、LLMコンパイル型パーソナルナレッジベースの実装です。[Sage Framework](https://github.com/xoai/sage)を使用して開発されました。

sage-wikiを構築して得た教訓は[こちら](https://x.com/xoai/status/2040936964799795503)。

論文、記事、ノートを投入するだけ。sage-wikiがそれらを構造化され相互リンクされたWikiにコンパイルします — コンセプトの抽出、クロスリファレンスの発見、すべてが検索可能になります。

- **ソースを入れれば、Wikiが出来上がる。** ドキュメントをフォルダに追加するだけ。LLMが読み取り、要約し、コンセプトを抽出し、相互接続された記事を作成します。
- **100K以上のドキュメントに対応。** ティアードコンパイルがすべてを高速にインデックスし、重要なものだけをコンパイルします。100Kのボールトが数ヶ月ではなく数時間で検索可能になります。
- **蓄積される知識。** 新しいソースが追加されるたびに既存の記事が充実します。Wikiは成長するほど賢くなります。
- **既存のツールと連携。** ObsidianでネイティブにOpenできます。MCPを介して任意のLLMエージェントに接続できます。単一バイナリで動作 — APIキーまたは既存のLLMサブスクリプションで利用可能。
- **Wikiに質問する。** チャンクレベルのインデックス、LLMクエリ拡張、リランキングによる強化検索。自然言語で質問し、引用付きの回答を取得できます。
- **オンデマンドコンパイル。** エージェントがMCPを介して特定のトピックのコンパイルをトリガーできます。検索結果が未コンパイルのソースが利用可能であることを通知します。

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_外側の境界上のドットはナレッジベース内のすべてのドキュメントの要約を表し、内側の円のドットはナレッジベースから抽出されたコンセプトを表します。リンクはそれらのコンセプトがどのように相互接続されているかを示しています。_

## インストール

```bash
# CLIのみ（Web UIなし）
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# Web UI付き（フロントエンドアセットのビルドにNode.jsが必要）
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## 対応ソースフォーマット

| フォーマット | 拡張子 | 抽出される内容 |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | フロントマターを分離してパースした本文テキスト |
| PDF         | `.pdf`                                  | 純粋なGoによるフルテキスト抽出 |
| Word        | `.docx`                                 | XMLからのドキュメントテキスト |
| Excel       | `.xlsx`                                 | セル値とシートデータ |
| PowerPoint  | `.pptx`                                 | スライドのテキストコンテンツ |
| CSV         | `.csv`                                  | ヘッダー＋行（最大1000行） |
| EPUB        | `.epub`                                 | XHTMLからの章テキスト |
| Email       | `.eml`                                  | ヘッダー（from/to/subject/date）＋本文 |
| プレーンテキスト | `.txt`, `.log`                     | 生のコンテンツ |
| トランスクリプト | `.vtt`, `.srt`                     | 生のコンテンツ |
| 画像        | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | ビジョンLLMによる説明（キャプション、コンテンツ、表示テキスト） |
| コード      | `.go`, `.py`, `.js`, `.ts`, `.rs` など  | ソースコード |

ファイルをソースフォルダにドロップするだけ — sage-wikiがフォーマットを自動検出します。画像にはビジョン対応のLLM（Gemini、Claude、GPT-4o）が必要です。

ここに記載されていないフォーマットが必要ですか？sage-wikiは**外部パーサー**をサポートしています — stdinを読み取りプレーンテキストをstdoutに書き出す任意の言語のスクリプトです。詳細は[外部パーサー](#外部パーサー)をご覧ください。

## クイックスタート

![コンパイラパイプライン](sage-wiki-compiler-pipeline.png)

### 新規プロジェクト（グリーンフィールド）

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# raw/にソースを追加
cp ~/papers/*.pdf raw/papers/
cp ~/articles/*.md raw/articles/
# config.yamlを編集してAPIキーを追加し、LLMを選択
# 初回コンパイル
sage-wiki compile
# 検索
sage-wiki search "attention mechanism"
# 質問する
sage-wiki query "How does flash attention optimize memory?"
# インタラクティブターミナルダッシュボード
sage-wiki tui
# ブラウザで閲覧（-tags webuiビルドが必要）
sage-wiki serve --ui
# フォルダ監視
sage-wiki compile --watch
```

### ボールトオーバーレイ（既存のObsidianボールト）

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# config.yamlを編集してソース/除外フォルダの設定、APIキーの追加、LLMの選択
# 初回コンパイル
sage-wiki compile
# ボールトの監視
sage-wiki compile --watch
```

### Docker

```bash
# GitHub Container Registryからプル
docker pull ghcr.io/xoai/sage-wiki:latest

# またはDocker Hubからプル
docker pull xoai/sage-wiki:latest

# Wikiディレクトリをマウントして実行
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# またはソースからビルド
docker build -t sage-wiki .
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... sage-wiki
```

利用可能なタグ：`:latest`（mainブランチ）、`:v1.0.0`（リリース）、`:sha-abc1234`（特定のコミット）。マルチアーキテクチャ：`linux/amd64`と`linux/arm64`。

Docker Compose、Syncthing同期、リバースプロキシ、LLMプロバイダーのセットアップについては、[セルフホスティングガイド](docs/guides/self-hosted-server.md)を参照してください。

## コマンド

| コマンド | 説明 |
| --------------------------------------------------------------------------------------- | ------------------------------------------------ |
| `sage-wiki init [--vault] [--skill <agent>]`                                            | プロジェクトの初期化（新規またはボールトオーバーレイ） |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | ソースをWiki記事にコンパイル |
| `sage-wiki serve [--transport stdio\|sse]`                                              | LLMエージェント用MCPサーバーを起動 |
| `sage-wiki serve --ui [--port 3333]`                                                    | Web UIを起動（`-tags webui`ビルドが必要） |
| `sage-wiki lint [--fix] [--pass name]`                                                  | リンティングパスを実行 |
| `sage-wiki search "query" [--tags ...]`                                                 | ハイブリッド検索（BM25 + ベクトル） |
| `sage-wiki query "question"`                                                            | Wikiに対するQ&A |
| `sage-wiki tui`                                                                         | インタラクティブターミナルダッシュボードを起動 |
| `sage-wiki ingest <url\|path>`                                                          | ソースを追加 |
| `sage-wiki status`                                                                      | Wikiの統計とヘルス情報 |
| `sage-wiki provenance <source-or-concept>`                                              | ソース↔記事の来歴マッピングを表示 |
| `sage-wiki doctor`                                                                      | 設定と接続性を検証 |
| `sage-wiki diff`                                                                        | マニフェストに対する保留中のソース変更を表示 |
| `sage-wiki list`                                                                        | Wikiエンティティ、コンセプト、またはソースを一覧表示 |
| `sage-wiki write <summary\|article>`                                                    | 要約または記事を作成 |
| `sage-wiki ontology <query\|list\|add>`                                                 | オントロジーグラフの照会、一覧表示、管理 |
| `sage-wiki hub <add\|remove\|search\|status\|list>`                                    | マルチプロジェクトハブコマンド |
| `sage-wiki learn "text"`                                                                | 学習エントリを保存 |
| `sage-wiki capture "text"`                                                              | テキストから知識をキャプチャ |
| `sage-wiki add-source <path>`                                                           | マニフェストにソースファイルを登録 |
| `sage-wiki skill <refresh\|preview> [--target <agent>]`                                 | エージェントスキルファイルの生成またはリフレッシュ |
| `sage-wiki pack install <name\|url>`                                                    | コントリビューションパックのインストール |
| `sage-wiki pack apply <name> [--mode merge\|replace]`                                   | インストール済みパックをプロジェクトに適用 |
| `sage-wiki pack remove <name>`                                                          | プロジェクトからパックを削除 |
| `sage-wiki pack list`                                                                   | 適用済み・キャッシュ済み・バンドル済みパック一覧 |
| `sage-wiki pack search <query>`                                                         | パックレジストリを検索 |
| `sage-wiki pack update [name]`                                                          | インストール済みパックを最新版に更新 |
| `sage-wiki pack info <name>`                                                            | パックの詳細を表示 |
| `sage-wiki pack create <name>`                                                          | 新しいパックディレクトリのスキャフォールド |
| `sage-wiki pack validate [path]`                                                        | パックのスキーマとファイルを検証 |
| `sage-wiki auth login --provider <name>`                                                | サブスクリプション認証用OAuthログイン |
| `sage-wiki auth import --provider <name>`                                               | 既存のCLIツールから認証情報をインポート |
| `sage-wiki auth status`                                                                 | 保存されたサブスクリプション認証情報を表示 |
| `sage-wiki auth logout --provider <name>`                                               | 保存された認証情報を削除 |
| `sage-wiki verify [--all] [--since 7d] [--limit 20]`                                   | 保留中の出力のグラウンディング検証 |
| `sage-wiki outputs list [--state pending\|confirmed\|conflict\|stale]`                  | 信頼状態別に出力を一覧表示 |
| `sage-wiki outputs promote <id>`                                                        | 出力を手動で確認済みに昇格 |
| `sage-wiki outputs reject <id>`                                                         | 保留中の出力を拒否して削除 |
| `sage-wiki outputs resolve <id>`                                                        | 回答を昇格し、競合する出力を拒否 |
| `sage-wiki outputs clean [--older-than 90d]`                                            | 古い/失効した保留中の出力を削除 |
| `sage-wiki outputs migrate`                                                             | 既存の出力を信頼システムに移行 |
| `sage-wiki scribe <session-file>`                                                       | セッショントランスクリプトからエンティティを抽出 |

## TUI

```bash
sage-wiki tui
```

4つのタブを持つフル機能のターミナルダッシュボード：

- **[F1] ブラウズ** — セクション別に記事をナビゲート（コンセプト、要約、出力）。矢印キーで選択、Enterでglamourレンダリングされたmarkdownを読む、Escで戻る。
- **[F2] 検索** — 分割ペインプレビュー付きファジー検索。入力してフィルタリング、ハイブリッドスコアで結果をランク付け、Enterで`$EDITOR`で開く。
- **[F3] Q&A** — ストリーミング対話型Q&A。質問してLLM合成された回答をソース引用付きで取得。Ctrl+Sで回答をoutputs/に保存。
- **[F4] コンパイル** — ライブコンパイルダッシュボード。ソースディレクトリの変更を監視し自動再コンパイル。プレビュー付きでコンパイル済みファイルをブラウズ。

タブ切り替え：任意のタブから`F1`-`F4`、ブラウズ/コンパイルでは`1`-`4`、`Esc`でブラウズに戻る。`Ctrl+C`で終了。

## Web UI

![Sage-Wiki アーキテクチャ](sage-wiki-webui.png)

sage-wikiには、Wikiの閲覧と探索のためのオプションのブラウザベースビューアが含まれています。

```bash
sage-wiki serve --ui
# http://127.0.0.1:3333 で開きます
```

機能：

- レンダリングされたmarkdown、シンタックスハイライト、クリック可能な`[[wikilinks]]`を備えた**記事ブラウザ**
- ランク付けされた結果とスニペットによる**ハイブリッド検索**
- コンセプトとその接続のインタラクティブなフォースディレクテッドビジュアライゼーションによる**ナレッジグラフ**
- 質問してLLM合成された回答をソース引用付きで取得する**ストリーミングQ&A**
- スクロールスパイ付きの**目次**、またはグラフビューに切り替え
- システム設定検出付き**ダーク/ライトモード**切り替え
- **壊れたリンク検出** — 欠落した記事へのリンクがグレーで表示

Web UIはPreact + Tailwind CSSで構築され、`go:embed`でGoバイナリに埋め込まれます。バイナリサイズに約1.2 MB（gzip圧縮）が追加されます。Web UIなしでビルドするには、`-tags webui`フラグを省略してください — バイナリはすべてのCLIおよびMCP操作で引き続き動作します。

オプション：

- `--port 3333` — ポートを変更（デフォルト3333）
- `--bind 0.0.0.0` — ネットワークに公開（デフォルトはlocalhostのみ、認証なし）

## 設定

`config.yaml`は`sage-wiki init`で作成されます。完全な例：

```yaml
version: 1
project: my-research
description: "Personal research wiki"

# 監視・コンパイル対象のソースフォルダ
sources:
  - path: raw # またはClippings/、Papers/などのボールトフォルダ
    type: auto # ファイル拡張子から自動検出
    watch: true

output: wiki # コンパイル済み出力ディレクトリ（ボールトオーバーレイの場合は_wiki）

# 読み取り・API送信を行わないフォルダ（ボールトオーバーレイモード）
# ignore:
#   - Daily Notes
#   - Personal

# LLMプロバイダー
# 対応: anthropic, openai, gemini, ollama, openai-compatible, qwen
# OpenRouterまたはその他のOpenAI互換プロバイダーの場合:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# Alibaba Cloud DashScope Qwenの場合:
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY} # 環境変数の展開に対応
  # auth: subscription          # api_keyの代わりにサブスクリプション認証を使用
                                # 要件: sage-wiki auth login --provider <name>
                                # 対応プロバイダー: openai, anthropic, gemini
  # base_url:                   # カスタムエンドポイント（OpenRouter、Azureなど）
  # rate_limit: 60              # 1分あたりのリクエスト数
  # extra_params:               # リクエストボディにマージされるプロバイダー固有のパラメータ
  #   enable_thinking: false    # 例: Qwen思考モードを無効化
  #   reasoning_effort: low     # 例: DeepSeek推論制御

# タスク別モデル — 大量処理には安価なモデル、執筆には高品質モデルを使用
models:
  summarize: gemini-3-flash-preview
  extract: gemini-3-flash-preview
  write: gemini-3-flash-preview
  lint: gemini-3-flash-preview
  query: gemini-3-flash-preview

# エンベディングプロバイダー（オプション — apiプロバイダーから自動検出）
# エンベディングに別のプロバイダーを使用する場合にオーバーライド
embed:
  provider: auto # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # エンベディング用の別キー
  # base_url:                   # 別のエンドポイント
  # rate_limit: 0              # エンベディングRPM上限（0 = 無制限; Gemini Tier 1の場合は1200に設定）

# マルチプロバイダーに関する注記:
# apiセクションは、すべてのコンパイラおよびクエリタスク（summarize、extract、write、
# lint、query）に使用されるプライマリLLMプロバイダーを設定します。embedセクションは
# エンベディングに完全に別のプロバイダーを使用できます — 独自のapi_key、base_url、
# rate_limitを持ちます。これにより、コストや品質のためにプロバイダーを混在させることができます:
#
#   api:
#     provider: anthropic                    # 生成にClaudeを使用
#     api_key: ${ANTHROPIC_API_KEY}
#   models:
#     summarize: claude-haiku-4-5-20251001   # 大量処理に安価なモデル
#     write: claude-sonnet-4-20250514        # 記事に高品質モデル
#     query: claude-sonnet-4-20250514
#   embed:
#     provider: openai                       # エンベディングにOpenAIを使用
#     model: text-embedding-3-small
#     api_key: ${OPENAI_API_KEY}
#
# サブスクリプション認証では、複数のプロバイダーで認証できます:
#   sage-wiki auth login --provider anthropic
#   sage-wiki auth import --provider gemini
# その後、生成にAnthropicを使用し、エンベディングにGeminiを使用できます。

compiler:
  max_parallel: 20 # 同時LLM呼び出し数（適応的バックプレッシャー付き）
  debounce_seconds: 2 # 監視モードのデバウンス
  summary_max_tokens: 2000
  article_max_tokens: 4000
  # extract_batch_size: 20     # コンセプト抽出呼び出しあたりの要約数（大規模コーパスでのJSON切り捨てを避けるには縮小）
  # extract_max_tokens: 8192   # コンセプト抽出の最大出力トークン数（抽出が切り捨てられる場合は16384に増加）
  auto_commit: true # コンパイル後にgitコミット
  auto_lint: true # コンパイル後にlintを実行
  mode: auto # standard、batch、またはauto（auto = 10以上のソースでbatch）
  # estimate_before: false    # コンパイル前にコスト見積もりを表示
  # prompt_cache: true        # プロンプトキャッシュを有効化（デフォルト: true）
  # batch_threshold: 10       # 自動バッチモードの最小ソース数
  # token_price_per_million: 0  # 価格オーバーライド（0 = 組み込み価格を使用）
  # timezone: Asia/Shanghai   # ユーザー向けタイムスタンプ用IANAタイムゾーン（デフォルト: UTC）
  # article_fields:           # LLMレスポンスから抽出されるカスタムフロントマターフィールド
  #   - language
  #   - domain

  # ティアードコンパイル — 高速インデックス、重要なものだけコンパイル
  default_tier: 3 # 0=インデックスのみ, 1=インデックス+エンベッド, 3=フルコンパイル
  # tier_defaults:             # 拡張子別ティアオーバーライド
  #   json: 0                  # 構造化データ — インデックスのみ
  #   yaml: 0
  #   lock: 0
  #   md: 1                    # 散文 — インデックス + エンベッド
  #   go: 1                    # コード — インデックス + エンベッド + パース
  # auto_promote: true         # クエリヒットに基づいてティア3に自動昇格
  # auto_demote: true          # 古い記事を自動降格
  # split_threshold: 15000     # 文字数 — 大きなドキュメントを分割して高速化
  # dedup_threshold: 0.85      # コンセプト重複排除のコサイン類似度
  # backpressure: true         # レート制限に対する適応的並行性

search:
  hybrid_weight_bm25: 0.7 # BM25対ベクトルの重み
  hybrid_weight_vector: 0.3
  default_limit: 10
  # query_expansion: true     # Q&A用LLMクエリ拡張（デフォルト: true）
  # rerank: true              # Q&A用LLMリランキング（デフォルト: true）
  # chunk_size: 800           # インデックス用のチャンクあたりトークン数（100-5000）
  # graph_expansion: true     # Q&A用グラフベースコンテキスト拡張（デフォルト: true）
  # graph_max_expand: 10      # グラフ拡張で追加される最大記事数
  # graph_depth: 2            # オントロジー走査深度（1-5）
  # context_max_tokens: 8000  # クエリコンテキストのトークン予算
  # weight_direct_link: 3.0   # グラフシグナル: コンセプト間のオントロジー関係
  # weight_source_overlap: 4.0 # グラフシグナル: 共有ソースドキュメント
  # weight_common_neighbor: 1.5 # グラフシグナル: Adamic-Adar共通近傍
  # weight_type_affinity: 1.0  # グラフシグナル: エンティティタイプペアボーナス

serve:
  transport: stdio # stdioまたはsse
  port: 3333 # SSEモードのみ

# 出力信頼性 — クエリ出力を検証まで隔離
# trust:
#   include_outputs: false       # "false"（デフォルト）、"verified"、"true"（レガシー）
#   consensus_threshold: 3       # 自動昇格に必要な確認数
#   grounding_threshold: 0.8     # 最小グラウンディングスコア（0.0-1.0）
#   similarity_threshold: 0.85   # 質問マッチングの閾値
#   auto_promote: true           # すべての閾値を満たしたら自動昇格

# オントロジータイプ（オプション）
# 組み込みタイプに追加のシノニムを拡張するか、カスタムタイプを追加します。
# ontology:
#   relation_types:
#     - name: implements           # 組み込みにシノニムを追加して拡張
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates            # カスタム関係タイプを追加
#       synonyms: ["regulates", "regulated by", "調控"]
#   entity_types:
#     - name: decision
#       description: "根拠付きの記録された決定"
```

### マルチプロバイダーセットアップ

sage-wikiでは、異なるタスクに異なるLLMプロバイダーを使用できます。`api`セクションは生成（summarize、extract、write、lint、query）用のプライマリプロバイダーを設定し、`embed`はエンベディング用に完全に別のプロバイダーを使用できます — それぞれ独自の認証情報とレート制限を持ちます。

**ユースケース：**
- **コスト最適化** — 大量の要約に安価なモデル、記事執筆に高品質モデル
- **ベストオブブリード** — 生成にClaude、エンベディングにOpenAI、ローカル検索にOllama
- **サブスクリプションの組み合わせ** — 生成にChatGPTサブスクリプション、エンベディングにGeminiサブスクリプションを使用

**例：Claude（生成）+ OpenAI（エンベディング）**

```yaml
api:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}

models:
  summarize: claude-haiku-4-5-20251001    # 大量処理に安価
  extract: claude-haiku-4-5-20251001
  write: claude-sonnet-4-20250514         # 記事に高品質
  lint: claude-haiku-4-5-20251001
  query: claude-sonnet-4-20250514

embed:
  provider: openai
  model: text-embedding-3-small
  api_key: ${OPENAI_API_KEY}
```

**例：2つのプロバイダーによるサブスクリプション認証**

```bash
sage-wiki auth login --provider anthropic
sage-wiki auth import --provider gemini
```

```yaml
api:
  provider: anthropic
  auth: subscription

embed:
  provider: gemini
  # api_keyは不要 — インポートされたGeminiサブスクリプション認証情報を使用
```

`models`セクションはプライマリプロバイダー内でタスクごとに使用するモデルを制御します。モデルによってコスト/品質のトレードオフが大きく異なります — 要約のような大量処理パスには小さいモデル（haiku、flash、mini）を使用し、記事執筆やQ&Aには大きいモデル（sonnet、pro）を使用してください。

### 設定可能な関係

オントロジーには8つの組み込み関係タイプがあります：`implements`、`extends`、`optimizes`、`contradicts`、`cites`、`prerequisite_of`、`trades_off`、`derived_from`。各タイプには自動抽出に使用されるデフォルトのキーワードシノニムがあります。

`config.yaml`の`ontology.relations`で関係をカスタマイズできます：

- **組み込みタイプの拡張** — 既存のタイプにシノニム（例：多言語キーワード）を追加します。デフォルトのシノニムは保持され、追加分が末尾に加えられます。
- **カスタムタイプの追加** — 新しい関係名とキーワードシノニムを定義します。関係名は小文字の`[a-z][a-z0-9_]*`である必要があります。

関係はブロックレベルのキーワード近接を使用して抽出されます — キーワードが`[[wikilink]]`と同じ段落またはヘディングブロック内で共起する必要があります。これにより、段落をまたいだマッチングからの誤ったエッジを防ぎます。

関係が接続するエンティティタイプを制限することもできます：

```yaml
ontology:
  relation_types:
    - name: curated_by
      synonyms: ["curated by", "organized by"]
      valid_sources: [exhibition, program]
      valid_targets: [artist]
```

`valid_sources`/`valid_targets`が設定されている場合、ソース/ターゲットのエンティティタイプが一致する場合のみエッジが作成されます。空 = すべてのタイプを許可（デフォルト）。

設定なし = 現在の動作と同一です。既存のデータベースは初回オープン時に自動的に移行されます。ドメイン固有の例、タイプ制限付き関係、抽出の仕組みについては、[完全ガイド](docs/guides/configurable-relations.md)を参照してください。

## コスト最適化

sage-wikiはトークン使用量を追跡し、すべてのコンパイルのコストを見積もります。コストを削減する3つの戦略：

**プロンプトキャッシュ**（デフォルト：有効） — コンパイルパス内のLLM呼び出し間でシステムプロンプトを再利用します。AnthropicとGeminiは明示的にキャッシュし、OpenAIは自動的にキャッシュします。入力トークンを50-90%節約できます。

**Batch API** — すべてのソースを単一の非同期バッチとして送信し、50%のコスト削減を実現します。AnthropicとOpenAIで利用可能です。

```bash
sage-wiki compile --batch       # バッチを送信、チェックポイント、終了
sage-wiki compile               # ステータスをポーリング、完了時に取得
```

**コスト見積もり** — コミットする前にコストをプレビュー：

```bash
sage-wiki compile --estimate    # コスト内訳を表示して終了
```

または、`config.yaml`で`compiler.estimate_before: true`を設定して毎回プロンプトを表示します。

**自動モード** — `compiler.mode: auto`と`compiler.batch_threshold: 10`を設定すると、10以上のソースのコンパイル時に自動的にバッチを使用します。

## サブスクリプション認証

APIキーの代わりに既存のLLMサブスクリプションを使用できます。ChatGPT Plus/Pro、Claude Pro/Max、GitHub Copilot、Google Geminiに対応しています。

```bash
# ブラウザ経由でログイン（OpenAIまたはAnthropic）
sage-wiki auth login --provider openai

# または既存のCLIツールからインポート
sage-wiki auth import --provider claude
sage-wiki auth import --provider copilot
sage-wiki auth import --provider gemini
```

次に、`config.yaml`で`api.auth: subscription`を設定します：

```yaml
api:
  provider: openai
  auth: subscription
```

すべてのコマンドがサブスクリプション認証情報を使用します。トークンは自動的にリフレッシュされます。トークンが期限切れでリフレッシュできない場合、sage-wikiは警告付きで`api_key`にフォールバックします。

**制限事項：** サブスクリプション認証ではバッチモードは利用できません（自動的に無効化）。一部のモデルはサブスクリプショントークンではアクセスできない場合があります。詳細は[サブスクリプション認証ガイド](docs/guides/subscription-auth.md)を参照してください。

## 出力信頼性

sage-wikiが質問に回答する場合、その回答はLLMが生成した主張であり、検証された事実ではありません。セーフガードなしでは、誤った回答がWikiにインデックスされ、将来のクエリを汚染します。出力信頼システムは新しい出力を隔離し、検索可能なコーパスに入る前に検証を要求します。

```yaml
# config.yaml
trust:
  include_outputs: verified  # "false"（すべて除外）、"verified"（確認済みのみ）、"true"（レガシー）
  consensus_threshold: 3     # 自動昇格に必要な確認数
  grounding_threshold: 0.8   # 最小グラウンディングスコア
  similarity_threshold: 0.85 # 質問マッチングのコサイン類似度
  auto_promote: true          # すべての閾値を満たしたら自動昇格
```

**仕組み：**

1. **クエリ** — sage-wikiがあなたの質問に回答します。出力は`wiki/under_review/`に保留中として書き込まれます。
2. **コンセンサス** — 同じ質問が再度され、異なるソースチャンクから同じ回答が生成されると、確認が蓄積されます。独立性はチャンクセット間のJaccard距離でスコアリングされます。
3. **グラウンディング** — `sage-wiki verify`を実行して、LLMエンタイルメントによりソースパッセージに対する主張を検証します。
4. **昇格** — コンセンサスとグラウンディングの両方の閾値が満たされると、出力は`wiki/outputs/`に昇格され、検索にインデックスされます。

```bash
# 保留中の出力を確認
sage-wiki outputs list

# グラウンディング検証を実行
sage-wiki verify --all

# 信頼できる出力を手動で昇格
sage-wiki outputs promote 2026-05-09-what-is-attention.md

# 競合を解決（1つを昇格、他を拒否）
sage-wiki outputs resolve 2026-05-09-what-is-attention.md

# 古い保留中の出力をクリーンアップ
sage-wiki outputs clean --older-than 90d

# 既存の出力を信頼システムに移行
sage-wiki outputs migrate
```

`sage-wiki compile`中のソース変更は、引用されたソースが変更された場合に確認済みの出力を自動的に降格させます。完全なアーキテクチャ、設定リファレンス、トラブルシューティングについては、[出力信頼ガイド](docs/guides/output-trust.md)を参照してください。

## 大規模ボールトへのスケーリング

sage-wikiは**ティアードコンパイル**を使用して、10K-100K以上のドキュメントのボールトを処理します。すべてをフルLLMパイプラインでコンパイルする代わりに、ファイルタイプと使用状況に基づいてソースがティアにルーティングされます：

| ティア | 処理内容 | コスト | ドキュメントあたりの時間 |
|------|-------------|------|-------------|
| **0** — インデックスのみ | FTS5全文検索 | 無料 | 約5ms |
| **1** — インデックス + エンベッド | FTS5 + ベクトルエンベディング | 約$0.00002 | 約200ms |
| **2** — コードパース | 正規表現パーサーによる構造的要約（LLMなし） | 無料 | 約10ms |
| **3** — フルコンパイル | 要約 + コンセプト抽出 + 記事執筆 | 約$0.05-0.15 | 約5-8分 |

デフォルト（`default_tier: 3`）では、すべてのソースがフルLLMパイプラインを通ります — ティアードコンパイル以前と同じ動作です。大規模ボールト（10K以上）では、`default_tier: 1`に設定して約5.5時間ですべてをインデックスし、その後オンデマンドでコンパイルします — エージェントがトピックを照会すると、検索が未コンパイルのソースを通知し、`wiki_compile_topic`がそのクラスターだけをコンパイルします（20ソースで約2分）。

**主な機能：**
- **ファイルタイプデフォルト** — JSON、YAML、lockファイルは自動的にティア0にスキップします。`tier_defaults`で拡張子ごとに設定可能です。
- **自動昇格** — 3回以上の検索ヒット後、またはトピッククラスターが5以上のソースに達した場合、ソースはティア3に昇格します。
- **自動降格** — 古い記事（クエリなしで90日経過）はティア1に降格し、次回アクセス時に再コンパイルされます。
- **適応的バックプレッシャー** — 並行数がプロバイダーのレート制限に自動調整されます。20並列で開始、429エラーで半減、自動的に回復します。
- **10のコードパーサー** — Go（go/ast経由）、TypeScript、JavaScript、Python、Rust、Java、C、C++、Ruby、およびJSON/YAML/TOMLキー抽出。コードはLLM呼び出しなしで構造的要約を取得します。
- **オンデマンドコンパイル** — MCPを介した`wiki_compile_topic("flash attention")`が関連ソースをリアルタイムでコンパイルします。
- **品質スコアリング** — 記事ごとのソースカバレッジ、抽出完全性、クロスリファレンス密度が自動的に追跡されます。

設定、ティアオーバーライドの例、パフォーマンス目標については、[完全なスケーリングガイド](docs/guides/large-vault-performance.md)を参照してください。

## 検索品質

sage-wikiは[qmd](https://github.com/dmayboroda/qmd)の検索アプローチの分析にインスパイアされた、Q&Aクエリ用の強化検索パイプラインを使用しています：

- **チャンクレベルインデックス** — 記事は約800トークンのチャンクに分割され、各チャンクに独自のFTS5エントリとベクトルエンベディングが付きます。「flash attention」の検索は、3000トークンのTransformer記事内の関連段落を見つけます。
- **LLMクエリ拡張** — 単一のLLM呼び出しがキーワードリライト（BM25用）、セマンティックリライト（ベクトル検索用）、仮説的回答（エンベディング類似度用）を生成します。トップBM25結果が既に高い信頼度の場合、強シグナルチェックにより拡張をスキップします。
- **LLMリランキング** — 上位15候補がLLMにより関連性でスコアリングされます。ポジション認識ブレンディングにより高信頼度の検索結果が保護されます（ランク1-3は75%の検索重み、ランク11以上は60%のリランカー重み）。
- **クロスリンガルベクトル検索** — すべてのチャンクベクトルに対する完全な総当たりコサイン検索を、RRFフュージョンによりBM25と組み合わせます。これにより、字句の重複がゼロでも多言語クエリ（例：ポーランド語クエリ対英語コンテンツ）がセマンティックに関連する結果を見つけます。
- **グラフ強化コンテキスト拡張** — 検索後、4シグナルグラフスコアラーがオントロジーを介して関連記事を検出します：直接関係（×3.0）、共有ソースドキュメント（×4.0）、Adamic-Adar共通近傍（×1.5）、エンティティタイプアフィニティ（×1.0）。これにより、キーワード/ベクトル検索で見つからなかった構造的に関連する記事が浮上します。
- **トークン予算制御** — クエリコンテキストは設定可能なトークン制限（デフォルト8000）に上限が設けられ、記事は各4000トークンで切り捨てられます。貪欲な充填が最高スコアの記事を優先します。

|                 | sage-wiki                                  | qmd               |
| --------------- | ------------------------------------------ | ----------------- |
| チャンク検索     | FTS5 + ベクトル（デュアルチャネル）           | ベクトルのみ       |
| クエリ拡張      | LLMベース（lex/vec/hyde）                   | LLMベース         |
| リランキング     | LLM + ポジション認識ブレンディング           | クロスエンコーダー |
| グラフコンテキスト | 4シグナルグラフ拡張 + 1ホップ走査          | グラフなし         |
| クエリあたりコスト | 無料（Ollama）/ 約$0.0006（クラウド）      | 無料（ローカルGGUF） |

設定なし = すべての機能が有効です。Ollamaやその他のローカルモデルでは、強化検索は完全に無料です — リランキングは自動無効化されますが（ローカルモデルは構造化JSONスコアリングが苦手）、チャンクレベル検索とクエリ拡張は引き続き動作します。クラウドLLMでは追加コストはごくわずかです（クエリあたり約$0.0006）。拡張とリランキングは設定で切り替え可能です。設定、コスト内訳、詳細な比較については、[完全な検索品質ガイド](docs/guides/search-quality.md)を参照してください。

## プロンプトのカスタマイズ

sage-wikiは要約と記事執筆に組み込みプロンプトを使用します。カスタマイズするには：

```bash
sage-wiki init --prompts    # デフォルトでprompts/ディレクトリをスキャフォールド
```

これにより、編集可能なmarkdownファイルが作成されます：

```
prompts/
├── summarize-article.md    # 記事の要約方法
├── summarize-paper.md      # 論文の要約方法
├── write-article.md        # コンセプト記事の執筆方法
├── extract-concepts.md     # コンセプトの特定方法
└── caption-image.md        # 画像の説明方法
```

任意のファイルを編集して、sage-wikiがそのタイプをどのように処理するかを変更します。`summarize-{type}.md`を作成して新しいソースタイプを追加します（例：`summarize-dataset.md`）。ファイルを削除すると組み込みデフォルトに戻ります。

### カスタムフロントマターフィールド

記事のフロントマターは2つのソースから構築されます：**グラウンドトゥルースデータ**（コンセプト名、エイリアス、ソース、タイムスタンプ）は常にコードによって生成され、**セマンティックフィールド**はLLMによって評価されます。

デフォルトでは、`confidence`が唯一のLLM評価フィールドです。カスタムフィールドを追加するには：

1. `config.yaml`で宣言します：

```yaml
compiler:
  article_fields:
    - language
    - domain
```

2. `prompts/write-article.md`テンプレートを更新して、LLMにこれらのフィールドを要求します：

```
At the end of your response, state:
Language: (the primary language of the concept)
Domain: (the academic field, e.g., machine learning, biology)
Confidence: high, medium, or low
```

LLMのレスポンスは記事本文から抽出され、YAMLフロントマターに自動的にマージされます。結果のフロントマターは以下のようになります：

```yaml
---
concept: self-attention
aliases: ["scaled dot-product attention"]
sources: ["raw/transformer-paper.md"]
confidence: high
language: English
domain: machine learning
created_at: 2026-04-10T08:00:00+08:00
---
```

グラウンドトゥルースフィールド（`concept`、`aliases`、`sources`、`created_at`）は常に正確です — 抽出パスから取得され、LLMからではありません。セマンティックフィールド（`confidence` + カスタムフィールド）はLLMの判断を反映します。

## コントリビューションパック

パックは、特定のドメイン向けのオントロジー型、プロンプト、サンプルソースをバンドルするインストール可能な設定プロファイルです。sage-wikiには、オフラインで動作する8つのバンドルパックが付属しています：

| パック | 対象 | 主なオントロジー |
|------|------|-------------|
| `academic-research` | 研究者 | cites, contradicts, finding, hypothesis |
| `software-engineering` | 開発チーム | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | ノート管理 | relates_to, inspired_by, fleeting_note |
| `study-group` | 学生 | explains, prerequisite_of, definition |
| `meeting-organizer` | マネージャー | decided, assigned_to, action_item |
| `content-creation` | ライター | references, revises, draft, published |
| `legal-compliance` | 法務チーム | regulates, supersedes, policy, control |

```bash
# 初期化時にバンドルパックを適用
sage-wiki init --pack academic-research

# 既存プロジェクトにインストール・適用
sage-wiki pack install academic-research
sage-wiki pack apply academic-research --mode merge

# 利用可能なパックを閲覧
sage-wiki pack list
sage-wiki pack search "research"
```

パックは組み合わせ可能です — 複数のパックを適用すると、オントロジー型はユニオンマージされます。コミュニティパックは[sage-wiki-packs](https://github.com/xoai/sage-wiki-packs)レジストリで配布されています。詳細は[CONTRIBUTING.md](CONTRIBUTING.md)をご覧ください。

## 外部パーサー

sage-wikiには12以上のフォーマットのビルトインパーサーがあります。それ以外のフォーマットには、任意の言語でスクリプトとして外部パーサーを追加できます。

外部パーサーはstdin/stdoutプロトコルを使用します：sage-wikiがファイルの内容をstdinにパイプし、スクリプトがプレーンテキストをstdoutに出力します。

```yaml
# parsers/parser.yaml
parsers:
  - extensions: [".rtf"]
    command: python3
    args: ["rtf_parser.py"]
    timeout: 30s
```

セキュリティ：外部パーサーはタイムアウト制限、環境変数のストリップ、Linuxでのネットワーク分離で実行されます。`parsers.external: true`による明示的なオプトインが必要です。詳細は[CONTRIBUTING.md](CONTRIBUTING.md)をご覧ください。

## エージェントスキルファイル

sage-wikiには17のMCPツールがありますが、エージェントのコンテキストにWikiを*いつ*チェックすべきかを示すものがなければ、ツールは使用されません。スキルファイルはそのギャップを埋めます — エージェントに検索のタイミング、キャプチャすべき内容、効果的なクエリ方法を教える生成されたスニペットです。

```bash
# プロジェクト初期化時に生成
sage-wiki init --skill claude-code

# または既存のプロジェクトに追加
sage-wiki skill refresh --target claude-code

# 書き込みなしでプレビュー
sage-wiki skill preview --target cursor
```

これにより、エージェントの指示ファイル（CLAUDE.md、.cursorrules等）にプロジェクト固有のトリガー、キャプチャガイドライン、config.yamlから導出されたクエリ例を含む動作スキルセクションが追加されます。

**対応エージェント：** `claude-code`、`cursor`、`windsurf`、`agents-md`（Antigravity/Codex）、`gemini`、`generic`

スキルファイルは汎用的なベーステンプレートを提供します — いつ検索するか、何をキャプチャするか、どうクエリするか — config.yamlのエンティティ型とリレーション型を使用します。ドメイン固有のエージェント動作（研究トリガー、会議キャプチャパターンなど）には、[コントリビューションパック](#コントリビューションパック)を適用してください：

```bash
sage-wiki init --skill claude-code --pack academic-research
```

パックの`skills/`ディレクトリがベーススキルと共にドメイン固有のトリガーを追加します。`skill refresh`を実行すると、マーカー間のスキルセクションのみが再生成されます — 他のコンテンツは保持されます。

## MCP統合

![MCP統合](sage-wiki-interfaces.png)

### Claude Code

`.mcp.json`に追加します：

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", "/path/to/wiki"]
    }
  }
}
```

### SSE（ネットワーククライアント）

```bash
sage-wiki serve --transport sse --port 3333
```

## AI会話からの知識キャプチャ

sage-wikiはMCPサーバーとして動作するため、AI会話から直接知識をキャプチャできます。Claude Code、ChatGPT、Cursor、またはその他のMCPクライアントに接続して、次のように依頼するだけです：

> 「コネクションプーリングについてわかったことをWikiに保存して」

> 「このデバッグセッションの主要な決定をキャプチャして」

`wiki_capture`ツールはLLMを介して会話テキストからナレッジアイテム（決定、発見、修正）を抽出し、ソースファイルとして書き込み、コンパイルのキューに入れます。ノイズ（挨拶、リトライ、行き止まり）は自動的にフィルタリングされます。

単一の事実には`wiki_learn`が直接ナゲットを保存します。完全なドキュメントには`wiki_add_source`がファイルを取り込みます。`wiki_compile`を実行してすべてを記事にプロセスします。

完全なセットアップガイド：[エージェントメモリレイヤーガイド](docs/guides/agent-memory-layer.md)

## チームセットアップ

sage-wikiは個人のWikiから3-50人のチーム向け共有ナレッジベースまでスケールします。3つのデプロイパターン：

**Git同期リポジトリ**（3-10人） — Wikiがgitリポジトリに置かれます。全員がクローンし、ローカルでコンパイルし、プッシュします。コンパイル済みの`wiki/`ディレクトリは追跡され、データベースは`.gitignore`対象で各コンパイル時に再構築されます。

**共有サーバー**（5-30人） — Web UI付きでsage-wikiをサーバーで実行します。チームメンバーはブラウザで閲覧し、MCPをSSE経由でエージェントに接続します。

**ハブフェデレーション**（マルチプロジェクト） — 各プロジェクトが独自のWikiを持ちます。ハブシステムがそれらを`sage-wiki hub search`で単一の検索インターフェースにフェデレートします。

```bash
# ハブ: 複数のWikiを登録して横断検索
sage-wiki hub add /projects/backend-wiki
sage-wiki hub add /projects/ml-wiki
sage-wiki hub search "deployment process"
```

**チームが得られるもの：**

- **蓄積される組織の記憶。** 1つのエージェントが学んだことを、すべてのエージェントが知っています。任意のセッションでキャプチャされた決定、慣例、落とし穴が全員から検索可能になります。
- **信頼ゲート付き出力。** [出力信頼システム](docs/guides/output-trust.md)はLLM回答をグラウンディング検証とコンセンサス確認が行われるまで隔離します。1つのエージェントのハルシネーションが共有コーパスを汚染することはありません。
- **エージェントスキルファイル。** 生成された指示が各チームメンバーのAIエージェントに、Wikiをいつチェックし、何をキャプチャし、どうクエリするかを教えます。Claude Code、Cursor、Windsurf、Codex、Geminiに対応。
- **ユーザーごとのサブスクリプション認証。** 各開発者が自分のLLMサブスクリプションを使用 — リポジトリに共有APIキーは不要です。設定は`auth: subscription`とし、認証情報は`~/.sage-wiki/auth.json`にユーザーごとに保存されます。
- **完全な監査証跡。** `auto_commit: true`がすべてのコンパイルでgitコミットを作成します。誰が何をいつ変更したかを追跡。

```yaml
# 推奨チーム設定
trust:
  include_outputs: verified    # 検証まで隔離
compiler:
  default_tier: 1              # 高速インデックス、オンデマンドコンパイル
  auto_commit: true            # 監査証跡
```

ソースの整理、エージェント統合ワークフロー、知識キャプチャパイプライン、スケーリングの考慮事項、スタートアップ、研究室、Obsidianボールトチーム向けのすぐに使えるレシピについては、[完全なチームセットアップガイド](docs/guides/team-setup.md)を参照してください。

## ベンチマーク

1,107のソース（49.4 MBデータベース、2,832のWikiファイル）からコンパイルされた実際のWikiで評価されました。

自分のプロジェクトで再現するには`python3 eval.py .`を実行してください。詳細は[eval.py](eval.py)を参照してください。

### パフォーマンス

| 操作 | p50 | スループット |
| ------------------------------------ | ----: | -----------: |
| FTS5キーワード検索（top-10） | 411µs | 1,775 qps |
| ベクトルコサイン検索（2,858 × 3072d） | 81ms | 15 qps |
| ハイブリッドRRF（BM25 + ベクトル） | 80ms | 16 qps |
| グラフ走査（BFS深度 ≤ 5） | 1µs | 738K qps |
| サイクル検出（全グラフ） | 1.4ms | — |
| FTS挿入（バッチ100） | — | 89,802 /s |
| 持続的混合リード | 77µs | 8,500+ ops/s |

非LLMコンパイルオーバーヘッド（ハッシュ + 依存関係分析）は1秒未満です。コンパイラの実行時間はほぼ完全にLLM API呼び出しによって支配されます。

### 品質

| 指標 | スコア |
| --------------------------- | --------: |
| 検索recall@10 | **100%** |
| 検索recall@1 | 91.6% |
| ソース引用率 | 94.6% |
| エイリアスカバレッジ | 90.0% |
| ファクト抽出率 | 68.5% |
| Wiki接続性 | 60.5% |
| クロスリファレンス整合性 | 50.0% |
| **全体品質スコア** | **73.0%** |

### 評価の実行

```bash
# 完全な評価（パフォーマンス + 品質）
python3 eval.py /path/to/your/wiki

# パフォーマンスのみ
python3 eval.py --perf-only .

# 品質のみ
python3 eval.py --quality-only .

# 機械可読JSON
python3 eval.py --json . > report.json
```

Python 3.10以上が必要です。ベクトルベンチマークを約10倍高速にするには`numpy`をインストールしてください。

### テストの実行

```bash
# フルテストスイートの実行（合成フィクスチャを生成、実データ不要）
python3 -m unittest eval_test -v

# スタンドアロンテストフィクスチャの生成
python3 eval_test.py --generate-fixture ./test-fixture
python3 eval.py ./test-fixture
```

24のテストをカバー：フィクスチャ生成、CLIモード（`--perf-only`、`--quality-only`、`--json`）、JSONスキーマ検証、スコア境界、検索recall、エッジケース（空のWiki、大規模データセット、存在しないパス）。

## アーキテクチャ

![Sage-Wiki アーキテクチャ](sage-wiki-architecture.png)

- **ストレージ：** FTS5（BM25検索）+ BLOBベクトル（コサイン類似度）+ ソースごとのティア/状態追跡用compile_itemsテーブルを持つSQLite
- **オントロジー：** BFS走査とサイクル検出を備えた型付きエンティティ-関係グラフ
- **検索：** チャンクレベルFTS5 + ベクトルインデックス、LLMクエリ拡張、LLMリランキング、RRFフュージョン、4シグナルグラフ拡張を備えた強化パイプライン。検索レスポンスはオンデマンドコンパイル用の未コンパイルソースを通知します。
- **コンパイラ：** 適応的バックプレッシャー、並行Pass 2抽出、プロンプトキャッシュ、Batch API（Anthropic + OpenAI + Gemini）、コスト追跡、MCP経由のオンデマンドコンパイル、品質スコアリング、カスケード認識を備えたティアードパイプライン（ティア0：インデックス、ティア1：エンベッド、ティア2：コードパース、ティア3：フルLLMコンパイル）。エンベディングには指数バックオフ付きリトライ、オプションのレート制限、長い入力用のミーンプーリングが含まれます。10の組み込みコードパーサー（go/ast経由のGo、正規表現経由の8言語、構造化データキー抽出）。
- **MCP：** stdioまたはSSE経由の17ツール（6読み取り、9書き込み、2複合）。オンデマンドコンパイル用の`wiki_compile_topic`と知識抽出用の`wiki_capture`を含みます。
- **TUI：** bubbletea + glamourの4タブターミナルダッシュボード（ブラウズ、検索、Q&A、コンパイル）。ティア分布表示付き。
- **Web UI：** ビルドタグ（`-tags webui`）による`go:embed`で埋め込まれたPreact + Tailwind CSS
- **Scribe：** 会話からの知識取り込み用拡張可能インターフェース。セッションScribeがClaude Code JSONLトランスクリプトを処理します。
- **パック:** コントリビューションパックシステム — 8つのバンドルパック、Gitベースレジストリ、インストール/適用/削除/更新ライフサイクル、スナップショットロールバック付きトランザクショナル適用。
- **外部パーサー:** stdin/stdoutサブプロセスプロトコルによるランタイムプラガブルファイルフォーマットパーサー。タイムアウト、環境ストリップ、ネットワーク分離によるサンドボックス実行。

CGOなし。純粋Go。クロスプラットフォーム。

## ライセンス

MIT
