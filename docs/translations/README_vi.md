[English](../../README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | [한국어](README_ko.md) | **Tiếng Việt** | [Français](README_fr.md) | [Русский](README_ru.md)

<!-- translations: may-lag -->
> ⚠️ Bản dịch này có thể chưa cập nhật theo README.md — bản tiếng Anh là chuẩn.

# sage-wiki

**sage-wiki** là bộ nhớ đồ thị kiêm cơ sở tri thức mà các agent AI và con người cùng xây dựng và truy vấn. Thả tài liệu vào; một trình biên dịch LLM biến chúng thành một wiki liên kết chéo kèm đồ thị tri thức — agent truy vấn qua MCP, con người duyệt dưới dạng markdown thuần. Bật các pass đồ thị tùy chọn và nó trở thành một đồ thị *có bằng chứng*: thực thể có kiểu, quan hệ mang nguồn gốc, bí danh được phân giải, và trích dẫn theo từng dữ kiện trong câu trả lời. Một tệp nhị phân Go duy nhất mở rộng nó từ vault cá nhân tới hub nhóm tới đồ thị tri thức công ty.

**→ Bắt đầu: [Cài đặt](#cài-đặt) · [Bắt đầu nhanh](#bắt-đầu-nhanh)**

Phát triển từ [ý tưởng của Andrej Karpathy](https://x.com/karpathy/status/2039805659525644595) về một cơ sở tri thức cá nhân được biên dịch bởi LLM, xây dựng bằng [Sage Framework](https://github.com/xoai/sage). Một số bài học rút ra trên chặng đường [tại đây](https://x.com/xoai/status/2040936964799795503).

- **Bộ nhớ đồ thị có trích dẫn.** Đặt câu hỏi quan hệ qua `wiki_graph_query` — câu trả lời chỉ dựa trên các cạnh đồ thị đã được tuần tự hóa; khi bật đồ thị có bằng chứng, mỗi trích dẫn kèm theo tài liệu nguồn và độ tin cậy của nó.
- **Xây dựng cho cả agent và con người.** 19 công cụ MCP cùng các tệp kỹ năng được tạo tự động dạy agent khi nào cần tìm kiếm, ghi nhận, và biên dịch; con người có markdown tương thích Obsidian, một TUI, và một web UI trên cùng một dữ liệu.
- **Tin cậy và nguồn gốc.** Đầu ra truy vấn bị cách ly cho đến khi được xác minh; mỗi quan hệ có bằng chứng đều ghi lại tài liệu nào đã khẳng định nó.
- **Đưa nguồn vào, nhận wiki ra.** Pipeline biên dịch đọc bài báo, ghi chú, mã nguồn, và email; tóm tắt; trích xuất khái niệm; và viết các bài viết liên kết với nhau — lớp tiếp nhận cho mọi thứ ở trên. Mỗi nguồn mới làm phong phú thêm các bài viết hiện có; wiki tích lũy giá trị khi phát triển.
- **Đặt câu hỏi cho wiki của bạn.** Tìm kiếm lai cấp chunk với mở rộng truy vấn LLM, xếp hạng lại, và lắp ráp ngữ cảnh nhận biết đồ thị trả về câu trả lời có trích dẫn.
- **Mở rộng tới 100K+ tài liệu.** Biên dịch phân tầng lập chỉ mục mọi thứ nhanh chóng và chỉ tiêu ngân sách LLM ở nơi thực sự quan trọng.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Các điểm trên đường biên ngoài đại diện cho tóm tắt của tất cả tài liệu trong cơ sở tri thức, trong khi các điểm ở vòng tròn bên trong đại diện cho các khái niệm được trích xuất từ cơ sở tri thức, với các liên kết cho thấy cách các khái niệm kết nối với nhau._

## Từ vault cá nhân tới đồ thị tri thức công ty

- **Cá nhân** — phủ lên một vault Obsidian hiện có (`init --vault`), chạy trên [model cục bộ](../guides/local-models.md) với chi phí bằng không, và bật các pass đồ thị (`ontology.triples` + `ontology.resolve`) khi bạn muốn có đồ thị có bằng chứng.
- **Nhóm** — chia sẻ một wiki qua git hoặc một [máy chủ tự host](../guides/self-hosted-server.md), cùng nhau rà soát các đề xuất phân giải thực thể và [tin cậy đầu ra](../guides/output-trust.md), và liên kết nhiều wiki bằng hub. Xem [Thiết lập nhóm](../guides/team-setup.md).
- **Công ty** — chuyển lưu trữ sang [PostgreSQL/pgvector](../guides/storage-backends.md), bật [số liệu](../guides/metrics.md), đặt lớp xác thực trước máy chủ, và mở rộng tiếp nhận với [biên dịch phân tầng](../guides/large-vault-performance.md).

## Đồ thị tri thức & bộ nhớ đồ thị

![engine đồ thị sage-wiki](../../assets/sage-wiki-graph-engine.png)

Tìm kiếm vector trả về những đoạn *trông giống* câu hỏi. Đồ thị còn lưu **các sự vật liên hệ với nhau ra sao**, nên một câu hỏi cần hai ba bước suy luận được trả lời bằng cách duyệt đồ thị, thay vì hy vọng một đoạn văn chứa trọn chuỗi lập luận. sage-wiki dựng đồ thị đó như một sản phẩm của quá trình biên dịch — không phải một cơ sở dữ liệu thứ hai phải đồng bộ.

- **Thực thể và quan hệ có kiểu.** Mỗi lần biên dịch sẽ trích xuất thực thể (khái niệm, nguồn, hiện vật) và nối chúng bằng quan hệ có kiểu. Bộ từ vựng quan hệ do bạn định nghĩa — xem
  [quan hệ có thể cấu hình](../guides/configurable-relations.md).
- **Cạnh có bằng chứng.** Một quan hệ có thể mang `evidence` (đoạn văn chống lưng), `confidence` (0–1) và `source_doc`, nhờ đó kết luận truy ngược tới đúng câu đã tạo ra cạnh, chứ không chỉ tới cả tài liệu.
- **Bộ ba (triple).** Một lượt xử lý tùy chọn với đầu ra có cấu trúc trích thẳng chủ thể → quan hệ → đối tượng. Phải bật thủ công (`ontology.triples`): nó thêm một lệnh gọi LLM cho mỗi tài liệu, và mặc định không bao giờ tiêu tiền của bạn mà không hỏi.
- **Hợp nhất thực thể.** “K8s” và “Kubernetes” trở thành một nút. Đề xuất mặc định phải qua duyệt chứ không âm thầm gộp.

**Đồ thị là một kênh truy xuất, không phải khung nhìn phụ.** Mỗi lần tìm kiếm hợp nhất ba kênh — từ vựng (BM25), vector và độ gần trên đồ thị: từ khóa truy vấn khơi mào các thực thể, một lượt duyệt có giới hạn xếp hạng vùng lân cận, rồi cả ba hợp nhất theo `search.hybrid_weight_graph`. Ontology rỗng không tốn gì và giữ kết quả giống hệt từng byte.

Hỏi trực tiếp, hoặc để agent làm qua MCP:

```bash
sage-wiki ontology query --entity kubernetes --depth 3 --direction both
sage-wiki provenance "service mesh"    # những nguồn nào sinh ra khái niệm này
```

Các cạnh mang tính lưỡng thời gian (bi-temporal): khi một sự thật bị bác bỏ, cạnh cũ bị vô hiệu thay vì xung đột, câu trả lời mặc định không còn mâu thuẫn, và truy vấn `as_of` trả lời "tháng Giêng chúng ta đã tin điều gì?". Các mâu thuẫn không rõ ràng vẫn lộ ra qua khâu duyệt
[độ tin cậy đầu ra](../guides/output-trust.md). Cho câu hỏi toàn corpus ("các chủ đề chính là gì?"), tính năng phát hiện cộng đồng opt-in (`ontology.communities.enabled`) tạo tóm tắt cộng đồng được cache và trả lời qua `wiki_graph_query` `mode: "global"`. Chi tiết:
[bộ nhớ đồ thị](../guides/graph-memory.md).

## Hướng dẫn

| Hướng dẫn | Mô tả |
|-------|-------------|
| [Lớp bộ nhớ Agent](../guides/agent-memory-layer.md) | Cấu hình MCP, tệp kỹ năng, quy trình ghi nhận, vòng lặp đọc-ghi nhận-tiến hóa |
| [HTTP API](../guides/http-api.md) | Bề mặt REST /v1: xác thực, mô hình lỗi, idempotency, job bất đồng bộ |
| [Bộ nhớ đồ thị](../guides/graph-memory.md) | Quan hệ có bằng chứng, trích xuất bộ ba, phân giải thực thể, hỏi đáp đồ thị |
| [Cấu hình](../guides/configuration.md) | config.yaml đầy đủ có chú giải, cấu hình đa nhà cung cấp, worker của serve |
| [Thiết lập nhóm](../guides/team-setup.md) | Các mẫu triển khai đồng bộ git, máy chủ dùng chung, và liên kết hub |
| [Chất lượng tìm kiếm](../guides/search-quality.md) | Lập chỉ mục chunk, mở rộng truy vấn, xếp hạng lại, mở rộng đồ thị, ANN |
| [Hiệu năng Vault lớn](../guides/large-vault-performance.md) | Biên dịch phân tầng, backpressure, trình phân tích mã, mở rộng 100K+ |
| [Tin cậy đầu ra](../guides/output-trust.md) | Xác minh grounding, đồng thuận, vòng đời thăng/giáng cấp |
| [Xác thực đăng ký](../guides/subscription-auth.md) | Đăng nhập OAuth, nhập token, quản lý thông tin xác thực |
| [Máy chủ tự host](../guides/self-hosted-server.md) | Docker Compose, Syncthing, reverse proxy, triển khai VPS |
| [Backend lưu trữ](../guides/storage-backends.md) | Cài đặt SQLite vs PostgreSQL/pgvector, chuyển đổi, định cỡ pool |
| [Quan hệ có thể cấu hình](../guides/configurable-relations.md) | Loại ontology tùy chỉnh, từ đồng nghĩa đa ngôn ngữ, hạn chế theo loại |
| [Tùy chỉnh Prompt](../guides/customizing-prompts.md) | Khung prompt, ghi đè theo loại, trường frontmatter tùy chỉnh |
| [Model cục bộ](../guides/local-models.md) | Cài đặt Ollama, định tuyến GPU/CPU, cấu hình model theo từng pass |
| [Số liệu](../guides/metrics.md) | Snapshot log, endpoint /metrics, kiểm soát cardinality |
| [Gói đóng góp](../../CONTRIBUTING.md) | Tạo gói, viết parser, gửi lên registry |

## Cài đặt

```bash
# Chỉ CLI (không có web UI)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# Với web UI (yêu cầu Node.js để build tài nguyên frontend)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## Bắt đầu nhanh

![Pipeline trình biên dịch](../../assets/sage-wiki-compiler-pipeline.png)

### Dự án mới (greenfield)

```bash
sage-wiki init my-wiki && cd my-wiki
# Thêm nguồn vào raw/
cp ~/papers/*.pdf raw/
# Chỉnh sửa config.yaml để thêm api key và chọn LLM
sage-wiki compile                                  # biên dịch lần đầu
sage-wiki search "attention mechanism"             # tìm kiếm lai
sage-wiki query "How does flash attention work?"   # hỏi đáp có trích dẫn
sage-wiki tui                                      # bảng điều khiển terminal
sage-wiki serve --ui                               # trình duyệt (build webui)
sage-wiki compile --watch                          # theo dõi thư mục
```

Mọi khóa trong `config.yaml`, được chú giải từng dòng: [Cấu hình](../guides/configuration.md).

**Cấu trúc dự án** (những gì `init` tạo ra — một số mục tiêu biểu, không đầy đủ):

```
my-wiki/
├── config.yaml           # provider, model, compiler, search, ontology
├── raw/                  # thả nguồn vào đây (bài viết, paper, code, ảnh)
├── wiki/                 # kết quả biên dịch — markdown tương thích Obsidian
│   ├── summaries/        # tóm tắt LLM theo nguồn
│   ├── concepts/         # bài viết khái niệm (đồ thị tri thức)
│   ├── images/           # mô tả ảnh bằng vision
│   ├── outputs/          # câu trả lời được lưu (trust.include_outputs: "true")
│   ├── under_review/     # câu trả lời chờ duyệt (mặc định)
│   └── archive/          # bài viết đã prune
├── .sage/wiki.db         # một tệp SQLite: chỉ mục FTS, vector, ontology, queue
└── .manifest.json        # ánh xạ nguồn↔bài viết + trạng thái biên dịch
```

### Lớp phủ Vault (vault Obsidian hiện có)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Chỉnh sửa config.yaml để thiết lập thư mục nguồn/bỏ qua, thêm api key, chọn LLM
sage-wiki compile --watch
```

Thích dùng container? Image Docker đa kiến trúc dựng sẵn và các tệp compose
được trình bày trong [hướng dẫn máy chủ tự host](../guides/self-hosted-server.md).

## Các định dạng nguồn được hỗ trợ

| Định dạng   | Phần mở rộng                            | Nội dung được trích xuất                                    |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | Nội dung văn bản với frontmatter được phân tích riêng       |
| PDF         | `.pdf`                                  | Toàn bộ văn bản qua trích xuất pure-Go                      |
| Word        | `.docx`                                 | Văn bản tài liệu từ XML                                     |
| Excel       | `.xlsx`                                 | Giá trị ô và dữ liệu sheet                                  |
| PowerPoint  | `.pptx`                                 | Nội dung văn bản trên slide                                 |
| CSV         | `.csv`                                  | Tiêu đề + các hàng (tối đa 1000 hàng)                       |
| EPUB        | `.epub`                                 | Văn bản chương từ XHTML                                     |
| Email       | `.eml`                                  | Tiêu đề (from/to/subject/date) + nội dung                   |
| Văn bản thuần | `.txt`, `.log`                        | Nội dung thô                                                |
| Phụ đề      | `.vtt`, `.srt`                          | Nội dung thô                                                |
| Hình ảnh    | `.png`, `.jpg`, `.gif`, `.webp`, `.svg`, `.bmp` | Mô tả qua vision LLM (chú thích, nội dung, văn bản hiển thị) |
| Mã nguồn    | `.go`, `.py`, `.js`, `.ts`, `.rs`, v.v. | Mã nguồn                                                    |

Chỉ cần thả tệp vào thư mục nguồn — sage-wiki tự động phát hiện định dạng. Hình ảnh yêu cầu LLM có khả năng vision (Gemini, Claude, GPT-4o). Cần định dạng không có trong danh sách? sage-wiki hỗ trợ [trình phân tích ngoài](#trình-phân-tích-ngoài) — script bằng bất kỳ ngôn ngữ nào đọc stdin và ghi văn bản ra stdout.

## Bộ nhớ đồ thị

Ngay từ đầu, wiki xây dựng một đồ thị tri thức từ độ lân cận từ khóa —
các khái niệm được liên kết khi từ khóa quan hệ xuất hiện cùng một `[[wikilink]]`
trong cùng một khối. Bật các
**pass đồ thị tùy chọn** để biến nó thành một đồ thị có bằng chứng:

- **Trích xuất bộ ba** (`ontology.triples.enabled`) — một cuộc gọi LLM bổ sung
  cho mỗi tài liệu được biên dịch đầy đủ trích xuất các thực thể và quan hệ
  có kiểu, mỗi mục kèm theo đoạn bằng chứng, độ tin cậy, và tài liệu nguồn.
- **Phân giải thực thể** (`ontology.resolve.enabled`) — các biến thể hình thức
  bề mặt ("NASA" / "National Aeronautics and Space Administration")
  được liên kết về một thực thể chuẩn. Đề xuất có độ tin cậy cao được áp dụng
  tự động (ngưỡng 0.85; đặt đúng `1.0` để chỉ rà soát), và
  mỗi liên kết đều có thể hoàn tác chính xác bằng `ontology resolve --unlink`.
- **Hỏi đáp đồ thị** — công cụ MCP `wiki_graph_query` trả lời các câu hỏi
  quan hệ nhiều bước chỉ dựa *duy nhất* trên một tập cạnh có giới hạn đã được
  tuần tự hóa; trích dẫn kèm `source_doc` và `confidence` khi cạnh có
  bằng chứng (cạnh từ độ lân cận từ khóa không kèm cả hai). Ngữ cảnh hỏi đáp
  thông thường cũng nêu tên cạnh kết nối dưới mỗi bài viết liên quan.

Chi tiết chuyên sâu, chi phí, quy trình rà soát, và ngữ nghĩa hoàn tác: [Bộ nhớ đồ thị](../guides/graph-memory.md).

## Các lệnh

Bề mặt lệnh cốt lõi; chạy `sage-wiki <command> --help` để xem các cờ.

| Lệnh | Mô tả |
| ------- | ----------- |
| `sage-wiki init [dir] [--vault] [--skill <agent>] [--pack <name>] [--prompts] [--force]` | Khởi tạo dự án (greenfield hoặc lớp phủ vault) |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | Biên dịch nguồn thành bài viết wiki |
| `sage-wiki serve [--transport stdio\|sse] [--ui] [--port 3333]` | Máy chủ MCP / web UI |
| `sage-wiki reindex [--drop-chunk-vectors]` | Dựng lại chỉ mục chunk từ các tài liệu trên đĩa với `chunk_size` / `chunk_overlap_tokens` hiện tại |
| `sage-wiki search "query" [--tags ...] [--boost-tags ...] [--limit N] [--channels bm25,vector,graph] [--expand] [--rerank]` | Tìm kiếm lai (BM25 + vector + đồ thị ontology) |
| `sage-wiki query "question"` | Hỏi đáp trên wiki với trích dẫn |
| `sage-wiki tui` | Bảng điều khiển terminal tương tác |
| `sage-wiki ontology <query\|list\|add\|resolve>` | Truy vấn, quản lý, và phân giải đồ thị ontology |
| `sage-wiki ingest <url\|path>` / `sage-wiki add-source <path>` | Thêm nguồn |
| `sage-wiki source <show\|list>` / `sage-wiki coverage` | Kiểm tra nguồn và độ phủ biên dịch |
| `sage-wiki status` / `sage-wiki doctor` / `sage-wiki diff` | Tình trạng, kiểm tra cấu hình, thay đổi đang chờ |
| `sage-wiki lint [--fix]` / `sage-wiki list` / `sage-wiki write <summary\|article>` | Bảo trì và ghi thủ công |
| `sage-wiki hub <init\|add\|remove\|search\|status\|list\|compile>` | Hub đa dự án |
| `sage-wiki learn "text"` / `sage-wiki capture "text"` / `sage-wiki scribe <session-file>` | Ghi nhận tri thức |
| `sage-wiki skill <refresh\|preview> [--target <agent>]` | Tạo hoặc làm mới tệp kỹ năng agent |
| `sage-wiki provenance <source-or-concept>` / `sage-wiki version` | Ánh xạ nguồn gốc, phiên bản |

Các nhóm lệnh theo chủ đề nằm cùng hướng dẫn của chúng: `pack *` trong
[CONTRIBUTING](../../CONTRIBUTING.md), `auth *` (login, import, status, logout,
migrate) trong [Xác thực đăng ký](../guides/subscription-auth.md), và
`verify` / `outputs *` trong [Tin cậy đầu ra](../guides/output-trust.md).

## TUI

```bash
sage-wiki tui
```

Bảng điều khiển terminal đầy đủ tính năng với 4 tab:

- **[F1] Duyệt** — Điều hướng bài viết theo phần (khái niệm, tóm tắt, đầu ra). Phím mũi tên để chọn, Enter để đọc với markdown được render bằng glamour, Esc để quay lại.
- **[F2] Tìm kiếm** — Tìm kiếm mờ (fuzzy) với khung xem trước chia đôi. Gõ để lọc, kết quả xếp hạng theo điểm lai, Enter để mở trong `$EDITOR`.
- **[F3] Hỏi đáp** — Hỏi đáp streaming dạng hội thoại. Đặt câu hỏi, nhận câu trả lời do LLM tổng hợp với trích dẫn nguồn. Ctrl+S lưu câu trả lời vào outputs/.
- **[F4] Biên dịch** — Bảng điều khiển biên dịch trực tiếp. Theo dõi thư mục nguồn để phát hiện thay đổi và tự động biên dịch lại. Duyệt tệp đã biên dịch với bản xem trước.

Chuyển tab: `F1`-`F4` từ bất kỳ tab nào, `1`-`4` trên tab Duyệt/Biên dịch, `Esc` quay lại tab Duyệt. Thoát bằng `Ctrl+C`.

## Web UI

```bash
sage-wiki serve --ui        # http://127.0.0.1:3333, yêu cầu build -tags webui
```

- **Trình duyệt bài viết** với markdown được render, tô sáng cú pháp, và `[[wikilinks]]` có thể nhấp
- **Tìm kiếm lai** với kết quả được xếp hạng và đoạn trích
- **Đồ thị tri thức** — trực quan hóa lực hướng (force-directed) tương tác của các khái niệm và kết nối giữa chúng
- **Hỏi đáp streaming** — đặt câu hỏi và nhận câu trả lời do LLM tổng hợp với trích dẫn nguồn
- **Mục lục** với scroll-spy; chế độ tối/sáng với phát hiện tùy chọn hệ thống; liên kết bài viết hỏng hiển thị màu xám

Xây dựng với Preact + Tailwind, nhúng qua `go:embed` (~1.2 MB, ~420 KB khi nén gzip); bỏ `-tags webui` để có tệp nhị phân chỉ CLI/MCP. Token xác thực, host được phép, và gia cố triển khai: [Máy chủ tự host](../guides/self-hosted-server.md).

## Tích hợp MCP

![Tích hợp MCP](../../assets/sage-wiki-interfaces.png)

Thêm vào `.mcp.json` (Claude Code; các agent khác trong [hướng dẫn Lớp bộ nhớ Agent](../guides/agent-memory-layer.md)):

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

Client mạng: `sage-wiki serve --transport sse --port 3333`. Máy chủ
cung cấp 19 công cụ — tìm kiếm, đọc, truy vấn đồ thị, ghi nhận, `wiki_query`
(trả lời câu hỏi kèm lưu bản để duyệt), biên dịch theo yêu cầu và nhiều hơn nữa; cách thiết lập cho từng agent và quy trình
ghi nhận nằm trong [hướng dẫn Lớp bộ nhớ Agent](../guides/agent-memory-layer.md).

**Tệp kỹ năng agent** — `sage-wiki skill refresh --target <agent>` ghi
một phần kỹ năng hành vi vào tệp hướng dẫn của agent (CLAUDE.md,
.cursorrules, …) dạy nó khi nào cần tìm kiếm, ghi nhận gì, và cách
truy vấn, được suy ra từ cấu hình của bạn. Các target: `claude-code`, `cursor`,
`windsurf`, `agents-md` (Antigravity), `codex`, `gemini`, `generic`.

### Kỹ năng agent

Cài đặt kỹ năng tham chiếu của sage-wiki để trợ lý lập trình biết toàn bộ
bề mặt công cụ — cả 19 công cụ MCP, các tương đương REST `/v1`, cờ opt-in,
các tier, ngữ nghĩa biên dịch bất đồng bộ và mã lỗi — mà không cần đọc
README này:

```bash
# Claude Code
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki

# Hoặc thủ công: sao chép skills/sage-wiki/SKILL.md vào .claude/skills/
```

Kỹ năng pipeline `sage-wiki-integrate` kết nối sage-wiki vào một repo mới một
cách tương tác (phát hiện ngôn ngữ → cài client hoặc cấu hình MCP → smoke
test lưu-và-truy-xuất):

```bash
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki-integrate
```

Cả hai kỹ năng được tạo từ registry MCP trực tiếp (`go run ./tools/skillgen/`)
và được kiểm tra trôi lệch trong CI — chúng không thể lỗi thời khi công cụ
thay đổi. Pre-1.0 — hãy ghim một phiên bản.

**Ghi nhận tri thức** — agent lưu lại các phát hiện qua `wiki_capture` /
`wiki_learn`, khép kín vòng lặp đọc-ghi nhận-tiến hóa. Quy trình và mẹo:
[Lớp bộ nhớ Agent](../guides/agent-memory-layer.md).

## Client SDK

Client có kiểu cho REST API `/v1` (pre-1.0 — hãy ghim một phiên bản):

**Python** — `pip install sagewiki` (≥3.9, chỉ `httpx`):

```python
from sagewiki import SageWiki

c = SageWiki()  # SAGE_WIKI_URL / SAGE_WIKI_TOKEN từ env
for r in c.search("attention", limit=5).results:
    print(r.final_score, r.content[:80])
job = c.compile(topic="attention")
job.wait(timeout=600)  # bắt buộc timeout tường minh
```

**TypeScript** — `npm install sagewiki` (không phụ thuộc runtime, `fetch`
toàn cục; Node ≥18, Deno, Bun, edge runtime):

```ts
import { SageWikiClient } from "sagewiki";

const c = new SageWikiClient();
const results = await c.search("attention", { limit: 5 });
const job = await c.compile({ topic: "attention" });
await job.waitUntilDone({ timeoutMs: 600_000 });
```

Cả hai client bao phủ toàn bộ bề mặt `/v1`: tìm kiếm, provenance, truy vấn
đồ thị, wiki đã biên dịch, capture/ghi, và các job compile/lint bất đồng bộ
với phân loại lỗi theo mã. Tài liệu: [Python](../../clients/python/README.md) ·
[TypeScript](../../clients/typescript/README.md) ·
[hướng dẫn HTTP API](../guides/http-api.md). Chương trình Go có thể bỏ qua
HTTP hoàn toàn — xem [Nhúng vào chương trình Go](#nhúng-vào-chương-trình-go).

### Ví dụ

Các tích hợp framework có thể sao chép, được chạy trong CI với server thật:

- [`examples/langgraph/`](../../examples/langgraph/) — các node LangGraph có bộ
  nhớ (client Python): truy xuất với pattern `uncompiled_sources` →
  compile theo chủ đề, cùng capture.
- [`examples/vercel-ai-sdk/`](../../examples/vercel-ai-sdk/) — `search`,
  `graphQuery`, `provenance` dưới dạng tool Vercel AI SDK (client
  TypeScript); triển khai được trên edge.

### Nhúng vào chương trình Go

Để gọi các công cụ từ chính tiến trình Go của bạn — không cần tiến trình con, không cần quản lý stdio hay cổng — hãy dùng `pkg/sagewiki` với transport in-process của mcp-go:

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

Dự án phải tồn tại sẵn và phía gọi sở hữu handle cơ sở dữ liệu, nên bắt buộc phải `Close` — khác với `serve`, không có gì khác đóng nó. Log ghi ra stderr của host, và `initialize` báo phiên bản build của sage-wiki (`dev` với `go build` thuần); gọi `sagewiki.SetVersion` khi khởi động để báo chuỗi phiên bản của riêng bạn.

Package này **thử nghiệm** trong khi sage-wiki chưa đạt 1.0: các chữ ký Go dự kiến giữ nguyên, nhưng tên công cụ, schema đối số và cấu trúc `config.yaml` có thể thay đổi ở mỗi bản phát hành. Hãy ghim một phiên bản.

## Vận hành

- **Lưu trữ** — SQLite theo mặc định (một tệp duy nhất, không cần cấu hình); PostgreSQL +
  pgvector cho triển khai máy chủ. Chuyển đổi và định cỡ pool: [Backend lưu trữ](../guides/storage-backends.md).
- **Khả năng quan sát** — snapshot log có cấu trúc và endpoint `/metrics`
  tùy chọn: [Số liệu](../guides/metrics.md).
- **Đầu ra có cấu trúc** — các pass trích xuất LLM dùng cơ chế riêng của
  từng nhà cung cấp (Anthropic tool-use, OpenAI `response_format`, Gemini
  `responseSchema`) với phương án dự phòng tách fence có kiểm tra.
- **Thông tin xác thực** — token đăng ký nằm trong keychain của hệ điều hành
  khi khả dụng; chạy `sage-wiki auth migrate` một lần để chuyển thông tin
  xác thực lưu trong tệp sang. [Xác thực đăng ký](../guides/subscription-auth.md).
- **Cấu hình** — mọi khóa, có chú giải, kèm công thức đa nhà cung cấp
  và compile worker ở chế độ serve: [Cấu hình](../guides/configuration.md).
- **Phân giải thực thể** — tự động áp dụng ở ngưỡng 0.85, hoàn tác chính xác bằng `--unlink`; xem [Bộ nhớ đồ thị](#bộ-nhớ-đồ-thị) ở trên.
- **Loại quan hệ/thực thể tùy chỉnh** — mở rộng loại tích hợp hoặc thêm loại của riêng bạn
  (`ontology.relation_types`), với từ đồng nghĩa đa ngôn ngữ và hạn chế
  theo loại: [Quan hệ có thể cấu hình](../guides/configurable-relations.md).
- **Tin cậy đầu ra** — đầu ra truy vấn bị cách ly cho đến khi được xác minh grounding,
  xác nhận bằng đồng thuận, hoặc thăng cấp thủ công: [Tin cậy đầu ra](../guides/output-trust.md).
- **Tinh chỉnh tìm kiếm** — chia chunk, mở rộng truy vấn, xếp hạng lại, mở rộng đồ thị,
  và ANN tùy chọn: [Chất lượng tìm kiếm](../guides/search-quality.md).

### Chi phí

sage-wiki theo dõi lượng token sử dụng và ước tính chi phí cho mỗi lần biên dịch.
**Cache prompt** (mặc định bật) tái sử dụng prompt hệ thống giữa các cuộc gọi
trong một pass biên dịch — Anthropic và Gemini cache tường minh, OpenAI cache
tự động — tiết kiệm 50-90% token đầu vào. **Batch API**
(Anthropic, OpenAI, và Gemini) giảm một nửa chi phí cho các lần biên dịch lớn:

```bash
sage-wiki compile --batch       # gửi batch, lưu checkpoint, thoát
sage-wiki compile               # kiểm tra trạng thái, nhận khi xong
```

`compile --estimate` xem trước chi phí; `compiler.mode: auto` tự động dùng batch
khi vượt ngưỡng. Chi tiết: [Cấu hình](../guides/configuration.md).

### Mở rộng cho vault lớn

Biên dịch phân tầng định tuyến từng nguồn theo loại và mức sử dụng thay vì
biên dịch mọi thứ bằng LLM:

| Tầng | Điều gì diễn ra | Chi phí | Thời gian mỗi tài liệu |
|------|-------------|------|-------------|
| **0** — Chỉ lập chỉ mục | Tìm kiếm toàn văn FTS5 | Miễn phí | ~5ms |
| **1** — Chỉ mục + embed | FTS5 + vector embedding | ~$0.00002 | ~200ms |
| **2** — Phân tích mã | Tóm tắt cấu trúc qua regex parser (không LLM) | Miễn phí | ~10ms |
| **3** — Biên dịch đầy đủ | Tóm tắt + trích xuất khái niệm + viết bài | ~$0.05-0.15 | ~5-8 phút |

Với vault lớn: lập chỉ mục mọi thứ ở Tầng 1 (một vault 100K tài liệu trong ~5.5
giờ), rồi biên dịch theo yêu cầu — tự động thăng cấp, backpressure, và trình phân tích mã được trình bày trong
[Hiệu năng Vault lớn](../guides/large-vault-performance.md).

## Hệ sinh thái

### Gói đóng góp

Gói đóng gói các loại ontology, prompt, và trigger kỹ năng cho một lĩnh vực.
Tám gói đi kèm hoạt động offline:

| Gói | Đối tượng | Ontology chính |
|------|----------|-------------|
| `academic-research` | Nhà nghiên cứu | cites, contradicts, finding, research_hypothesis |
| `software-engineering` | Đội phát triển | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | Người ghi chú | relates_to, inspired_by, fleeting_note |
| `study-group` | Sinh viên | explains, prerequisite_of, definition |
| `meeting-organizer` | Quản lý | decided, assigned_to, action_item |
| `content-creation` | Người viết | references, revises, draft, published |
| `legal-compliance` | Đội pháp lý | regulates, supersedes, policy, control |

`sage-wiki init --pack academic-research` áp dụng một gói lúc khởi tạo;
`pack install <name|url>` thêm gói khác. Tạo và xuất bản gói:
[CONTRIBUTING](../../CONTRIBUTING.md).

### Trình phân tích ngoài

Xử lý bất kỳ định dạng tệp nào bằng một script viết bằng bất kỳ ngôn ngữ nào
(stdin → văn bản ra stdout), khai báo trong `parsers/parser.yaml` sau hai lớp
opt-in — chúng chạy dưới dạng subprocess không sandbox với cưỡng chế timeout và
loại bỏ biến môi trường. Chi tiết viết parser và gia cố: [CONTRIBUTING](../../CONTRIBUTING.md);
thảo luận về ranh giới tin cậy: [Thiết lập nhóm](../guides/team-setup.md).

### Nhóm

Ba mẫu chia sẻ — đồng bộ git, máy chủ dùng chung, liên kết hub — cộng thêm
rà soát tin cậy theo nhóm và quản lý chi phí: [Thiết lập nhóm](../guides/team-setup.md).

## Đánh giá hiệu năng

Hai bộ đánh giá trả lời hai câu hỏi khác nhau. Chi tiết:
[eval/benchmarks/REPORT.md](../../eval/benchmarks/REPORT.md) · [eval/REPORT.md](../../eval/REPORT.md)

**Đánh giá bộ nhớ** — nó có trả lời được câu hỏi về một cuộc hội thoại dài không? Dữ liệu công bố, chấm bằng LLM, dùng đúng prompt và quy trình của
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks) với sage-wiki làm backend (gpt-5 vừa trả lời vừa chấm, mẫu rút gọn):

| Bộ đánh giá | Score | Mem0 Platform |
|---|---|---|
| LOCOMO (150 q) | **92.0%** @ top-50 | 91.8% @ top-50 |
| LongMemEval-S (30 q) | **93.3%** @ top-50 | 94.8% @ top-50 |
| BEAM 100K (60 q) | **0.691** mean nugget | 0.641 @ 1M |

Đây không phải so sánh ngang hàng: mem0 chạy nền tảng có quản lý trên toàn bộ câu hỏi, còn đây là mẫu rút gọn (±4–5 điểm phần trăm), và pipeline biên dịch cũng khác. Các lưu ý được nêu rõ trong báo cáo.

**Đánh giá chất lượng + hiệu năng** — wiki có chuẩn và nhanh không? Chạy trên bất kỳ wiki đã biên dịch nào, không cần API key, chỉ vài giây. Trung vị trên 10 wiki thật: tổng thể **87,4%**, trích xuất dữ kiện 100%, recall@10 100%, toàn vẹn liên kết chéo 100%. Truy xuất trong tiến trình: FTS5 top-10 **0,035 ms**, RRF lai **4,9 ms**, BFS đồ thị **0,001 ms**.

```bash
python3 eval/eval.py .                      # chất lượng + hiệu năng wiki
python3 -m pytest eval/eval_test.py -q      # tự kiểm thử bộ công cụ
```

## Giấy phép

MIT
