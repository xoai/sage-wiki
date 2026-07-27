[English](README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | [한국어](README_ko.md) | **Tiếng Việt** | [Français](README_fr.md) | [Русский](README_ru.md)

<!-- translations: may-lag -->
> ⚠️ Bản dịch này có thể chưa cập nhật theo README.md — bản tiếng Anh là chuẩn.

# sage-wiki

**sage-wiki** là bộ nhớ đồ thị kiêm cơ sở tri thức mà các agent AI và con người cùng xây dựng và truy vấn. Thả tài liệu vào; một trình biên dịch LLM biến chúng thành một wiki liên kết chéo kèm đồ thị tri thức — agent truy vấn qua MCP, con người duyệt dưới dạng markdown thuần. Bật các pass đồ thị tùy chọn và nó trở thành một đồ thị *có bằng chứng*: thực thể có kiểu, quan hệ mang nguồn gốc, bí danh được phân giải, và trích dẫn theo từng dữ kiện trong câu trả lời. Một tệp nhị phân Go duy nhất mở rộng nó từ vault cá nhân tới hub nhóm tới đồ thị tri thức công ty.

**→ Bắt đầu: [Cài đặt](#cài-đặt) · [Bắt đầu nhanh](#bắt-đầu-nhanh)**

Phát triển từ [ý tưởng của Andrej Karpathy](https://x.com/karpathy/status/2039805659525644595) về một cơ sở tri thức cá nhân được biên dịch bởi LLM, xây dựng bằng [Sage Framework](https://github.com/xoai/sage). Một số bài học rút ra trên chặng đường [tại đây](https://x.com/xoai/status/2040936964799795503).

- **Bộ nhớ đồ thị có trích dẫn.** Đặt câu hỏi quan hệ qua `wiki_graph_query` — câu trả lời chỉ dựa trên các cạnh đồ thị đã được tuần tự hóa; khi bật đồ thị có bằng chứng, mỗi trích dẫn kèm theo tài liệu nguồn và độ tin cậy của nó.
- **Xây dựng cho cả agent và con người.** 18 công cụ MCP cùng các tệp kỹ năng được tạo tự động dạy agent khi nào cần tìm kiếm, ghi nhận, và biên dịch; con người có markdown tương thích Obsidian, một TUI, và một web UI trên cùng một dữ liệu.
- **Tin cậy và nguồn gốc.** Đầu ra truy vấn bị cách ly cho đến khi được xác minh; mỗi quan hệ có bằng chứng đều ghi lại tài liệu nào đã khẳng định nó.
- **Đưa nguồn vào, nhận wiki ra.** Pipeline biên dịch đọc bài báo, ghi chú, mã nguồn, và email; tóm tắt; trích xuất khái niệm; và viết các bài viết liên kết với nhau — lớp tiếp nhận cho mọi thứ ở trên. Mỗi nguồn mới làm phong phú thêm các bài viết hiện có; wiki tích lũy giá trị khi phát triển.
- **Đặt câu hỏi cho wiki của bạn.** Tìm kiếm lai cấp chunk với mở rộng truy vấn LLM, xếp hạng lại, và lắp ráp ngữ cảnh nhận biết đồ thị trả về câu trả lời có trích dẫn.
- **Mở rộng tới 100K+ tài liệu.** Biên dịch phân tầng lập chỉ mục mọi thứ nhanh chóng và chỉ tiêu ngân sách LLM ở nơi thực sự quan trọng.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Các điểm trên đường biên ngoài đại diện cho tóm tắt của tất cả tài liệu trong cơ sở tri thức, trong khi các điểm ở vòng tròn bên trong đại diện cho các khái niệm được trích xuất từ cơ sở tri thức, với các liên kết cho thấy cách các khái niệm kết nối với nhau._

## Từ vault cá nhân tới đồ thị tri thức công ty

- **Cá nhân** — phủ lên một vault Obsidian hiện có (`init --vault`), chạy trên [model cục bộ](docs/guides/local-models.md) với chi phí bằng không, và bật các pass đồ thị (`ontology.triples` + `ontology.resolve`) khi bạn muốn có đồ thị có bằng chứng.
- **Nhóm** — chia sẻ một wiki qua git hoặc một [máy chủ tự host](docs/guides/self-hosted-server.md), cùng nhau rà soát các đề xuất phân giải thực thể và [tin cậy đầu ra](docs/guides/output-trust.md), và liên kết nhiều wiki bằng hub. Xem [Thiết lập nhóm](docs/guides/team-setup.md).
- **Công ty** — chuyển lưu trữ sang [PostgreSQL/pgvector](docs/guides/storage-backends.md), bật [số liệu](docs/guides/metrics.md), đặt lớp xác thực trước máy chủ, và mở rộng tiếp nhận với [biên dịch phân tầng](docs/guides/large-vault-performance.md).

## Hướng dẫn

| Hướng dẫn | Mô tả |
|-------|-------------|
| [Lớp bộ nhớ Agent](docs/guides/agent-memory-layer.md) | Cấu hình MCP, tệp kỹ năng, quy trình ghi nhận, vòng lặp đọc-ghi nhận-tiến hóa |
| [Bộ nhớ đồ thị](docs/guides/graph-memory.md) | Quan hệ có bằng chứng, trích xuất bộ ba, phân giải thực thể, hỏi đáp đồ thị |
| [Cấu hình](docs/guides/configuration.md) | config.yaml đầy đủ có chú giải, cấu hình đa nhà cung cấp, worker của serve |
| [Thiết lập nhóm](docs/guides/team-setup.md) | Các mẫu triển khai đồng bộ git, máy chủ dùng chung, và liên kết hub |
| [Chất lượng tìm kiếm](docs/guides/search-quality.md) | Lập chỉ mục chunk, mở rộng truy vấn, xếp hạng lại, mở rộng đồ thị, ANN |
| [Hiệu năng Vault lớn](docs/guides/large-vault-performance.md) | Biên dịch phân tầng, backpressure, trình phân tích mã, mở rộng 100K+ |
| [Tin cậy đầu ra](docs/guides/output-trust.md) | Xác minh grounding, đồng thuận, vòng đời thăng/giáng cấp |
| [Xác thực đăng ký](docs/guides/subscription-auth.md) | Đăng nhập OAuth, nhập token, quản lý thông tin xác thực |
| [Máy chủ tự host](docs/guides/self-hosted-server.md) | Docker Compose, Syncthing, reverse proxy, triển khai VPS |
| [Backend lưu trữ](docs/guides/storage-backends.md) | Cài đặt SQLite vs PostgreSQL/pgvector, chuyển đổi, định cỡ pool |
| [Quan hệ có thể cấu hình](docs/guides/configurable-relations.md) | Loại ontology tùy chỉnh, từ đồng nghĩa đa ngôn ngữ, hạn chế theo loại |
| [Tùy chỉnh Prompt](docs/guides/customizing-prompts.md) | Khung prompt, ghi đè theo loại, trường frontmatter tùy chỉnh |
| [Model cục bộ](docs/guides/local-models.md) | Cài đặt Ollama, định tuyến GPU/CPU, cấu hình model theo từng pass |
| [Số liệu](docs/guides/metrics.md) | Snapshot log, endpoint /metrics, kiểm soát cardinality |
| [Gói đóng góp](CONTRIBUTING.md) | Tạo gói, viết parser, gửi lên registry |

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

![Pipeline trình biên dịch](sage-wiki-compiler-pipeline.png)

### Dự án mới (greenfield)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
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

Mọi khóa trong `config.yaml`, được chú giải từng dòng: [Cấu hình](docs/guides/configuration.md).

### Lớp phủ Vault (vault Obsidian hiện có)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Chỉnh sửa config.yaml để thiết lập thư mục nguồn/bỏ qua, thêm api key, chọn LLM
sage-wiki compile --watch
```

Thích dùng container? Image Docker đa kiến trúc dựng sẵn và các tệp compose
được trình bày trong [hướng dẫn máy chủ tự host](docs/guides/self-hosted-server.md).

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

Chi tiết chuyên sâu, chi phí, quy trình rà soát, và ngữ nghĩa hoàn tác: [Bộ nhớ đồ thị](docs/guides/graph-memory.md).

## Các lệnh

Bề mặt lệnh cốt lõi; chạy `sage-wiki <command> --help` để xem các cờ.

| Lệnh | Mô tả |
| ------- | ----------- |
| `sage-wiki init [--vault] [--skill <agent>] [--pack <name>] [--prompts]` | Khởi tạo dự án (greenfield hoặc lớp phủ vault) |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | Biên dịch nguồn thành bài viết wiki |
| `sage-wiki serve [--transport stdio\|sse] [--ui] [--port 3333]` | Máy chủ MCP / web UI |
| `sage-wiki search "query" [--tags ...]` | Tìm kiếm lai (BM25 + vector) |
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
[CONTRIBUTING](CONTRIBUTING.md), `auth *` (login, import, status, logout,
migrate) trong [Xác thực đăng ký](docs/guides/subscription-auth.md), và
`verify` / `outputs *` trong [Tin cậy đầu ra](docs/guides/output-trust.md).

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

Xây dựng với Preact + Tailwind, nhúng qua `go:embed` (~1.2 MB, ~420 KB khi nén gzip); bỏ `-tags webui` để có tệp nhị phân chỉ CLI/MCP. Token xác thực, host được phép, và gia cố triển khai: [Máy chủ tự host](docs/guides/self-hosted-server.md).

## Tích hợp MCP

![Tích hợp MCP](sage-wiki-interfaces.png)

Thêm vào `.mcp.json` (Claude Code; các agent khác trong [hướng dẫn Lớp bộ nhớ Agent](docs/guides/agent-memory-layer.md)):

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

Client mạng: `sage-wiki serve --transport sse --port 3333`. Máy chủ
cung cấp 18 công cụ — tìm kiếm, đọc, truy vấn đồ thị, ghi nhận, biên dịch
theo yêu cầu và nhiều hơn nữa; cách thiết lập cho từng agent và quy trình
ghi nhận nằm trong [hướng dẫn Lớp bộ nhớ Agent](docs/guides/agent-memory-layer.md).

**Tệp kỹ năng agent** — `sage-wiki skill refresh --target <agent>` ghi
một phần kỹ năng hành vi vào tệp hướng dẫn của agent (CLAUDE.md,
.cursorrules, …) dạy nó khi nào cần tìm kiếm, ghi nhận gì, và cách
truy vấn, được suy ra từ cấu hình của bạn. Các target: `claude-code`, `cursor`,
`windsurf`, `agents-md` (Antigravity), `codex`, `gemini`, `generic`.

**Ghi nhận tri thức** — agent lưu lại các phát hiện qua `wiki_capture` /
`wiki_learn`, khép kín vòng lặp đọc-ghi nhận-tiến hóa. Quy trình và mẹo:
[Lớp bộ nhớ Agent](docs/guides/agent-memory-layer.md).

## Vận hành

- **Lưu trữ** — SQLite theo mặc định (một tệp duy nhất, không cần cấu hình); PostgreSQL +
  pgvector cho triển khai máy chủ. Chuyển đổi và định cỡ pool: [Backend lưu trữ](docs/guides/storage-backends.md).
- **Khả năng quan sát** — snapshot log có cấu trúc và endpoint `/metrics`
  tùy chọn: [Số liệu](docs/guides/metrics.md).
- **Đầu ra có cấu trúc** — các pass trích xuất LLM dùng cơ chế riêng của
  từng nhà cung cấp (Anthropic tool-use, OpenAI `response_format`, Gemini
  `responseSchema`) với phương án dự phòng tách fence có kiểm tra.
- **Thông tin xác thực** — token đăng ký nằm trong keychain của hệ điều hành
  khi khả dụng; chạy `sage-wiki auth migrate` một lần để chuyển thông tin
  xác thực lưu trong tệp sang. [Xác thực đăng ký](docs/guides/subscription-auth.md).
- **Cấu hình** — mọi khóa, có chú giải, kèm công thức đa nhà cung cấp
  và compile worker ở chế độ serve: [Cấu hình](docs/guides/configuration.md).
- **Phân giải thực thể** — tự động áp dụng ở ngưỡng 0.85, hoàn tác chính xác bằng `--unlink`; xem [Bộ nhớ đồ thị](#bộ-nhớ-đồ-thị) ở trên.
- **Loại quan hệ/thực thể tùy chỉnh** — mở rộng loại tích hợp hoặc thêm loại của riêng bạn
  (`ontology.relation_types`), với từ đồng nghĩa đa ngôn ngữ và hạn chế
  theo loại: [Quan hệ có thể cấu hình](docs/guides/configurable-relations.md).
- **Tin cậy đầu ra** — đầu ra truy vấn bị cách ly cho đến khi được xác minh grounding,
  xác nhận bằng đồng thuận, hoặc thăng cấp thủ công: [Tin cậy đầu ra](docs/guides/output-trust.md).
- **Tinh chỉnh tìm kiếm** — chia chunk, mở rộng truy vấn, xếp hạng lại, mở rộng đồ thị,
  và ANN tùy chọn: [Chất lượng tìm kiếm](docs/guides/search-quality.md).

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
khi vượt ngưỡng. Chi tiết: [Cấu hình](docs/guides/configuration.md).

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
[Hiệu năng Vault lớn](docs/guides/large-vault-performance.md).

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
[CONTRIBUTING](CONTRIBUTING.md).

### Trình phân tích ngoài

Xử lý bất kỳ định dạng tệp nào bằng một script viết bằng bất kỳ ngôn ngữ nào
(stdin → văn bản ra stdout), khai báo trong `parsers/parser.yaml` sau hai lớp
opt-in — chúng chạy dưới dạng subprocess không sandbox với cưỡng chế timeout và
loại bỏ biến môi trường. Chi tiết viết parser và gia cố: [CONTRIBUTING](CONTRIBUTING.md);
thảo luận về ranh giới tin cậy: [Thiết lập nhóm](docs/guides/team-setup.md).

### Nhóm

Ba mẫu chia sẻ — đồng bộ git, máy chủ dùng chung, liên kết hub — cộng thêm
rà soát tin cậy theo nhóm và quản lý chi phí: [Thiết lập nhóm](docs/guides/team-setup.md).

## Đánh giá hiệu năng

Đánh giá hiện tại ([eval/REPORT.md](eval/REPORT.md), tháng 4 năm 2026): điểm
chất lượng tổng thể **85.9–86.7%** (tổng hợp từ các chỉ số tìm kiếm, trích xuất,
trích dẫn, và toàn vẹn đồ thị), recall@1 tìm kiếm **97.5–99.7%**, recall@10
100% trên bộ benchmark tổng hợp. Chi phí biên dịch không dùng LLM (băm +
phân tích phụ thuộc) luôn dưới một giây — thời gian thực tế bị chi phối bởi
các cuộc gọi API LLM. Tái tạo bằng bộ công cụ trong
[eval/](eval/README.md):

```bash
python3 eval/eval.py .               # đánh giá đầy đủ trên wiki của bạn
python3 -m unittest discover eval    # kiểm thử tự thân của bộ công cụ
```

## Kiến trúc

![Kiến trúc Sage-Wiki](sage-wiki-architecture.png)

- **Lưu trữ:** SQLite với FTS5 (tìm kiếm BM25) + vector BLOB (cosine similarity) + bảng compile_items để theo dõi tầng/trạng thái theo từng nguồn
- **Ontology:** Đồ thị thực thể-quan hệ có kiểu với duyệt BFS và phát hiện chu trình
- **Tìm kiếm:** Pipeline nâng cao với FTS5 cấp chunk + lập chỉ mục vector, mở rộng truy vấn LLM, xếp hạng lại bằng LLM, kết hợp RRF, và mở rộng đồ thị 4 tín hiệu. Phản hồi tìm kiếm báo hiệu nguồn chưa biên dịch để biên dịch theo yêu cầu.
- **Trình biên dịch:** Pipeline phân tầng (Tầng 0: chỉ mục, Tầng 1: embed, Tầng 2: phân tích mã, Tầng 3: biên dịch LLM đầy đủ) với backpressure thích ứng, trích xuất Pass 2 đồng thời, cache prompt, Batch API (Anthropic + OpenAI + Gemini), theo dõi chi phí, biên dịch theo yêu cầu qua MCP, chấm điểm chất lượng, và nhận biết cascade. Embedding bao gồm thử lại với backoff lũy thừa, giới hạn tốc độ tùy chọn, và mean-pooling cho đầu vào dài. 10 trình phân tích mã tích hợp (Go qua go/ast, 8 ngôn ngữ qua regex, trích xuất khóa dữ liệu có cấu trúc).
- **MCP:** 18 công cụ (7 đọc, 9 ghi, 2 kết hợp) qua stdio hoặc SSE, bao gồm `wiki_graph_query` cho hỏi đáp đồ thị nhiều bước có trích dẫn nguồn gốc, `wiki_compile_topic` cho biên dịch theo yêu cầu và `wiki_capture` cho trích xuất tri thức
- **TUI:** Bảng điều khiển terminal 4 tab bằng bubbletea + glamour (duyệt, tìm kiếm, hỏi đáp, biên dịch) với hiển thị phân bố tầng
- **Web UI:** Preact + Tailwind CSS nhúng qua `go:embed` với build tag (`-tags webui`)
- **Scribe:** Giao diện có thể mở rộng để tiếp nhận tri thức từ hội thoại. Session scribe xử lý bản ghi JSONL của Claude Code.
- **Gói:** Hệ thống gói đóng góp với 8 gói đi kèm, registry dựa trên Git, vòng đời cài đặt/áp dụng/gỡ/cập nhật, áp dụng có tính giao dịch với rollback bằng snapshot, hợp nhất chỉ-điền (fill-only), và bảo mật allowlist cấu hình.
- **Trình phân tích ngoài:** Trình phân tích định dạng tệp có thể cắm lúc chạy qua giao thức subprocess stdin/stdout. Thực thi sandbox với timeout, loại bỏ biến môi trường, và cách ly mạng (Linux).

Không CGO. Thuần Go. Đa nền tảng.

## Giấy phép

MIT
