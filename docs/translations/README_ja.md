[English](../../README.md) | [中文](README_zh.md) | **日本語** | [한국어](README_ko.md) | [Tiếng Việt](README_vi.md) | [Français](README_fr.md) | [Русский](README_ru.md)

<!-- translations: may-lag -->
> ⚠️ この翻訳は README.md に遅れている場合があります — 英語版が正本です。

# sage-wiki

**sage-wiki**は、AIエージェントと人間が共に構築し、共にクエリするグラフメモリ兼ナレッジベースです。ドキュメントを投入すると、LLMコンパイラがそれらをナレッジグラフを備えた相互リンクされたWikiに変換します — エージェントはMCPを通じてクエリし、人間はプレーンなmarkdownとして閲覧します。オプトインのグラフパスを有効にすると、*根拠付き*グラフになります：型付きエンティティ、来歴を持つリレーション、解決されたエイリアス、そして回答へのファクト単位の引用です。1つのGoバイナリで、パーソナルボールトからチームハブ、企業ナレッジグラフまでスケールします。

**→ はじめに：[インストール](#インストール) · [クイックスタート](#クイックスタート)**

LLMコンパイル型パーソナルナレッジベースという[Andrej Karpathyのアイデア](https://x.com/karpathy/status/2039805659525644595)から発展し、[Sage Framework](https://github.com/xoai/sage)で構築されています。開発の過程で得られた教訓は[こちら](https://x.com/xoai/status/2040936964799795503)。

- **引用付きグラフメモリ。** `wiki_graph_query`を通じて関係についての質問ができます — 回答はシリアライズされたグラフエッジのみに根拠付けられ、根拠付きグラフを有効にすると、各引用がソースドキュメントと信頼度を伴います。
- **エージェントと人間のために。** 19のMCPツールと生成されたスキルファイルが、いつ検索し、キャプチャし、コンパイルすべきかをエージェントに教えます。人間には、同じデータの上でObsidianネイティブなmarkdown、TUI、Web UIが提供されます。
- **信頼と来歴。** クエリ出力は検証されるまで隔離され、すべての根拠付きリレーションはどのドキュメントがそれを主張したかを記録します。
- **ソースを入れれば、Wikiが出来上がる。** コンパイルパイプラインが論文、ノート、コード、メールを読み取り、要約し、コンセプトを抽出し、相互接続された記事を執筆します — 上記すべてのための取り込みレイヤーです。新しいソースが追加されるたびに既存の記事が充実し、Wikiは成長するほど複利的に価値を増します。
- **Wikiに質問する。** LLMクエリ拡張、リランキング、グラフ認識コンテキスト構築を備えたチャンクレベルのハイブリッド検索が、引用付きの回答を返します。
- **100K以上のドキュメントに対応。** ティアードコンパイルがすべてを高速にインデックスし、重要なところだけにLLM予算を使います。

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_外側の境界上のドットはナレッジベース内のすべてのドキュメントの要約を表し、内側の円のドットはナレッジベースから抽出されたコンセプトを表します。リンクはそれらのコンセプトがどのように相互接続されているかを示しています。_

## パーソナルボールトから企業ナレッジグラフまで

- **パーソナル** — 既存のObsidianボールトにオーバーレイし（`init --vault`）、[ローカルモデル](../guides/local-models.md)でコストゼロで運用し、根拠付きグラフが欲しくなったらグラフパス（`ontology.triples` + `ontology.resolve`）をオプトインします。
- **チーム** — 1つのWikiをgitまたは[セルフホストサーバー](../guides/self-hosted-server.md)で共有し、エンティティ解決の提案と[出力信頼](../guides/output-trust.md)をチームでレビューし、ハブで複数のWikiをフェデレートします。[チームセットアップ](../guides/team-setup.md)を参照してください。
- **企業** — ストレージを[PostgreSQL/pgvector](../guides/storage-backends.md)に移行し、[メトリクス](../guides/metrics.md)を有効にし、サーバーの前段に認証を置き、[ティアードコンパイル](../guides/large-vault-performance.md)で取り込みをスケールさせます。

## ナレッジグラフとグラフメモリ

![sage-wiki グラフエンジン](../../assets/sage-wiki-graph-engine.png)

ベクトル検索はクエリに*似た*文章を取り出します。グラフはさらに **物事がどう関係しているか** を保持するため、2〜3 ホップ必要な問いも、単一チャンクに連鎖全体が含まれていることを期待せず、走査で答えられます。sage-wiki はこのグラフをコンパイル成果物として構築します — 別途同期が必要な第二のデータベースではありません。

- **エンティティと型付き関係。** コンパイルのたびにエンティティ（概念・ソース・成果物）を抽出し、型付き関係で結びます。関係の語彙は利用者が定義できます —
  [設定可能な関係](../guides/configurable-relations.md)を参照。
- **根拠付きのエッジ。** 関係は `evidence`（根拠となる箇所）、`confidence`（0–1）、`source_doc` を保持できます。結論は文書単位ではなく、そのエッジを裏づけた一文まで辿れます。
- **トリプル。** 任意の構造化出力パスが 主語 → 関係 → 目的語 を直接抽出します。明示的な有効化が必要（`ontology.triples`）: 文書ごとに LLM 呼び出しが 1 回増えるため、既定値が黙って課金することはありません。
- **エンティティ解決。** 「K8s」と「Kubernetes」を 1 ノードに統合します。提案は既定でレビューを経由し、黙って統合されません。

**グラフは検索チャネルであり、付随ビューではありません。** すべての検索は 3 つのチャネル（字句 BM25・ベクトル・グラフ近接）を融合します: クエリ語がエンティティを起点にし、範囲を限った走査が近傍を順位付けし、`search.hybrid_weight_graph` で融合されます。オントロジーが空ならコストはゼロで、結果はバイト単位で不変です。

直接問い合わせることも、MCP 経由でエージェントに任せることもできます:

```bash
sage-wiki ontology query --entity kubernetes --depth 3 --direction both
sage-wiki provenance "service mesh"    # この概念を生んだソース
```

エッジはバイテンポラルです: 事実を更新すると古いエッジは衝突せず無効化され、デフォルトの回答は矛盾のないものになり、`as_of` クエリで「1月時点では何を信じていたか」を答えられます。曖昧な矛盾は引き続き
[出力信頼](../guides/output-trust.md)のレビューで表面化します。コーパス全体の問い（「全体の主なテーマは？」）には、オプトインのコミュニティ検出（`ontology.communities.enabled`）がキャッシュ済みコミュニティ要約を生成し、`wiki_graph_query` `mode: "global"` で回答します。詳細:
[グラフメモリ](../guides/graph-memory.md)。

## ガイド

| ガイド | 説明 |
|-------|------|
| [エージェントメモリレイヤー](../guides/agent-memory-layer.md) | MCP設定、スキルファイル、キャプチャワークフロー、読み取り・キャプチャ・進化ループ |
| [HTTP API](../guides/http-api.md) | /v1 REST サーフェス：認証、エラーモデル、冪等性、非同期ジョブ |
| [グラフメモリ](../guides/graph-memory.md) | 根拠付きリレーション、トリプル抽出、エンティティ解決、グラフQA |
| [設定](../guides/configuration.md) | 完全な注釈付きconfig.yaml、マルチプロバイダーセットアップ、serveワーカー |
| [チームセットアップ](../guides/team-setup.md) | Git同期、共有サーバー、ハブフェデレーションのデプロイパターン |
| [検索品質](../guides/search-quality.md) | チャンクインデックス、クエリ拡張、リランキング、グラフ拡張、ANN |
| [大規模ボールトのパフォーマンス](../guides/large-vault-performance.md) | ティアードコンパイル、バックプレッシャー、コードパーサー、100K以上へのスケーリング |
| [出力信頼](../guides/output-trust.md) | グラウンディング検証、コンセンサス、昇格/降格ライフサイクル |
| [サブスクリプション認証](../guides/subscription-auth.md) | OAuthログイン、トークンインポート、認証情報管理 |
| [セルフホストサーバー](../guides/self-hosted-server.md) | Docker Compose、Syncthing、リバースプロキシ、VPSデプロイ |
| [ストレージバックエンド](../guides/storage-backends.md) | SQLite vs PostgreSQL/pgvectorのセットアップ、切り替え、プールサイズ調整 |
| [設定可能な関係](../guides/configurable-relations.md) | カスタムオントロジー型、多言語シノニム、型制限 |
| [プロンプトのカスタマイズ](../guides/customizing-prompts.md) | プロンプトスキャフォールディング、タイプ別オーバーライド、カスタムフロントマターフィールド |
| [ローカルモデル](../guides/local-models.md) | Ollamaセットアップ、GPU/CPUルーティング、パス別モデル設定 |
| [メトリクス](../guides/metrics.md) | ログスナップショット、/metricsエンドポイント、カーディナリティ制御 |
| [コントリビューションパック](../../CONTRIBUTING.md) | パック作成、パーサー開発、レジストリ登録 |

## インストール

```bash
# CLIのみ（Web UIなし）
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# Web UI付き（フロントエンドアセットのビルドにNode.jsが必要）
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## クイックスタート

![コンパイラパイプライン](../../assets/sage-wiki-compiler-pipeline.png)

### グリーンフィールド（新規プロジェクト）

```bash
sage-wiki init my-wiki && cd my-wiki
# raw/ にソースを追加
cp ~/papers/*.pdf raw/
# config.yaml を編集して API キーを追加し、LLM を選択
sage-wiki compile                                  # 初回コンパイル
sage-wiki search "attention mechanism"             # ハイブリッド検索
sage-wiki query "How does flash attention work?"   # 引用付き Q&A
sage-wiki tui                                      # ターミナルダッシュボード
sage-wiki serve --ui                               # ブラウザ（webui ビルド）
sage-wiki compile --watch                          # フォルダ監視
```

`config.yaml`の全キーを行ごとに注釈付きで解説：[設定](../guides/configuration.md)。

**プロジェクト構成**（`init`が作成するもの — 抜粋、網羅的ではなく例示）：

```
my-wiki/
├── config.yaml           # プロバイダー、モデル、コンパイラ、検索、オントロジー
├── raw/                  # ソースをここに置く（記事、論文、コード、画像）
├── wiki/                 # コンパイル出力 — Obsidian互換markdown
│   ├── summaries/        # ソースごとのLLM要約
│   ├── concepts/         # コンセプト記事（ナレッジグラフ）
│   ├── images/           # ビジョンキャプション付き画像説明
│   ├── outputs/          # ファイリングされたクエリ回答（trust.include_outputs: "true"）
│   ├── under_review/     # レビュー待ちの回答（デフォルト）
│   └── archive/          # 刈り込まれた記事
├── .sage/wiki.db         # 単一SQLiteファイル：FTS索引、ベクトル、オントロジー、キュー
└── .manifest.json        # ソース↔記事の対応 + コンパイル状態
```

### ボールトオーバーレイ（既存のObsidianボールト）

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# config.yaml を編集してソース/除外フォルダを設定、API キーを追加、LLM を選択
sage-wiki compile --watch
```

コンテナがお好みですか？ビルド済みのマルチアーキテクチャDockerイメージとcomposeファイルについては、[セルフホストサーバーガイド](../guides/self-hosted-server.md)で説明しています。

## 対応ソースフォーマット

| フォーマット | 拡張子 | 抽出される内容 |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | フロントマターを分離してパースした本文テキスト |
| PDF         | `.pdf`                                  | 純粋なGo実装によるフルテキスト抽出 |
| Word        | `.docx`                                 | XMLからのドキュメントテキスト |
| Excel       | `.xlsx`                                 | セル値とシートデータ |
| PowerPoint  | `.pptx`                                 | スライドのテキストコンテンツ |
| CSV         | `.csv`                                  | ヘッダー＋行（最大1000行） |
| EPUB        | `.epub`                                 | XHTMLからの章テキスト |
| Email       | `.eml`                                  | ヘッダー（from/to/subject/date）＋本文 |
| プレーンテキスト | `.txt`, `.log`                      | 生のコンテンツ |
| トランスクリプト | `.vtt`, `.srt`                      | 生のコンテンツ |
| 画像        | `.png`, `.jpg`, `.gif`, `.webp`, `.svg`, `.bmp` | ビジョンLLMによる説明（キャプション、コンテンツ、表示テキスト） |
| コード      | `.go`, `.py`, `.js`, `.ts`, `.rs` など  | ソースコード |

ファイルをソースフォルダにドロップするだけ — sage-wikiがフォーマットを自動検出します。画像にはビジョン対応のLLM（Gemini、Claude、GPT-4o）が必要です。記載のないフォーマットが必要ですか？sage-wikiは[外部パーサー](#外部パーサー)をサポートしています — stdinを読み取り、テキストをstdoutに書き出す任意の言語のスクリプトです。

## グラフメモリ

Wikiは標準でキーワード近接からナレッジグラフを構築します —
関係キーワードが同じブロック内で`[[wikilink]]`と共起する箇所で
コンセプトがリンクされます。**オプトインのグラフパス**を有効にすると、
これが根拠付きグラフになります：

- **トリプル抽出**（`ontology.triples.enabled`） — フルコンパイルされた
  ドキュメントごとに1回の追加LLM呼び出しで型付きエンティティとリレーションを
  抽出し、それぞれが根拠スパン、信頼度、ソースドキュメントを伴います。
- **エンティティ解決**（`ontology.resolve.enabled`） — 表層形のバリアント
  （「NASA」/「National Aeronautics and Space Administration」）が
  正規エンティティにリンクされます。高信頼度の提案は自動適用され
  （閾値0.85。レビューのみにするには正確に`1.0`を設定）、すべてのリンクは
  `ontology resolve --unlink`で正確に元に戻せます。
- **グラフQA** — `wiki_graph_query` MCPツールは、境界が定められ
  シリアライズされたエッジ集合*のみ*に根拠付けてマルチホップの関係質問に
  回答します。エッジが根拠付きの場合、引用は`source_doc`と`confidence`を
  伴います（キーワード近接エッジはどちらも伴いません）。通常のQ&A
  コンテキストでも、各関連記事の下に接続エッジの名前が示されます。

深掘り、コスト、レビューワークフロー、取り消しのセマンティクス：[グラフメモリ](../guides/graph-memory.md)。

## コマンド

コアとなるコマンド群です。フラグは`sage-wiki <command> --help`で確認できます。

| コマンド | 説明 |
| ------- | ----------- |
| `sage-wiki init [dir] [--vault] [--skill <agent>] [--pack <name>] [--prompts] [--force]` | プロジェクトの初期化（グリーンフィールドまたはボールトオーバーレイ） |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | ソースをWiki記事にコンパイル |
| `sage-wiki serve [--transport stdio\|sse] [--ui] [--port 3333]` | MCPサーバー / Web UI |
| `sage-wiki reindex [--drop-chunk-vectors]` | 現在の `chunk_size` / `chunk_overlap_tokens` でディスク上のドキュメントからチャンクインデックスを再構築 |
| `sage-wiki search "query" [--tags ...] [--boost-tags ...] [--limit N] [--channels bm25,vector,graph] [--expand] [--rerank]` | ハイブリッド検索（BM25 + ベクトル + オントロジーグラフ） |
| `sage-wiki query "question"` | Wikiに対する引用付きQ&A |
| `sage-wiki tui` | インタラクティブターミナルダッシュボード |
| `sage-wiki ontology <query\|list\|add\|resolve>` | オントロジーグラフの照会、管理、解決 |
| `sage-wiki ingest <url\|path>` / `sage-wiki add-source <path>` | ソースを追加 |
| `sage-wiki source <show\|list>` / `sage-wiki coverage` | ソースとコンパイルカバレッジの検査 |
| `sage-wiki status` / `sage-wiki doctor` / `sage-wiki diff` | ヘルス、設定検証、保留中の変更 |
| `sage-wiki lint [--fix]` / `sage-wiki list` / `sage-wiki write <summary\|article>` | メンテナンスと手動書き込み |
| `sage-wiki hub <init\|add\|remove\|search\|status\|list\|compile>` | マルチプロジェクトハブ |
| `sage-wiki learn "text"` / `sage-wiki capture "text"` / `sage-wiki scribe <session-file>` | 知識キャプチャ |
| `sage-wiki skill <refresh\|preview> [--target <agent>]` | エージェントスキルファイルの生成またはリフレッシュ |
| `sage-wiki provenance <source-or-concept>` / `sage-wiki version` | 来歴マッピング、バージョン |

トピック別のコマンド群はそれぞれのガイドにあります：`pack *`は
[CONTRIBUTING](../../CONTRIBUTING.md)、`auth *`（login、import、status、logout、
migrate）は[サブスクリプション認証](../guides/subscription-auth.md)、
`verify` / `outputs *`は[出力信頼](../guides/output-trust.md)を参照してください。

## TUI

```bash
sage-wiki tui
```

4つのタブを持つフル機能のターミナルダッシュボード：

- **[F1] ブラウズ** — セクション別（コンセプト、要約、出力）に記事をナビゲート。矢印キーで選択、Enterでglamourレンダリングされたmarkdownを読み、Escで戻ります。
- **[F2] 検索** — 分割ペインプレビュー付きのファジー検索。入力してフィルタリングし、結果はハイブリッドスコアでランク付けされ、Enterで`$EDITOR`で開きます。
- **[F3] Q&A** — 対話型ストリーミングQ&A。質問すると、ソース引用付きのLLM合成回答が得られます。Ctrl+Sで回答をoutputs/に保存します。
- **[F4] コンパイル** — ライブコンパイルダッシュボード。ソースディレクトリの変更を監視して自動再コンパイルします。プレビュー付きでコンパイル済みファイルをブラウズできます。

タブ切り替え：任意のタブから`F1`-`F4`、ブラウズ/コンパイルでは`1`-`4`、`Esc`でブラウズに戻ります。終了は`Ctrl+C`。

## Web UI

```bash
sage-wiki serve --ui        # http://127.0.0.1:3333、-tags webui ビルドが必要
```

- レンダリングされたmarkdown、シンタックスハイライト、クリック可能な`[[wikilinks]]`を備えた**記事ブラウザ**
- ランク付けされた結果とスニペットを備えた**ハイブリッド検索**
- **ナレッジグラフ** — コンセプトとその接続のインタラクティブなフォースディレクテッド可視化
- **ストリーミングQ&A** — 質問すると、ソース引用付きのLLM合成回答が得られます
- スクロールスパイ付きの**目次**。システム設定検出付きのダーク/ライトモード。壊れた記事リンクはグレーで表示

Preact + Tailwindで構築され、`go:embed`で埋め込まれます（約1.2 MB、gzip圧縮で約420 KB）。CLI/MCP専用バイナリにするには`-tags webui`を省略してください。認証トークン、許可ホスト、デプロイの堅牢化：[セルフホストサーバー](../guides/self-hosted-server.md)。

## MCP統合

![MCP統合](../../assets/sage-wiki-interfaces.png)

`.mcp.json`に追加します（Claude Codeの場合。他のエージェントは[エージェントメモリレイヤーガイド](../guides/agent-memory-layer.md)を参照）：

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--transport", "stdio", "--project", "/path/to/wiki"]
    }
  }
}
```

ネットワーククライアント向け：`sage-wiki serve --transport sse --port 3333`。
サーバーは19のツールを公開します — 検索、読み取り、グラフクエリ、キャプチャ、
`wiki_query`（レビュー付きファイリングでの質問応答）、オンデマンドコンパイルなど。エージェントごとのセットアップとキャプチャ
ワークフローは[エージェントメモリレイヤーガイド](../guides/agent-memory-layer.md)にあります。

**エージェントスキルファイル** — `sage-wiki skill refresh --target <agent>`は、
エージェントの指示ファイル（CLAUDE.md、.cursorrules など）に、いつ検索し、
何をキャプチャし、どうクエリするかを教える動作セクションを書き込みます。
内容はあなたの設定から導出されます。ターゲット：`claude-code`、`cursor`、
`windsurf`、`agents-md`（Antigravity）、`codex`、`gemini`、`generic`。

### エージェントスキル

sage-wikiのリファレンススキルをインストールすると、コーディングアシスタントが
このREADMEを読まなくてもツールの全表面—19のMCPツール、`/v1` REST相当、
オプトインフラグ、ティア、非同期コンパイルのセマンティクス、エラーコード—を
把握できます：

```bash
# Claude Code
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki

# または手動で：skills/sage-wiki/SKILL.md を .claude/skills/ にコピー
```

パイプラインスキル `sage-wiki-integrate` は、新しいリポジトリへのsage-wikiの
組み込みを対話的に行います（言語検出 → クライアントのインストールまたは
MCP設定 → 保存と検索のスモークテスト）：

```bash
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki-integrate
```

両スキルはライブのMCPレジストリから生成され（`go run ./tools/skillgen/`）、
CIでドリフトチェックされます—ツールが変わっても陳腐化しません。
Pre-1.0 — バージョンを固定してください。

**知識キャプチャ** — エージェントは`wiki_capture` / `wiki_learn`を介して
インサイトを書き戻し、読み取り・キャプチャ・進化のループを閉じます。
ワークフローとヒント：[エージェントメモリレイヤー](../guides/agent-memory-layer.md)。

## クライアントSDK

`/v1` REST APIの型付きクライアント（Pre-1.0 — バージョンを固定してください）：

**Python** — `pip install sagewiki`（≥3.9、`httpx`のみ）：

```python
from sagewiki import SageWiki

c = SageWiki()  # 環境変数 SAGE_WIKI_URL / SAGE_WIKI_TOKEN
for r in c.search("attention", limit=5).results:
    print(r.final_score, r.content[:80])
job = c.compile(topic="attention")
job.wait(timeout=600)  # 明示的なタイムアウトが必須
```

**TypeScript** — `npm install sagewiki`（ランタイム依存ゼロ、グローバル
`fetch`。Node ≥18、Deno、Bun、エッジランタイム）：

```ts
import { SageWikiClient } from "sagewiki";

const c = new SageWikiClient();
const results = await c.search("attention", { limit: 5 });
const job = await c.compile({ topic: "attention" });
await job.waitUntilDone({ timeoutMs: 600_000 });
```

両クライアントとも`/v1`サーフェス全体をカバー：検索、プロベナンス、
グラフクエリ、コンパイル済みwiki、キャプチャ/書き込み、非同期
compile/lintジョブとコード駆動のエラー分類。ドキュメント：
[Python](../../clients/python/README.md) · [TypeScript](../../clients/typescript/README.md) ·
[HTTP APIガイド](../guides/http-api.md)。GoプログラムはHTTPを完全に
省略できます — [Goプログラムへの埋め込み](#goプログラムへの埋め込み)を参照。

### 使用例

実サーバーに対してCIで検証される、コピー可能なフレームワーク統合：

- [`examples/langgraph/`](../../examples/langgraph/) — メモリバックドのLangGraph
  ノード（Pythonクライアント）：`uncompiled_sources` → トピックコンパイルの
  パターンによる取得とキャプチャ。
- [`examples/vercel-ai-sdk/`](../../examples/vercel-ai-sdk/) — `search`、
  `graphQuery`、`provenance`をVercel AI SDKツールとして提供
  （TypeScriptクライアント）。エッジにデプロイ可能。

### Goプログラムへの埋め込み

サブプロセスや stdio、ポート管理なしに、自分の Go プロセスから同じツールを呼び出すには、mcp-go のインプロセストランスポートと `pkg/sagewiki` を使います：

```go
srv, err := sagewiki.NewServer("/path/to/wiki")  // project must already exist
if err != nil {
    return err
}
defer srv.Close()  // the caller owns the DB handle here

cli, err := client.NewInProcessClient(srv.MCPServer())
if err != nil {
    return err
}
defer cli.Close()

if err := cli.Start(ctx); err != nil {
    return err
}
if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
    return err
}

res, err := cli.CallTool(ctx, mcp.CallToolRequest{
    Params: mcp.CallToolParams{
        Name:      "wiki_search",
        Arguments: map[string]any{"query": "attention", "limit": 5},
    },
})
```

プロジェクトは事前に存在している必要があり、呼び出し側がデータベースハンドルを所有するため `Close` は必須です — `serve` とは異なり、他の何かが閉じてくれることはありません。ログはホストの stderr に出力され、`initialize` は sage-wiki のビルドバージョンを報告します（素の `go build` では `dev`）。起動時に `sagewiki.SetVersion` を呼ぶと、独自のバージョン文字列を報告できます。

このパッケージは sage-wiki が 1.0 未満の間は**実験的**です：Go のシグネチャは維持される想定ですが、ツール名、引数スキーマ、`config.yaml` のレイアウトはリリースごとに変わる可能性があります。バージョンを固定してください。

## 運用

- **ストレージ** — デフォルトはSQLite（単一ファイル、設定不要）。サーバー
  デプロイにはPostgreSQL + pgvector。切り替えとプールサイズ調整：[ストレージバックエンド](../guides/storage-backends.md)。
- **オブザーバビリティ** — 構造化ログスナップショットとオプトインの`/metrics`
  エンドポイント：[メトリクス](../guides/metrics.md)。
- **構造化出力** — LLM抽出パスは各プロバイダーのネイティブメカニズム
  （Anthropicツール使用、OpenAI `response_format`、Gemini
  `responseSchema`）を使用し、検証付きのフェンス除去フォールバックを備えます。
- **認証情報** — サブスクリプショントークンは利用可能な環境ではOSの
  キーチェーンに保存されます。ファイル保存された認証情報を移行するには
  `sage-wiki auth migrate`を一度実行してください。[サブスクリプション認証](../guides/subscription-auth.md)。
- **設定** — すべてのキーの注釈付き解説、マルチプロバイダーレシピ、
  serveモードのコンパイルワーカー：[設定](../guides/configuration.md)。
- **エンティティ解決** — 0.85で自動適用、`--unlink`で正確に元に戻せます。上記の[グラフメモリ](#グラフメモリ)を参照。
- **カスタム関係/エンティティ型** — 組み込み型の拡張や独自型の追加
  （`ontology.relation_types`）が可能で、多言語シノニムと型制限に
  対応：[設定可能な関係](../guides/configurable-relations.md)。
- **出力信頼** — クエリ出力は、グラウンディングされるか、コンセンサスで
  確認されるか、手動で昇格されるまで隔離されます：[出力信頼](../guides/output-trust.md)。
- **検索チューニング** — チャンキング、拡張、リランキング、グラフ拡張、
  オプトインのANN：[検索品質](../guides/search-quality.md)。

### コスト

sage-wikiはトークン使用量を追跡し、すべてのコンパイルのコストを見積もります。
**プロンプトキャッシュ**（デフォルトで有効）はコンパイルパス内の呼び出し間で
システムプロンプトを再利用し — AnthropicとGeminiは明示的に、OpenAIは
自動的にキャッシュ — 入力トークンを50-90%節約します。**Batch API**
（Anthropic、OpenAI、Gemini）は大規模コンパイルのコストを半減させます：

```bash
sage-wiki compile --batch       # バッチを送信、チェックポイント、終了
sage-wiki compile               # ステータスをポーリング、完了時に取得
```

`compile --estimate`はコストをプレビューし、`compiler.mode: auto`は閾値を
超えると自動的にバッチ処理します。詳細：[設定](../guides/configuration.md)。

### 大規模ボールトへのスケーリング

ティアードコンパイルは、すべてをLLMコンパイルする代わりに、各ソースを
タイプと使用状況に基づいてルーティングします：

| ティア | 処理内容 | コスト | ドキュメントあたりの時間 |
|------|-------------|------|-------------|
| **0** — インデックスのみ | FTS5全文検索 | 無料 | 約5ms |
| **1** — インデックス + エンベッド | FTS5 + ベクトルエンベディング | 約$0.00002 | 約200ms |
| **2** — コードパース | 正規表現パーサーによる構造的要約（LLMなし） | 無料 | 約10ms |
| **3** — フルコンパイル | 要約 + コンセプト抽出 + 記事執筆 | 約$0.05-0.15 | 約5-8分 |

大規模ボールトの場合：まずティア1ですべてをインデックスし（100Kドキュメントの
ボールトで約5.5時間）、その後オンデマンドでコンパイルします — 自動昇格、
バックプレッシャー、コードパーサーについては
[大規模ボールトのパフォーマンス](../guides/large-vault-performance.md)で説明しています。

## エコシステム

### コントリビューションパック

パックは、ドメイン向けのオントロジー型、プロンプト、スキルトリガーを
バンドルします。8つのバンドルパックがオフラインで動作します：

| パック | 対象 | 主なオントロジー |
|------|----------|-------------|
| `academic-research` | 研究者 | cites, contradicts, finding, research_hypothesis |
| `software-engineering` | 開発チーム | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | ノート管理 | relates_to, inspired_by, fleeting_note |
| `study-group` | 学生 | explains, prerequisite_of, definition |
| `meeting-organizer` | マネージャー | decided, assigned_to, action_item |
| `content-creation` | ライター | references, revises, draft, published |
| `legal-compliance` | 法務チーム | regulates, supersedes, policy, control |

`sage-wiki init --pack academic-research`は初期化時にパックを1つ適用し、
`pack install <name|url>`で追加できます。パックの作成と公開：
[CONTRIBUTING](../../CONTRIBUTING.md)。

### 外部パーサー

任意の言語のスクリプト（stdin → stdoutにテキスト）で任意のファイル
フォーマットを処理できます。`parsers/parser.yaml`で宣言し、二重の
オプトインの背後にあります — サンドボックスなしのサブプロセスとして
実行されますが、タイムアウト強制と環境変数のストリップが適用されます。
作成と堅牢化の詳細：[CONTRIBUTING](../../CONTRIBUTING.md)、
信頼境界の議論：[チームセットアップ](../guides/team-setup.md)。

### チーム

3つの共有パターン — git同期、共有サーバー、ハブフェデレーション — に
加えて、チームでの信頼レビューとコスト管理：[チームセットアップ](../guides/team-setup.md)。

## ベンチマーク

2 つのスイートは異なる問いに答えます。詳細:
[eval/benchmarks/REPORT.md](../../eval/benchmarks/REPORT.md) · [eval/REPORT.md](../../eval/REPORT.md)

**メモリベンチマーク** — 長い会話について質問に答えられるか。公開データセットを LLM が採点し、
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks) のプロンプトと手順を用い、バックエンドを sage-wiki に置き換えています（回答・採点とも gpt-5、サンプル抽出）:

| ベンチマーク | Score | Mem0 Platform |
|---|---|---|
| LOCOMO (150 q) | **92.0%** @ top-50 | 91.8% @ top-50 |
| LongMemEval-S (30 q) | **93.3%** @ top-50 | 94.8% @ top-50 |
| BEAM 100K (60 q) | **0.691** mean nugget | 0.641 @ 1M |

厳密な優劣比較ではありません: mem0 はマネージド基盤で全問を実行しており、こちらは抽出サンプル（±4〜5pt）で、コンパイル経路も異なります。留意点はレポートに明記しています。

**品質・性能評価** — ウィキが健全で高速か。コンパイル済みウィキならどれでも、API キー不要、数秒で実行できます。実データ 10 ウィキの中央値: 総合 **87.4%**、事実抽出 100%、recall@10 100%、相互参照整合性 100%。プロセス内検索: FTS5 top-10 **0.035 ms**、ハイブリッド RRF **4.9 ms**、グラフ BFS **0.001 ms**。

```bash
python3 eval/eval.py .                      # ウィキの品質と性能
python3 -m pytest eval/eval_test.py -q      # ハーネスの自己テスト
```

## ライセンス

MIT
