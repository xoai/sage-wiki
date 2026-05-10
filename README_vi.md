[English](README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | [한국어](README_ko.md) | **Tiếng Việt** | [Français](README_fr.md) | [Русский](README_ru.md)

# sage-wiki

Một triển khai dựa trên [ý tưởng của Andrej Karpathy](https://x.com/karpathy/status/2039805659525644595) về cơ sở tri thức cá nhân được biên dịch bởi LLM. Phát triển bằng [Sage Framework](https://github.com/xoai/sage).

Một số bài học rút ra sau khi xây dựng sage-wiki [tại đây](https://x.com/xoai/status/2040936964799795503).

Đưa vào các bài báo, bài viết và ghi chú của bạn. sage-wiki biên dịch chúng thành một wiki có cấu trúc, liên kết chéo — với các khái niệm được trích xuất, tham chiếu chéo được phát hiện, và tất cả đều có thể tìm kiếm được.

- **Đưa nguồn vào, nhận wiki ra.** Thêm tài liệu vào một thư mục. LLM đọc, tóm tắt, trích xuất khái niệm, và viết các bài viết liên kết với nhau.
- **Mở rộng tới 100K+ tài liệu.** Biên dịch phân tầng lập chỉ mục mọi thứ nhanh chóng, chỉ biên dịch những gì quan trọng. Một vault 100K có thể tìm kiếm được trong vài giờ, không phải vài tháng.
- **Tri thức tích lũy.** Mỗi nguồn mới làm phong phú thêm các bài viết hiện có. Wiki ngày càng thông minh hơn khi phát triển.
- **Hoạt động với các công cụ của bạn.** Mở trực tiếp trong Obsidian. Kết nối với bất kỳ agent LLM nào qua MCP. Chạy dưới dạng một tệp nhị phân duy nhất — hoạt động với API key hoặc gói đăng ký LLM hiện có của bạn.
- **Hỏi wiki của bạn.** Tìm kiếm nâng cao với lập chỉ mục cấp chunk, mở rộng truy vấn LLM, và xếp hạng lại. Đặt câu hỏi bằng ngôn ngữ tự nhiên và nhận câu trả lời có trích dẫn nguồn.
- **Biên dịch theo yêu cầu.** Agent có thể kích hoạt biên dịch cho các chủ đề cụ thể qua MCP. Kết quả tìm kiếm báo hiệu khi có nguồn chưa được biên dịch.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Các điểm trên đường biên ngoài đại diện cho tóm tắt của tất cả tài liệu trong cơ sở tri thức, trong khi các điểm ở vòng tròn bên trong đại diện cho các khái niệm được trích xuất từ cơ sở tri thức, với các liên kết cho thấy cách các khái niệm kết nối với nhau._

## Hướng dẫn

| Hướng dẫn | Mô tả |
|-----------|-------|
| [Agent Memory Layer](docs/guides/agent-memory-layer.md) | Cấu hình MCP, file kỹ năng, quy trình thu thập |
| [Team Setup](docs/guides/team-setup.md) | Đồng bộ Git, server dùng chung, liên kết hub |
| [Contribution Packs](CONTRIBUTING.md) | Tạo gói, phát triển parser, đăng ký registry |
| [Large Vault Performance](docs/guides/large-vault-performance.md) | Biên dịch phân tầng, backpressure, mở rộng 100K+ |
| [Search Quality](docs/guides/search-quality.md) | Lập chỉ mục chunk, mở rộng truy vấn, xếp hạng lại |
| [Output Trust](docs/guides/output-trust.md) | Xác minh grounding, đồng thuận, thăng/giáng cấp |
| [Subscription Auth](docs/guides/subscription-auth.md) | Đăng nhập OAuth, nhập token, quản lý thông tin xác thực |
| [Self-Hosted Server](docs/guides/self-hosted-server.md) | Docker Compose, Syncthing, reverse proxy |
| [Configurable Relations](docs/guides/configurable-relations.md) | Ontology tùy chỉnh, từ đồng nghĩa đa ngôn ngữ |
| [Local Models](docs/guides/local-models.md) | Cài đặt Ollama, định tuyến GPU/CPU |

## Cài đặt

```bash
# Chỉ CLI (không có web UI)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# Với web UI (yêu cầu Node.js để build tài nguyên frontend)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## Các định dạng nguồn được hỗ trợ

| Định dạng   | Phần mở rộng                            | Nội dung được trích xuất                                           |
| ----------- | --------------------------------------- | ------------------------------------------------------------------ |
| Markdown    | `.md`                                   | Nội dung văn bản với frontmatter được phân tích riêng              |
| PDF         | `.pdf`                                  | Toàn bộ văn bản qua trích xuất pure-Go                            |
| Word        | `.docx`                                 | Văn bản tài liệu từ XML                                           |
| Excel       | `.xlsx`                                 | Giá trị ô và dữ liệu sheet                                       |
| PowerPoint  | `.pptx`                                 | Nội dung văn bản trên slide                                       |
| CSV         | `.csv`                                  | Tiêu đề + các hàng (tối đa 1000 hàng)                             |
| EPUB        | `.epub`                                 | Văn bản chương từ XHTML                                           |
| Email       | `.eml`                                  | Tiêu đề (from/to/subject/date) + nội dung                         |
| Văn bản thuần| `.txt`, `.log`                          | Nội dung thô                                                      |
| Phụ đề      | `.vtt`, `.srt`                          | Nội dung thô                                                      |
| Hình ảnh    | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | Mô tả qua vision LLM (chú thích, nội dung, văn bản hiển thị)     |
| Mã nguồn    | `.go`, `.py`, `.js`, `.ts`, `.rs`, v.v. | Mã nguồn                                                          |

Chỉ cần thả tệp vào thư mục nguồn — sage-wiki tự động phát hiện định dạng. Hình ảnh yêu cầu LLM có khả năng vision (Gemini, Claude, GPT-4o).

Cần định dạng không có trong danh sách? sage-wiki hỗ trợ **trình phân tích ngoài** — script bằng bất kỳ ngôn ngữ nào đọc stdin và ghi văn bản thuần ra stdout. Xem [Trình phân tích ngoài](#trình-phân-tích-ngoài) bên dưới.

## Bắt đầu nhanh

![Compiler Pipeline](sage-wiki-compiler-pipeline.png)

### Dự án mới (greenfield)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# Thêm nguồn vào raw/
cp ~/papers/*.pdf raw/papers/
cp ~/articles/*.md raw/articles/
# Chỉnh sửa config.yaml để thêm api key và chọn LLM
# Biên dịch lần đầu
sage-wiki compile
# Tìm kiếm
sage-wiki search "attention mechanism"
# Đặt câu hỏi
sage-wiki query "How does flash attention optimize memory?"
# Bảng điều khiển terminal tương tác
sage-wiki tui
# Duyệt trong trình duyệt (yêu cầu build -tags webui)
sage-wiki serve --ui
# Theo dõi thư mục
sage-wiki compile --watch
```

### Lớp phủ Vault (vault Obsidian hiện có)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Chỉnh sửa config.yaml để thiết lập thư mục nguồn/bỏ qua, thêm api key, chọn LLM
# Biên dịch lần đầu
sage-wiki compile
# Theo dõi vault
sage-wiki compile --watch
```

### Docker

```bash
# Tải từ GitHub Container Registry
docker pull ghcr.io/xoai/sage-wiki:latest

# Hoặc từ Docker Hub
docker pull xoai/sage-wiki:latest

# Chạy với thư mục wiki được mount
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# Hoặc build từ mã nguồn
docker build -t sage-wiki .
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... sage-wiki
```

Các tag có sẵn: `:latest` (nhánh main), `:v1.0.0` (bản phát hành), `:sha-abc1234` (commit cụ thể). Đa kiến trúc: `linux/amd64` và `linux/arm64`.

Xem [hướng dẫn tự host](docs/guides/self-hosted-server.md) để biết về Docker Compose, đồng bộ Syncthing, reverse proxy, và cấu hình nhà cung cấp LLM.

## Các lệnh

| Lệnh                                                                                   | Mô tả                                               |
| --------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| `sage-wiki init [--vault] [--skill <agent>]`                                            | Khởi tạo dự án (greenfield hoặc lớp phủ vault)      |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | Biên dịch nguồn thành bài viết wiki                  |
| `sage-wiki serve [--transport stdio\|sse]`                                              | Khởi động máy chủ MCP cho các agent LLM              |
| `sage-wiki serve --ui [--port 3333]`                                                    | Khởi động web UI (yêu cầu build `-tags webui`)       |
| `sage-wiki lint [--fix] [--pass name]`                                                  | Chạy các bước kiểm tra lint                          |
| `sage-wiki search "query" [--tags ...]`                                                 | Tìm kiếm lai (BM25 + vector)                        |
| `sage-wiki query "question"`                                                            | Hỏi đáp trên wiki                                   |
| `sage-wiki tui`                                                                         | Khởi chạy bảng điều khiển terminal tương tác         |
| `sage-wiki ingest <url\|path>`                                                          | Thêm một nguồn                                      |
| `sage-wiki status`                                                                      | Thống kê và tình trạng wiki                          |
| `sage-wiki provenance <source-or-concept>`                                              | Hiển thị ánh xạ nguồn gốc nguồn↔bài viết            |
| `sage-wiki doctor`                                                                      | Kiểm tra cấu hình và kết nối                        |
| `sage-wiki diff`                                                                        | Hiển thị thay đổi nguồn đang chờ so với manifest     |
| `sage-wiki list`                                                                        | Liệt kê các thực thể wiki, khái niệm, hoặc nguồn   |
| `sage-wiki write <summary\|article>`                                                    | Viết tóm tắt hoặc bài viết                          |
| `sage-wiki ontology <query\|list\|add>`                                                 | Truy vấn, liệt kê, và quản lý đồ thị ontology       |
| `sage-wiki hub <add\|remove\|search\|status\|list>`                                    | Các lệnh hub đa dự án                               |
| `sage-wiki learn "text"`                                                                | Lưu trữ một mục học                                 |
| `sage-wiki capture "text"`                                                              | Ghi nhận tri thức từ văn bản                         |
| `sage-wiki add-source <path>`                                                           | Đăng ký tệp nguồn trong manifest                    |
| `sage-wiki skill <refresh\|preview> [--target <agent>]`                                 | Tạo hoặc làm mới tệp kỹ năng agent                  |
| `sage-wiki pack install <name\|url>`                                                    | Cài đặt gói đóng góp                                |
| `sage-wiki pack apply <name> [--mode merge\|replace]`                                   | Áp dụng gói đã cài vào dự án                        |
| `sage-wiki pack remove <name>`                                                          | Gỡ gói khỏi dự án                                   |
| `sage-wiki pack list`                                                                   | Liệt kê gói đã áp dụng, cache, và tích hợp          |
| `sage-wiki pack search <query>`                                                         | Tìm kiếm registry gói                               |
| `sage-wiki pack update [name]`                                                          | Cập nhật gói lên phiên bản mới nhất                  |
| `sage-wiki pack info <name>`                                                            | Hiển thị chi tiết gói                                |
| `sage-wiki pack create <name>`                                                          | Tạo khung thư mục gói mới                           |
| `sage-wiki pack validate [path]`                                                        | Kiểm tra schema và file của gói                      |
| `sage-wiki auth login --provider <name>`                                                | Đăng nhập OAuth cho xác thực đăng ký                 |
| `sage-wiki auth import --provider <name>`                                               | Nhập thông tin xác thực từ công cụ CLI hiện có       |
| `sage-wiki auth status`                                                                 | Hiển thị thông tin xác thực đăng ký đã lưu           |
| `sage-wiki auth logout --provider <name>`                                               | Xóa thông tin xác thực đã lưu                       |
| `sage-wiki verify [--all] [--since 7d] [--limit 20]`                                   | Xác minh cơ sở cho các đầu ra đang chờ              |
| `sage-wiki outputs list [--state pending\|confirmed\|conflict\|stale]`                  | Liệt kê đầu ra theo trạng thái tin cậy              |
| `sage-wiki outputs promote <id>`                                                        | Thăng cấp đầu ra thủ công lên đã xác nhận           |
| `sage-wiki outputs reject <id>`                                                         | Từ chối và xóa đầu ra đang chờ                      |
| `sage-wiki outputs resolve <id>`                                                        | Thăng cấp câu trả lời, từ chối các xung đột         |
| `sage-wiki outputs clean [--older-than 90d]`                                            | Xóa đầu ra cũ/đang chờ lâu ngày                     |
| `sage-wiki outputs migrate`                                                             | Di chuyển đầu ra hiện có vào hệ thống tin cậy        |
| `sage-wiki scribe <session-file>`                                                       | Trích xuất thực thể từ bản ghi phiên                 |

## TUI

```bash
sage-wiki tui
```

Bảng điều khiển terminal đầy đủ tính năng với 4 tab:

- **[F1] Duyệt** — Điều hướng bài viết theo phần (khái niệm, tóm tắt, đầu ra). Phím mũi tên để chọn, Enter để đọc với markdown được render bởi glamour, Esc để quay lại.
- **[F2] Tìm kiếm** — Tìm kiếm mờ (fuzzy) với bản xem trước chia đôi. Gõ để lọc, kết quả xếp hạng theo điểm lai, Enter để mở trong `$EDITOR`.
- **[F3] Hỏi đáp** — Hỏi đáp streaming theo cuộc hội thoại. Đặt câu hỏi, nhận câu trả lời được tổng hợp bởi LLM với trích dẫn nguồn. Ctrl+S lưu câu trả lời vào outputs/.
- **[F4] Biên dịch** — Bảng điều khiển biên dịch trực tiếp. Theo dõi thư mục nguồn để phát hiện thay đổi và tự động biên dịch lại. Duyệt tệp đã biên dịch với bản xem trước.

Chuyển tab: `F1`-`F4` từ bất kỳ tab nào, `1`-`4` trên Duyệt/Biên dịch, `Esc` quay lại Duyệt. Thoát bằng `Ctrl+C`.

## Web UI

![Sage-Wiki Architecture](sage-wiki-webui.png)

sage-wiki bao gồm một trình xem dựa trên trình duyệt tùy chọn để đọc và khám phá wiki của bạn.

```bash
sage-wiki serve --ui
# Mở tại http://127.0.0.1:3333
```

Tính năng:

- **Trình duyệt bài viết** với markdown được render, tô sáng cú pháp, và `[[wikilinks]]` có thể nhấp
- **Tìm kiếm lai** với kết quả được xếp hạng và đoạn trích
- **Đồ thị tri thức** — trực quan hóa tương tác lực hướng (force-directed) của các khái niệm và kết nối của chúng
- **Hỏi đáp streaming** — đặt câu hỏi và nhận câu trả lời được tổng hợp bởi LLM với trích dẫn nguồn
- **Mục lục** với scroll-spy, hoặc chuyển sang chế độ xem đồ thị
- **Chế độ tối/sáng** chuyển đổi với phát hiện ưu tiên hệ thống
- **Phát hiện liên kết hỏng** — các liên kết bài viết bị thiếu hiển thị màu xám

Web UI được xây dựng với Preact + Tailwind CSS và được nhúng vào tệp nhị phân Go qua `go:embed`. Nó thêm khoảng 1.2 MB (đã nén gzip) vào kích thước tệp nhị phân. Để build mà không có web UI, bỏ qua cờ `-tags webui` — tệp nhị phân vẫn hoạt động cho tất cả các thao tác CLI và MCP.

Tùy chọn:

- `--port 3333` — thay đổi cổng (mặc định 3333)
- `--bind 0.0.0.0` — mở trên mạng (mặc định chỉ localhost, không có xác thực)

## Cấu hình

`config.yaml` được tạo bởi `sage-wiki init`. Ví dụ đầy đủ:

```yaml
version: 1
project: my-research
description: "Personal research wiki"

# Thư mục nguồn để theo dõi và biên dịch
sources:
  - path: raw # hoặc thư mục vault như Clippings/, Papers/
    type: auto # tự động phát hiện từ phần mở rộng tệp
    watch: true

output: wiki # thư mục đầu ra đã biên dịch (_wiki cho chế độ lớp phủ vault)

# Thư mục không bao giờ đọc hoặc gửi tới API (chế độ lớp phủ vault)
# ignore:
#   - Daily Notes
#   - Personal

# Nhà cung cấp LLM
# Hỗ trợ: anthropic, openai, gemini, ollama, openai-compatible, qwen
# Cho OpenRouter hoặc các nhà cung cấp tương thích OpenAI khác:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# Cho Alibaba Cloud DashScope Qwen:
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY} # hỗ trợ mở rộng biến môi trường
  # auth: subscription          # sử dụng thông tin xác thực đăng ký thay vì api_key
                                # yêu cầu: sage-wiki auth login --provider <name>
                                # nhà cung cấp hỗ trợ: openai, anthropic, gemini
  # base_url:                   # endpoint tùy chỉnh (OpenRouter, Azure, v.v.)
  # rate_limit: 60              # số yêu cầu mỗi phút
  # extra_params:               # tham số riêng của nhà cung cấp được hợp nhất vào body yêu cầu
  #   enable_thinking: false    # ví dụ: tắt chế độ suy nghĩ Qwen
  #   reasoning_effort: low     # ví dụ: điều khiển suy luận DeepSeek

# Model cho mỗi tác vụ — dùng model rẻ hơn cho khối lượng lớn, chất lượng cho viết
models:
  summarize: gemini-3-flash-preview
  extract: gemini-3-flash-preview
  write: gemini-3-flash-preview
  lint: gemini-3-flash-preview
  query: gemini-3-flash-preview

# Nhà cung cấp embedding (tùy chọn — tự động phát hiện từ nhà cung cấp api)
# Ghi đè để sử dụng nhà cung cấp khác cho embeddings
embed:
  provider: auto # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # key riêng cho embeddings
  # base_url:                   # endpoint riêng
  # rate_limit: 0              # giới hạn RPM cho embedding (0 = không giới hạn; đặt 1200 cho Gemini Tier 1)

# Ghi chú đa nhà cung cấp:
# Phần api cấu hình nhà cung cấp LLM chính được sử dụng cho tất cả tác vụ
# biên dịch và truy vấn (summarize, extract, write, lint, query). Phần embed
# có thể sử dụng nhà cung cấp KHÁC cho embeddings — với api_key, base_url,
# và rate_limit riêng. Điều này cho phép bạn kết hợp nhà cung cấp để tối ưu chi phí hoặc chất lượng:
#
#   api:
#     provider: anthropic                    # Claude cho sinh nội dung
#     api_key: ${ANTHROPIC_API_KEY}
#   models:
#     summarize: claude-haiku-4-5-20251001   # model rẻ cho công việc hàng loạt
#     write: claude-sonnet-4-20250514        # model chất lượng cho bài viết
#     query: claude-sonnet-4-20250514
#   embed:
#     provider: openai                       # OpenAI cho embeddings
#     model: text-embedding-3-small
#     api_key: ${OPENAI_API_KEY}
#
# Với xác thực đăng ký, bạn có thể xác thực với nhiều nhà cung cấp:
#   sage-wiki auth login --provider anthropic
#   sage-wiki auth import --provider gemini
# Sau đó dùng Anthropic cho sinh nội dung và Gemini cho embeddings.

compiler:
  max_parallel: 20 # số cuộc gọi LLM đồng thời (với backpressure thích ứng)
  debounce_seconds: 2 # debounce chế độ theo dõi
  summary_max_tokens: 2000
  article_max_tokens: 4000
  # extract_batch_size: 20     # số tóm tắt mỗi cuộc gọi trích xuất khái niệm (giảm để tránh cắt JSON trên corpus lớn)
  # extract_max_tokens: 8192   # token đầu ra tối đa cho trích xuất khái niệm (tăng lên 16384 nếu trích xuất bị cắt)
  auto_commit: true # git commit sau biên dịch
  auto_lint: true # chạy lint sau biên dịch
  mode: auto # standard, batch, hoặc auto (auto = batch khi 10+ nguồn)
  # estimate_before: false    # hiển thị ước tính chi phí trước khi biên dịch
  # prompt_cache: true        # bật bộ nhớ cache prompt (mặc định: true)
  # batch_threshold: 10       # số nguồn tối thiểu cho chế độ auto-batch
  # token_price_per_million: 0  # ghi đè giá (0 = sử dụng giá tích hợp)
  # timezone: Asia/Shanghai   # múi giờ IANA cho timestamp hiển thị (mặc định: UTC)
  # article_fields:           # trường frontmatter tùy chỉnh được trích xuất từ phản hồi LLM
  #   - language
  #   - domain

  # Biên dịch phân tầng — lập chỉ mục nhanh, biên dịch những gì quan trọng
  default_tier: 3 # 0=chỉ mục, 1=chỉ mục+embed, 3=biên dịch đầy đủ
  # tier_defaults:             # ghi đè tầng theo phần mở rộng tệp
  #   json: 0                  # dữ liệu có cấu trúc — chỉ lập chỉ mục
  #   yaml: 0
  #   lock: 0
  #   md: 1                    # văn xuôi — chỉ mục + embed
  #   go: 1                    # mã nguồn — chỉ mục + embed + phân tích
  # auto_promote: true         # thăng cấp lên tầng 3 dựa trên lượt truy vấn
  # auto_demote: true          # hạ cấp bài viết cũ
  # split_threshold: 15000     # ký tự — chia tài liệu lớn để viết nhanh hơn
  # dedup_threshold: 0.85      # cosine similarity cho loại bỏ trùng lặp khái niệm
  # backpressure: true         # đồng thời thích ứng khi bị giới hạn tốc độ

search:
  hybrid_weight_bm25: 0.7 # trọng số BM25 so với vector
  hybrid_weight_vector: 0.3
  default_limit: 10
  # query_expansion: true     # mở rộng truy vấn LLM cho hỏi đáp (mặc định: true)
  # rerank: true              # xếp hạng lại LLM cho hỏi đáp (mặc định: true)
  # chunk_size: 800           # token mỗi chunk cho lập chỉ mục (100-5000)
  # graph_expansion: true     # mở rộng ngữ cảnh dựa trên đồ thị cho hỏi đáp (mặc định: true)
  # graph_max_expand: 10      # số bài viết tối đa được thêm qua mở rộng đồ thị
  # graph_depth: 2            # độ sâu duyệt ontology (1-5)
  # context_max_tokens: 8000  # ngân sách token cho ngữ cảnh truy vấn
  # weight_direct_link: 3.0   # tín hiệu đồ thị: quan hệ ontology giữa các khái niệm
  # weight_source_overlap: 4.0 # tín hiệu đồ thị: tài liệu nguồn chung
  # weight_common_neighbor: 1.5 # tín hiệu đồ thị: láng giềng chung Adamic-Adar
  # weight_type_affinity: 1.0  # tín hiệu đồ thị: phần thưởng cặp loại thực thể

serve:
  transport: stdio # stdio hoặc sse
  port: 3333 # chỉ chế độ SSE

# Tin cậy đầu ra — cách ly đầu ra truy vấn cho đến khi được xác minh
# trust:
#   include_outputs: false       # "false" (mặc định), "verified", "true" (kế thừa)
#   consensus_threshold: 3       # số xác nhận cho thăng cấp tự động
#   grounding_threshold: 0.8     # điểm cơ sở tối thiểu (0.0-1.0)
#   similarity_threshold: 0.85   # ngưỡng khớp câu hỏi
#   auto_promote: true           # tự động thăng cấp khi đạt tất cả ngưỡng

# Loại ontology (tùy chọn)
# Mở rộng các loại tích hợp với từ đồng nghĩa bổ sung hoặc thêm loại tùy chỉnh.
# ontology:
#   relation_types:
#     - name: implements           # mở rộng loại tích hợp với thêm từ đồng nghĩa
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates            # thêm loại quan hệ tùy chỉnh
#       synonyms: ["regulates", "regulated by", "调控"]
#   entity_types:
#     - name: decision
#       description: "A recorded decision with rationale"
```

### Cấu hình đa nhà cung cấp

sage-wiki cho phép bạn sử dụng các nhà cung cấp LLM khác nhau cho các tác vụ khác nhau. Phần `api` thiết lập nhà cung cấp chính cho sinh nội dung (summarize, extract, write, lint, query), trong khi `embed` có thể sử dụng nhà cung cấp hoàn toàn riêng biệt cho embeddings — mỗi nhà cung cấp với thông tin xác thực và giới hạn tốc độ riêng.

**Trường hợp sử dụng:**
- **Tối ưu chi phí** — model rẻ cho tóm tắt hàng loạt, model chất lượng cho viết bài
- **Tốt nhất trong từng lĩnh vực** — Claude cho sinh nội dung, OpenAI cho embeddings, Ollama cho tìm kiếm cục bộ
- **Kết hợp gói đăng ký** — sử dụng gói đăng ký ChatGPT cho sinh nội dung và gói đăng ký Gemini cho embeddings

**Ví dụ: Claude cho sinh nội dung + OpenAI embeddings**

```yaml
api:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}

models:
  summarize: claude-haiku-4-5-20251001    # rẻ cho công việc hàng loạt
  extract: claude-haiku-4-5-20251001
  write: claude-sonnet-4-20250514         # chất lượng cho bài viết
  lint: claude-haiku-4-5-20251001
  query: claude-sonnet-4-20250514

embed:
  provider: openai
  model: text-embedding-3-small
  api_key: ${OPENAI_API_KEY}
```

**Ví dụ: Xác thực đăng ký với hai nhà cung cấp**

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
  # không cần api_key — sử dụng thông tin xác thực đăng ký Gemini đã nhập
```

Phần `models` kiểm soát model nào được sử dụng cho mỗi tác vụ, tất cả trong nhà cung cấp chính. Các model khác nhau có thể có sự đánh đổi chi phí/chất lượng rất khác — sử dụng model nhỏ hơn (haiku, flash, mini) cho các lượt xử lý khối lượng lớn như tóm tắt, và model lớn hơn (sonnet, pro) cho viết bài và hỏi đáp.

### Quan hệ có thể cấu hình

Ontology có 8 loại quan hệ tích hợp: `implements`, `extends`, `optimizes`, `contradicts`, `cites`, `prerequisite_of`, `trades_off`, `derived_from`. Mỗi loại có từ khóa đồng nghĩa mặc định được sử dụng cho trích xuất tự động.

Bạn có thể tùy chỉnh quan hệ qua `ontology.relations` trong `config.yaml`:

- **Mở rộng loại tích hợp** — thêm từ đồng nghĩa (ví dụ: từ khóa đa ngôn ngữ) vào loại hiện có. Các từ đồng nghĩa mặc định được giữ; từ của bạn được thêm vào.
- **Thêm loại tùy chỉnh** — định nghĩa tên quan hệ mới với từ khóa đồng nghĩa. Tên quan hệ phải là chữ thường `[a-z][a-z0-9_]*`.

Quan hệ được trích xuất bằng cách sử dụng xấp xỉ từ khóa cấp khối — từ khóa phải xuất hiện cùng với `[[wikilink]]` trong cùng một đoạn văn hoặc khối tiêu đề. Điều này ngăn chặn các cạnh sai từ các kết quả khớp xuyên đoạn.

Bạn cũng có thể hạn chế loại thực thể mà một quan hệ kết nối:

```yaml
ontology:
  relation_types:
    - name: curated_by
      synonyms: ["curated by", "organized by"]
      valid_sources: [exhibition, program]
      valid_targets: [artist]
```

Khi `valid_sources`/`valid_targets` được đặt, các cạnh chỉ được tạo nếu loại thực thể nguồn/đích khớp. Để trống = tất cả loại được phép (mặc định).

Không cấu hình = hành vi giống hệt như hiện tại. Cơ sở dữ liệu hiện có được di chuyển tự động khi mở lần đầu. Xem [hướng dẫn đầy đủ](docs/guides/configurable-relations.md) để biết ví dụ theo lĩnh vực cụ thể, quan hệ hạn chế theo loại, và cách trích xuất hoạt động.

## Tối ưu chi phí

sage-wiki theo dõi sử dụng token và ước tính chi phí cho mỗi lần biên dịch. Ba chiến lược để giảm chi phí:

**Cache prompt** (mặc định: bật) — Tái sử dụng prompt hệ thống qua các cuộc gọi LLM trong một lượt biên dịch. Anthropic và Gemini cache rõ ràng; OpenAI cache tự động. Tiết kiệm 50-90% token đầu vào.

**Batch API** — Gửi tất cả nguồn dưới dạng một batch bất đồng bộ duy nhất để giảm 50% chi phí. Có sẵn cho Anthropic và OpenAI.

```bash
sage-wiki compile --batch       # gửi batch, lưu checkpoint, thoát
sage-wiki compile               # kiểm tra trạng thái, nhận khi xong
```

**Ước tính chi phí** — Xem trước chi phí trước khi cam kết:

```bash
sage-wiki compile --estimate    # hiển thị phân tích chi phí, thoát
```

Hoặc đặt `compiler.estimate_before: true` trong cấu hình để hỏi mỗi lần.

**Chế độ tự động** — Đặt `compiler.mode: auto` và `compiler.batch_threshold: 10` để tự động sử dụng batch khi biên dịch 10+ nguồn.

## Xác thực đăng ký

Sử dụng gói đăng ký LLM hiện có thay vì API key. Hỗ trợ ChatGPT Plus/Pro, Claude Pro/Max, GitHub Copilot, và Google Gemini.

```bash
# Đăng nhập qua trình duyệt (OpenAI hoặc Anthropic)
sage-wiki auth login --provider openai

# Hoặc nhập từ công cụ CLI hiện có
sage-wiki auth import --provider claude
sage-wiki auth import --provider copilot
sage-wiki auth import --provider gemini
```

Sau đó đặt `api.auth: subscription` trong `config.yaml` của bạn:

```yaml
api:
  provider: openai
  auth: subscription
```

Tất cả các lệnh sẽ sử dụng thông tin xác thực đăng ký của bạn. Token tự động làm mới. Nếu token hết hạn và không thể làm mới, sage-wiki chuyển sang dùng `api_key` với cảnh báo.

**Hạn chế:** Chế độ batch không khả dụng với xác thực đăng ký (tự động tắt). Một số model có thể không truy cập được qua token đăng ký. Xem [hướng dẫn xác thực đăng ký](docs/guides/subscription-auth.md) để biết chi tiết.

## Tin cậy đầu ra

Khi sage-wiki trả lời một câu hỏi, câu trả lời là một tuyên bố do LLM tạo ra, không phải sự thật đã được xác minh. Nếu không có biện pháp bảo vệ, các câu trả lời sai sẽ được lập chỉ mục vào wiki và làm ô nhiễm các truy vấn trong tương lai. Hệ thống tin cậy đầu ra cách ly các đầu ra mới và yêu cầu xác minh trước khi chúng được đưa vào kho dữ liệu có thể tìm kiếm.

```yaml
# config.yaml
trust:
  include_outputs: verified  # "false" (loại trừ tất cả), "verified" (chỉ đã xác nhận), "true" (kế thừa)
  consensus_threshold: 3     # số xác nhận cần thiết cho thăng cấp tự động
  grounding_threshold: 0.8   # điểm cơ sở tối thiểu
  similarity_threshold: 0.85 # cosine similarity cho khớp câu hỏi
  auto_promote: true          # tự động thăng cấp khi đạt ngưỡng
```

**Cách hoạt động:**

1. **Truy vấn** — sage-wiki trả lời câu hỏi của bạn. Đầu ra được ghi vào `wiki/under_review/` ở trạng thái đang chờ.
2. **Đồng thuận** — Nếu cùng câu hỏi được hỏi lại và tạo ra cùng câu trả lời từ các chunk nguồn khác nhau, số xác nhận sẽ tích lũy. Tính độc lập được tính qua khoảng cách Jaccard giữa các tập chunk.
3. **Kiểm chứng cơ sở** — Chạy `sage-wiki verify` để kiểm tra các tuyên bố đối chiếu với các đoạn nguồn qua LLM entailment.
4. **Thăng cấp** — Khi cả ngưỡng đồng thuận và kiểm chứng cơ sở đều đạt, đầu ra được thăng cấp lên `wiki/outputs/` và được lập chỉ mục vào tìm kiếm.

```bash
# Kiểm tra đầu ra đang chờ
sage-wiki outputs list

# Chạy xác minh cơ sở
sage-wiki verify --all

# Thăng cấp thủ công một đầu ra đáng tin cậy
sage-wiki outputs promote 2026-05-09-what-is-attention.md

# Giải quyết xung đột (thăng cấp một, từ chối các cái khác)
sage-wiki outputs resolve 2026-05-09-what-is-attention.md

# Dọn dẹp đầu ra đang chờ cũ
sage-wiki outputs clean --older-than 90d

# Di chuyển đầu ra hiện có vào hệ thống tin cậy
sage-wiki outputs migrate
```

Thay đổi nguồn trong quá trình `sage-wiki compile` tự động hạ cấp các đầu ra đã xác nhận khi nguồn được trích dẫn bị sửa đổi. Xem [hướng dẫn tin cậy đầu ra](docs/guides/output-trust.md) để biết kiến trúc đầy đủ, tham chiếu cấu hình, và xử lý sự cố.

## Mở rộng quy mô cho Vault lớn

sage-wiki sử dụng **biên dịch phân tầng** để xử lý vault từ 10K-100K+ tài liệu. Thay vì biên dịch mọi thứ qua toàn bộ pipeline LLM, các nguồn được định tuyến qua các tầng dựa trên loại tệp và mức sử dụng:

| Tầng | Xử lý gì | Chi phí | Thời gian/tài liệu |
|------|-----------|---------|---------------------|
| **0** — Chỉ lập chỉ mục | Tìm kiếm toàn văn FTS5 | Miễn phí | ~5ms |
| **1** — Chỉ mục + embed | FTS5 + vector embedding | ~$0.00002 | ~200ms |
| **2** — Phân tích mã | Tóm tắt cấu trúc qua regex parser (không cần LLM) | Miễn phí | ~10ms |
| **3** — Biên dịch đầy đủ | Tóm tắt + trích xuất khái niệm + viết bài | ~$0.05-0.15 | ~5-8 phút |

Mặc định (`default_tier: 3`), tất cả nguồn đi qua toàn bộ pipeline LLM — hành vi giống như trước khi có biên dịch phân tầng. Cho vault lớn (10K+), đặt `default_tier: 1` để lập chỉ mục mọi thứ trong ~5.5 giờ, sau đó biên dịch theo yêu cầu — khi agent truy vấn một chủ đề, tìm kiếm báo hiệu nguồn chưa biên dịch, và `wiki_compile_topic` biên dịch chỉ cụm đó (~2 phút cho 20 nguồn).

**Tính năng chính:**
- **Mặc định theo loại tệp** — JSON, YAML, và tệp lock tự động chuyển sang Tầng 0. Cấu hình theo phần mở rộng qua `tier_defaults`.
- **Tự động thăng cấp** — Nguồn thăng cấp lên Tầng 3 sau 3+ lượt truy vấn hoặc khi cụm chủ đề đạt 5+ nguồn.
- **Tự động hạ cấp** — Bài viết cũ (90 ngày không có truy vấn) hạ cấp xuống Tầng 1 để biên dịch lại khi truy cập tiếp theo.
- **Backpressure thích ứng** — Đồng thời tự điều chỉnh theo giới hạn tốc độ của nhà cung cấp. Bắt đầu với 20 song song, giảm một nửa khi gặp 429, tự động phục hồi.
- **10 trình phân tích mã** — Go (qua go/ast), TypeScript, JavaScript, Python, Rust, Java, C, C++, Ruby, cùng trích xuất khóa JSON/YAML/TOML. Mã nguồn nhận tóm tắt cấu trúc mà không cần cuộc gọi LLM.
- **Biên dịch theo yêu cầu** — `wiki_compile_topic("flash attention")` qua MCP biên dịch các nguồn liên quan trong thời gian thực.
- **Chấm điểm chất lượng** — Độ phủ nguồn, mức hoàn thành trích xuất, và mật độ tham chiếu chéo cho mỗi bài viết được theo dõi tự động.

Xem [hướng dẫn mở rộng đầy đủ](docs/guides/large-vault-performance.md) để biết cấu hình, ví dụ ghi đè tầng, và mục tiêu hiệu suất.

## Chất lượng tìm kiếm

sage-wiki sử dụng pipeline tìm kiếm nâng cao cho truy vấn hỏi đáp, lấy cảm hứng từ phân tích cách tiếp cận truy xuất của [qmd](https://github.com/dmayboroda/qmd):

- **Lập chỉ mục cấp chunk** — Bài viết được chia thành các chunk ~800 token, mỗi chunk có mục FTS5 và vector embedding riêng. Tìm kiếm "flash attention" tìm đoạn văn liên quan bên trong bài viết Transformer dài 3000 token.
- **Mở rộng truy vấn LLM** — Một cuộc gọi LLM duy nhất tạo ra viết lại từ khóa (cho BM25), viết lại ngữ nghĩa (cho tìm kiếm vector), và câu trả lời giả định (cho tương đồng embedding). Kiểm tra tín hiệu mạnh bỏ qua mở rộng khi kết quả BM25 hàng đầu đã đủ tin cậy.
- **Xếp hạng lại LLM** — 15 ứng viên hàng đầu được LLM chấm điểm về mức độ liên quan. Kết hợp nhận biết vị trí bảo vệ kết quả truy xuất có độ tin cậy cao (hạng 1-3 nhận 75% trọng số truy xuất, hạng 11+ nhận 60% trọng số bộ xếp hạng lại).
- **Tìm kiếm vector đa ngôn ngữ** — Tìm kiếm cosine brute-force toàn bộ trên tất cả vector chunk, kết hợp với BM25 qua kết hợp RRF. Điều này đảm bảo truy vấn đa ngôn ngữ (ví dụ: truy vấn tiếng Ba Lan đối chiếu nội dung tiếng Anh) tìm thấy kết quả liên quan về ngữ nghĩa ngay cả khi không có sự trùng lặp từ vựng nào.
- **Mở rộng ngữ cảnh tăng cường đồ thị** — Sau truy xuất, bộ chấm điểm đồ thị 4 tín hiệu tìm bài viết liên quan qua ontology: quan hệ trực tiếp (x3.0), tài liệu nguồn chung (x4.0), láng giềng chung qua Adamic-Adar (x1.5), và ái lực loại thực thể (x1.0). Điều này đưa ra các bài viết có liên quan về cấu trúc nhưng bị bỏ lỡ bởi tìm kiếm từ khóa/vector.
- **Kiểm soát ngân sách token** — Ngữ cảnh truy vấn được giới hạn ở mức token có thể cấu hình (mặc định 8000), với bài viết bị cắt ở 4000 token mỗi bài. Lấp đầy tham lam ưu tiên các bài viết có điểm cao nhất.

|                      | sage-wiki                                  | qmd               |
| -------------------- | ------------------------------------------ | ----------------- |
| Tìm kiếm chunk       | FTS5 + vector (kênh đôi)                   | Chỉ vector        |
| Mở rộng truy vấn     | Dựa trên LLM (lex/vec/hyde)               | Dựa trên LLM      |
| Xếp hạng lại         | LLM + kết hợp nhận biết vị trí            | Cross-encoder      |
| Ngữ cảnh đồ thị      | Mở rộng đồ thị 4 tín hiệu + duyệt 1 bước | Không có đồ thị   |
| Chi phí mỗi truy vấn | Miễn phí (Ollama) / ~$0.0006 (cloud)      | Miễn phí (GGUF cục bộ) |

Không cần cấu hình = tất cả tính năng đều bật. Với Ollama hoặc model cục bộ khác, tìm kiếm nâng cao hoàn toàn miễn phí — xếp hạng lại tự động tắt (model cục bộ gặp khó khăn với chấm điểm JSON có cấu trúc) nhưng tìm kiếm cấp chunk và mở rộng truy vấn vẫn hoạt động. Với LLM cloud, chi phí bổ sung không đáng kể (~$0.0006/truy vấn). Cả mở rộng và xếp hạng lại đều có thể bật/tắt qua cấu hình. Xem [hướng dẫn chất lượng tìm kiếm đầy đủ](docs/guides/search-quality.md) để biết cấu hình, phân tích chi phí, và so sánh chi tiết.

## Tùy chỉnh prompt

sage-wiki sử dụng prompt tích hợp cho tóm tắt và viết bài. Để tùy chỉnh:

```bash
sage-wiki init --prompts    # tạo thư mục prompts/ với các giá trị mặc định
```

Điều này tạo ra các tệp markdown có thể chỉnh sửa:

```
prompts/
├── summarize-article.md    # cách tóm tắt bài viết
├── summarize-paper.md      # cách tóm tắt bài báo
├── write-article.md        # cách viết bài viết khái niệm
├── extract-concepts.md     # cách nhận diện khái niệm
└── caption-image.md        # cách mô tả hình ảnh
```

Chỉnh sửa bất kỳ tệp nào để thay đổi cách sage-wiki xử lý loại đó. Thêm loại nguồn mới bằng cách tạo `summarize-{type}.md` (ví dụ: `summarize-dataset.md`). Xóa tệp để quay lại giá trị mặc định tích hợp.

### Trường frontmatter tùy chỉnh

Frontmatter bài viết được xây dựng từ hai nguồn: **dữ liệu thực tế** (tên khái niệm, bí danh, nguồn, dấu thời gian) luôn được tạo bởi mã, trong khi **trường ngữ nghĩa** được đánh giá bởi LLM.

Mặc định, `confidence` là trường duy nhất được LLM đánh giá. Để thêm trường tùy chỉnh:

1. Khai báo chúng trong `config.yaml`:

```yaml
compiler:
  article_fields:
    - language
    - domain
```

2. Cập nhật mẫu `prompts/write-article.md` để yêu cầu LLM cung cấp các trường này:

```
At the end of your response, state:
Language: (the primary language of the concept)
Domain: (the academic field, e.g., machine learning, biology)
Confidence: high, medium, or low
```

Phản hồi của LLM được trích xuất từ nội dung bài viết và tự động hợp nhất vào frontmatter YAML. Frontmatter kết quả trông như sau:

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

Các trường thực tế (`concept`, `aliases`, `sources`, `created_at`) luôn chính xác — chúng đến từ bước trích xuất, không phải LLM. Các trường ngữ nghĩa (`confidence` + trường tùy chỉnh của bạn) phản ánh đánh giá của LLM.

## Gói đóng góp

Gói là các profile cấu hình có thể cài đặt, đóng gói các loại ontology, prompt và nguồn mẫu cho các lĩnh vực cụ thể. sage-wiki đi kèm 8 gói tích hợp hoạt động offline:

| Gói | Đối tượng | Ontology chính |
|-----|----------|---------------|
| `academic-research` | Nhà nghiên cứu | cites, contradicts, finding, hypothesis |
| `software-engineering` | Đội phát triển | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | Quản lý ghi chú | relates_to, inspired_by, fleeting_note |
| `study-group` | Sinh viên | explains, prerequisite_of, definition |
| `meeting-organizer` | Quản lý | decided, assigned_to, action_item |
| `content-creation` | Nhà văn | references, revises, draft, published |
| `legal-compliance` | Pháp lý | regulates, supersedes, policy, control |

```bash
sage-wiki init --pack academic-research
sage-wiki pack install academic-research
sage-wiki pack apply academic-research --mode merge
sage-wiki pack list
```

Các gói có thể kết hợp. Gói cộng đồng được phân phối qua registry [sage-wiki-packs](https://github.com/xoai/sage-wiki-packs). Xem [CONTRIBUTING.md](CONTRIBUTING.md) để biết thêm chi tiết.

## Trình phân tích ngoài

sage-wiki có trình phân tích tích hợp cho hơn 12 định dạng. Với các định dạng khác, bạn có thể thêm trình phân tích ngoài bằng script viết bằng bất kỳ ngôn ngữ nào. Sử dụng giao thức stdin/stdout.

```yaml
parsers:
  - extensions: [".rtf"]
    command: python3
    args: ["rtf_parser.py"]
    timeout: 30s
```

Bảo mật: trình phân tích ngoài chạy với giới hạn thời gian, loại bỏ biến môi trường, và cách ly mạng trên Linux. Cần cấu hình `parsers.external: true`. Xem [CONTRIBUTING.md](CONTRIBUTING.md).

## Tệp kỹ năng Agent

sage-wiki có 17 công cụ MCP, nhưng agent sẽ không sử dụng chúng trừ khi có gì đó trong ngữ cảnh của chúng cho biết *khi nào* cần kiểm tra wiki. Tệp kỹ năng lấp đầy khoảng trống đó — các đoạn mã được tạo ra dạy agent khi nào cần tìm kiếm, ghi nhận gì, và cách truy vấn hiệu quả.

```bash
# Tạo trong quá trình khởi tạo dự án
sage-wiki init --skill claude-code

# Hoặc thêm vào dự án hiện có
sage-wiki skill refresh --target claude-code

# Xem trước mà không ghi
sage-wiki skill preview --target cursor
```

Điều này thêm một phần kỹ năng hành vi vào tệp hướng dẫn agent (CLAUDE.md, .cursorrules, v.v.) với các trigger cụ thể theo dự án, hướng dẫn ghi nhận, và ví dụ truy vấn được tạo từ config.yaml của bạn.

**Agent được hỗ trợ:** `claude-code`, `cursor`, `windsurf`, `agents-md` (Antigravity/Codex), `gemini`, `generic`

File kỹ năng cung cấp mẫu cơ sở chung — khi nào tìm kiếm, cái gì cần lưu, cách truy vấn — sử dụng các loại thực thể và quan hệ từ config.yaml. Để có hành vi agent theo lĩnh vực cụ thể, hãy áp dụng [gói đóng góp](#gói-đóng-góp):

```bash
sage-wiki init --skill claude-code --pack academic-research
```

Thư mục `skills/` của gói thêm các trigger theo lĩnh vực bên cạnh kỹ năng cơ sở. Chạy `skill refresh` chỉ tạo lại phần kỹ năng được đánh dấu — nội dung khác của bạn được giữ nguyên.

## Tích hợp MCP

![Tích hợp MCP](sage-wiki-interfaces.png)

### Claude Code

Thêm vào `.mcp.json`:

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

### SSE (client mạng)

```bash
sage-wiki serve --transport sse --port 3333
```

## Ghi nhận tri thức từ cuộc hội thoại AI

sage-wiki chạy như một máy chủ MCP, vì vậy bạn có thể ghi nhận tri thức trực tiếp từ các cuộc hội thoại AI. Kết nối nó với Claude Code, ChatGPT, Cursor, hoặc bất kỳ client MCP nào — sau đó chỉ cần hỏi:

> "Lưu những gì chúng ta vừa tìm ra về connection pooling vào wiki của tôi"

> "Ghi nhận các quyết định chính từ phiên debug này"

Công cụ `wiki_capture` trích xuất các mục tri thức (quyết định, phát hiện, sửa đổi) từ văn bản hội thoại qua LLM của bạn, ghi chúng dưới dạng tệp nguồn, và xếp hàng để biên dịch. Nhiễu (lời chào, thử lại, ngõ cụt) được tự động lọc bỏ.

Cho các sự kiện đơn lẻ, `wiki_learn` lưu trực tiếp một mẩu tri thức. Cho tài liệu đầy đủ, `wiki_add_source` nhập một tệp. Chạy `wiki_compile` để xử lý mọi thứ thành bài viết.

Xem hướng dẫn thiết lập đầy đủ: [Hướng dẫn lớp bộ nhớ Agent](docs/guides/agent-memory-layer.md)

## Thiết lập nhóm

sage-wiki mở rộng từ wiki cá nhân đến cơ sở tri thức chung cho nhóm 3-50 người. Ba mô hình triển khai:

**Repo đồng bộ Git** (3-10 người) — wiki nằm trong một repository Git. Mọi người clone, biên dịch cục bộ, và push. Thư mục `wiki/` đã biên dịch được theo dõi; cơ sở dữ liệu được `.gitignore` và xây dựng lại mỗi lần biên dịch.

**Máy chủ chia sẻ** (5-30 người) — chạy sage-wiki trên máy chủ với web UI. Thành viên nhóm duyệt trong trình duyệt và kết nối agent qua MCP over SSE.

**Liên kết Hub** (đa dự án) — mỗi dự án có wiki riêng. Hệ thống hub liên kết chúng thành một giao diện tìm kiếm thống nhất với `sage-wiki hub search`.

```bash
# Hub: đăng ký và tìm kiếm qua nhiều wiki
sage-wiki hub add /projects/backend-wiki
sage-wiki hub add /projects/ml-wiki
sage-wiki hub search "deployment process"
```

**Nhóm được gì:**

- **Bộ nhớ tổ chức tích lũy.** Những gì một agent học được, tất cả agent đều biết. Quyết định, quy ước, và lưu ý được ghi nhận từ bất kỳ phiên nào đều có thể tìm kiếm bởi mọi người.
- **Đầu ra được kiểm soát bằng tin cậy.** [Hệ thống tin cậy đầu ra](docs/guides/output-trust.md) cách ly câu trả lời LLM cho đến khi được xác minh cơ sở và xác nhận đồng thuận. Ảo giác của một agent không thể làm ô nhiễm kho dữ liệu chung.
- **Tệp kỹ năng agent.** Hướng dẫn được tạo ra dạy agent AI của mỗi thành viên khi nào cần kiểm tra wiki, ghi nhận gì, và cách truy vấn. Hỗ trợ Claude Code, Cursor, Windsurf, Codex, và Gemini.
- **Xác thực đăng ký theo người dùng.** Mỗi lập trình viên sử dụng gói đăng ký LLM riêng — không chia sẻ API key trong repo. Cấu hình ghi `auth: subscription`; thông tin xác thực theo người dùng tại `~/.sage-wiki/auth.json`.
- **Dấu vết kiểm toán đầy đủ.** `auto_commit: true` tạo git commit mỗi lần biên dịch. Ai thay đổi gì, khi nào.

```yaml
# Cấu hình khuyến nghị cho nhóm
trust:
  include_outputs: verified    # cách ly cho đến khi xác minh
compiler:
  default_tier: 1              # lập chỉ mục nhanh, biên dịch theo yêu cầu
  auto_commit: true            # dấu vết kiểm toán
```

Xem [hướng dẫn thiết lập nhóm đầy đủ](docs/guides/team-setup.md) để biết tổ chức nguồn, quy trình tích hợp agent, pipeline ghi nhận tri thức, cân nhắc mở rộng, và công thức sẵn dùng cho startup, phòng nghiên cứu, và nhóm vault Obsidian.

## Đánh giá hiệu năng

Đánh giá trên wiki thực được biên dịch từ 1,107 nguồn (cơ sở dữ liệu 49.4 MB, 2,832 tệp wiki).

Chạy `python3 eval.py .` trên dự án của bạn để tái tạo. Xem [eval.py](eval.py) để biết chi tiết.

### Hiệu suất

| Thao tác                             |   p50 |   Thông lượng |
| ------------------------------------ | ----: | ------------: |
| Tìm kiếm từ khóa FTS5 (top-10)      | 411µs |     1,775 qps |
| Tìm kiếm cosine vector (2,858 × 3072d) |  81ms |        15 qps |
| Lai RRF (BM25 + vector)             |  80ms |        16 qps |
| Duyệt đồ thị (BFS sâu ≤ 5)         |   1µs |      738K qps |
| Phát hiện chu trình (toàn bộ đồ thị) | 1.4ms |             — |
| Chèn FTS (batch 100)                |     — |     89,802 /s |
| Đọc hỗn hợp liên tục               |  77µs |  8,500+ ops/s |

Chi phí biên dịch không phải LLM (hash + phân tích phụ thuộc) dưới 1 giây. Thời gian thực tế của trình biên dịch hoàn toàn bị chi phối bởi các cuộc gọi API LLM.

### Chất lượng

| Chỉ số                     |   Điểm số |
| --------------------------- | --------: |
| Recall tìm kiếm@10         |  **100%** |
| Recall tìm kiếm@1          |     91.6% |
| Tỷ lệ trích dẫn nguồn      |     94.6% |
| Độ phủ bí danh              |     90.0% |
| Tỷ lệ trích xuất sự kiện   |     68.5% |
| Kết nối wiki                |     60.5% |
| Tính toàn vẹn tham chiếu chéo |     50.0% |
| **Điểm chất lượng tổng thể** | **73.0%** |

### Chạy đánh giá

```bash
# Đánh giá đầy đủ (hiệu suất + chất lượng)
python3 eval.py /path/to/your/wiki

# Chỉ hiệu suất
python3 eval.py --perf-only .

# Chỉ chất lượng
python3 eval.py --quality-only .

# JSON cho máy đọc
python3 eval.py --json . > report.json
```

Yêu cầu Python 3.10+. Cài `numpy` để benchmark vector nhanh hơn ~10 lần.

### Chạy kiểm thử

```bash
# Chạy toàn bộ bộ kiểm thử (tạo fixture tổng hợp, không cần dữ liệu thực)
python3 -m unittest eval_test -v

# Tạo fixture kiểm thử độc lập
python3 eval_test.py --generate-fixture ./test-fixture
python3 eval.py ./test-fixture
```

24 bài kiểm thử bao gồm: tạo fixture, các chế độ CLI (`--perf-only`, `--quality-only`, `--json`), xác nhận schema JSON, giới hạn điểm, recall tìm kiếm, trường hợp biên (wiki trống, tập dữ liệu lớn, đường dẫn thiếu).

## Kiến trúc

![Kiến trúc Sage-Wiki](sage-wiki-architecture.png)

- **Lưu trữ:** SQLite với FTS5 (tìm kiếm BM25) + vector BLOB (cosine similarity) + bảng compile_items cho theo dõi tầng/trạng thái theo nguồn
- **Ontology:** Đồ thị quan hệ-thực thể có kiểu với duyệt BFS và phát hiện chu trình
- **Tìm kiếm:** Pipeline nâng cao với FTS5 cấp chunk + lập chỉ mục vector, mở rộng truy vấn LLM, xếp hạng lại LLM, kết hợp RRF, và mở rộng đồ thị 4 tín hiệu. Phản hồi tìm kiếm báo hiệu nguồn chưa biên dịch cho biên dịch theo yêu cầu.
- **Trình biên dịch:** Pipeline phân tầng (Tầng 0: chỉ mục, Tầng 1: embed, Tầng 2: phân tích mã, Tầng 3: biên dịch LLM đầy đủ) với backpressure thích ứng, trích xuất Pass 2 đồng thời, cache prompt, Batch API (Anthropic + OpenAI + Gemini), theo dõi chi phí, biên dịch theo yêu cầu qua MCP, chấm điểm chất lượng, và nhận biết cascade. Embedding bao gồm thử lại với backoff mũ, giới hạn tốc độ tùy chọn, và mean-pooling cho đầu vào dài. 10 trình phân tích mã tích hợp (Go qua go/ast, 8 ngôn ngữ qua regex, trích xuất khóa dữ liệu có cấu trúc).
- **MCP:** 17 công cụ (6 đọc, 9 ghi, 2 kết hợp) qua stdio hoặc SSE, bao gồm `wiki_compile_topic` cho biên dịch theo yêu cầu và `wiki_capture` cho trích xuất tri thức
- **TUI:** bubbletea + glamour bảng điều khiển terminal 4 tab (duyệt, tìm kiếm, hỏi đáp, biên dịch) với hiển thị phân bố tầng
- **Web UI:** Preact + Tailwind CSS nhúng qua `go:embed` với build tag (`-tags webui`)
- **Scribe:** Giao diện mở rộng để nhập tri thức từ cuộc hội thoại. Session scribe xử lý bản ghi JSONL của Claude Code.
- **Gói:** Hệ thống gói đóng góp — 8 gói tích hợp, registry Git, vòng đời cài đặt/áp dụng/gỡ/cập nhật, áp dụng giao dịch với rollback snapshot.
- **Trình phân tích ngoài:** Trình phân tích định dạng file có thể cắm nóng qua giao thức subprocess stdin/stdout. Chạy sandbox với timeout, loại bỏ môi trường, cách ly mạng.

Không CGO. Pure Go. Đa nền tảng.

## Giấy phép

MIT
