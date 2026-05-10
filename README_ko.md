[English](README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | **한국어** | [Tiếng Việt](README_vi.md) | [Français](README_fr.md) | [Русский](README_ru.md)

# sage-wiki

[Andrej Karpathy의 아이디어](https://x.com/karpathy/status/2039805659525644595)를 구현한 LLM 기반 개인 지식 베이스입니다. [Sage Framework](https://github.com/xoai/sage)를 사용하여 개발되었습니다.

sage-wiki를 만들면서 배운 교훈들은 [여기](https://x.com/xoai/status/2040936964799795503)에서 확인할 수 있습니다.

논문, 기사, 노트를 넣으면 sage-wiki가 이를 구조화되고 상호 연결된 위키로 컴파일합니다 — 개념을 추출하고, 교차 참조를 발견하며, 모든 것을 검색 가능하게 만듭니다.

- **소스를 넣으면 위키가 나옵니다.** 문서를 폴더에 추가하세요. LLM이 읽고, 요약하고, 개념을 추출하며, 상호 연결된 문서들을 작성합니다.
- **100K+ 문서까지 확장 가능.** 계층화된 컴파일이 모든 것을 빠르게 인덱싱하고 중요한 것만 컴파일합니다. 100K 규모의 볼트도 몇 달이 아닌 몇 시간 안에 검색 가능합니다.
- **축적되는 지식.** 새로운 소스가 추가될 때마다 기존 문서가 풍부해집니다. 위키는 성장할수록 더 똑똑해집니다.
- **기존 도구와 호환.** Obsidian에서 기본적으로 열립니다. MCP를 통해 모든 LLM 에이전트와 연결됩니다. 단일 바이너리로 실행 — API 키 또는 기존 LLM 구독으로 작동합니다.
- **위키에 질문하기.** 청크 수준 인덱싱, LLM 쿼리 확장, 재순위 매기기를 통한 향상된 검색. 자연어 질문을 하면 출처가 인용된 답변을 받습니다.
- **필요할 때 컴파일.** 에이전트가 MCP를 통해 특정 주제의 컴파일을 트리거할 수 있습니다. 검색 결과는 컴파일되지 않은 소스가 있을 때 이를 알려줍니다.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_외곽 경계의 점들은 지식 베이스에 있는 모든 문서의 요약을 나타내고, 안쪽 원의 점들은 지식 베이스에서 추출된 개념을 나타내며, 링크는 그 개념들이 서로 어떻게 연결되는지를 보여줍니다._

## 설치

```bash
# CLI 전용 (웹 UI 없음)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# 웹 UI 포함 (프론트엔드 에셋 빌드를 위해 Node.js 필요)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## 지원하는 소스 형식

| 형식 | 확장자 | 추출 내용 |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | 프론트매터를 별도로 파싱한 본문 텍스트                |
| PDF         | `.pdf`                                  | 순수 Go 추출을 통한 전체 텍스트                            |
| Word        | `.docx`                                 | XML에서 추출한 문서 텍스트                                      |
| Excel       | `.xlsx`                                 | 셀 값 및 시트 데이터                                  |
| PowerPoint  | `.pptx`                                 | 슬라이드 텍스트 내용                                          |
| CSV         | `.csv`                                  | 헤더 + 행 (최대 1000행)                            |
| EPUB        | `.epub`                                 | XHTML에서 추출한 챕터 텍스트                                     |
| 이메일       | `.eml`                                  | 헤더 (발신/수신/제목/날짜) + 본문                       |
| 일반 텍스트  | `.txt`, `.log`                          | 원시 내용                                                 |
| 자막 | `.vtt`, `.srt`                          | 원시 내용                                                 |
| 이미지      | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | 비전 LLM을 통한 설명 (캡션, 내용, 표시된 텍스트) |
| 코드        | `.go`, `.py`, `.js`, `.ts`, `.rs` 등 | 소스 코드                                                 |

소스 폴더에 파일을 넣기만 하면 sage-wiki가 자동으로 형식을 감지합니다. 이미지는 비전 지원 LLM (Gemini, Claude, GPT-4o)이 필요합니다.

## 빠른 시작

![Compiler Pipeline](sage-wiki-compiler-pipeline.png)

### 새 프로젝트 (Greenfield)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# raw/에 소스 추가
cp ~/papers/*.pdf raw/papers/
cp ~/articles/*.md raw/articles/
# config.yaml을 편집하여 API 키 추가 및 LLM 선택
# 첫 번째 컴파일
sage-wiki compile
# 검색
sage-wiki search "attention mechanism"
# 질문하기
sage-wiki query "How does flash attention optimize memory?"
# 대화형 터미널 대시보드
sage-wiki tui
# 브라우저에서 탐색 (-tags webui 빌드 필요)
sage-wiki serve --ui
# 폴더 감시
sage-wiki compile --watch
```

### 볼트 오버레이 (기존 Obsidian 볼트)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# config.yaml을 편집하여 소스/무시 폴더 설정, API 키 추가, LLM 선택
# 첫 번째 컴파일
sage-wiki compile
# 볼트 감시
sage-wiki compile --watch
```

### Docker

```bash
# GitHub Container Registry에서 가져오기
docker pull ghcr.io/xoai/sage-wiki:latest

# 또는 Docker Hub에서 가져오기
docker pull xoai/sage-wiki:latest

# 위키 디렉토리를 마운트하여 실행
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# 또는 소스에서 빌드
docker build -t sage-wiki .
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... sage-wiki
```

사용 가능한 태그: `:latest` (main 브랜치), `:v1.0.0` (릴리스), `:sha-abc1234` (특정 커밋). 멀티 아키텍처: `linux/amd64` 및 `linux/arm64`.

Docker Compose, Syncthing 동기화, 리버스 프록시, LLM 프로바이더 설정에 대해서는 [셀프 호스팅 가이드](docs/guides/self-hosted-server.md)를 참조하세요.

## 명령어

| 명령어                                                                                 | 설명                                      |
| --------------------------------------------------------------------------------------- | ------------------------------------------------ |
| `sage-wiki init [--vault] [--skill <agent>]`                                            | 프로젝트 초기화 (새 프로젝트 또는 볼트 오버레이) |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | 소스를 위키 문서로 컴파일               |
| `sage-wiki serve [--transport stdio\|sse]`                                              | LLM 에이전트용 MCP 서버 시작                  |
| `sage-wiki serve --ui [--port 3333]`                                                    | 웹 UI 시작 (`-tags webui` 빌드 필요)      |
| `sage-wiki lint [--fix] [--pass name]`                                                  | 린팅 패스 실행                               |
| `sage-wiki search "query" [--tags ...]`                                                 | 하이브리드 검색 (BM25 + 벡터)                    |
| `sage-wiki query "question"`                                                            | 위키 기반 Q&A                             |
| `sage-wiki tui`                                                                         | 대화형 터미널 대시보드 실행            |
| `sage-wiki ingest <url\|path>`                                                          | 소스 추가                                     |
| `sage-wiki status`                                                                      | 위키 통계 및 상태                            |
| `sage-wiki provenance <source-or-concept>`                                              | 소스↔문서 출처 매핑 표시          |
| `sage-wiki doctor`                                                                      | 설정 및 연결 검증                 |
| `sage-wiki diff`                                                                        | 매니페스트 대비 대기 중인 소스 변경사항 표시     |
| `sage-wiki list`                                                                        | 위키 엔티티, 개념 또는 소스 목록         |
| `sage-wiki write <summary\|article>`                                                    | 요약 또는 문서 작성                       |
| `sage-wiki ontology <query\|list\|add>`                                                 | 온톨로지 그래프 쿼리, 목록, 관리       |
| `sage-wiki hub <add\|remove\|search\|status\|list>`                                    | 멀티 프로젝트 허브 명령어                       |
| `sage-wiki learn "text"`                                                                | 학습 항목 저장                           |
| `sage-wiki capture "text"`                                                              | 텍스트에서 지식 캡처                      |
| `sage-wiki add-source <path>`                                                           | 매니페스트에 소스 파일 등록           |
| `sage-wiki skill <refresh\|preview> [--target <agent>]`                                 | 에이전트 스킬 파일 생성 또는 갱신            |
| `sage-wiki auth login --provider <name>`                                                | 구독 인증을 위한 OAuth 로그인                |
| `sage-wiki auth import --provider <name>`                                               | 기존 CLI 도구에서 자격 증명 가져오기       |
| `sage-wiki auth status`                                                                 | 저장된 구독 자격 증명 표시            |
| `sage-wiki auth logout --provider <name>`                                               | 저장된 자격 증명 제거                        |
| `sage-wiki verify [--all] [--since 7d] [--limit 20]`                                   | 대기 중인 출력에 대한 근거 검증        |
| `sage-wiki outputs list [--state pending\|confirmed\|conflict\|stale]`                  | 신뢰 상태별 출력 목록                      |
| `sage-wiki outputs promote <id>`                                                        | 출력을 수동으로 확인됨으로 승격             |
| `sage-wiki outputs reject <id>`                                                         | 대기 중인 출력 거부 및 삭제               |
| `sage-wiki outputs resolve <id>`                                                        | 답변 승격, 경쟁하는 충돌 거부       |
| `sage-wiki outputs clean [--older-than 90d]`                                            | 오래된/대기 중인 출력 제거                 |
| `sage-wiki outputs migrate`                                                             | 기존 출력을 신뢰 시스템으로 마이그레이션       |
| `sage-wiki scribe <session-file>`                                                       | 세션 트랜스크립트에서 엔티티 추출       |

## TUI

```bash
sage-wiki tui
```

4개의 탭을 갖춘 풀 기능 터미널 대시보드:

- **[F1] 탐색** — 섹션별로 문서 탐색 (개념, 요약, 출력). 방향키로 선택, Enter로 glamour 렌더링된 마크다운 읽기, Esc로 뒤로 가기.
- **[F2] 검색** — 분할 창 미리보기가 있는 퍼지 검색. 입력하면 필터링되고, 결과는 하이브리드 점수로 순위가 매겨지며, Enter로 `$EDITOR`에서 열기.
- **[F3] Q&A** — 대화형 스트리밍 Q&A. 질문을 하면 소스 인용이 포함된 LLM 합성 답변을 받습니다. Ctrl+S로 답변을 outputs/에 저장.
- **[F4] 컴파일** — 라이브 컴파일 대시보드. 소스 디렉토리의 변경사항을 감시하고 자동으로 재컴파일합니다. 미리보기로 컴파일된 파일 탐색.

탭 전환: 모든 탭에서 `F1`-`F4`, 탐색/컴파일에서 `1`-`4`, `Esc`로 탐색으로 돌아가기. `Ctrl+C`로 종료.

## 웹 UI

![Sage-Wiki Architecture](sage-wiki-webui.png)

sage-wiki에는 위키를 읽고 탐색할 수 있는 선택적 브라우저 기반 뷰어가 포함되어 있습니다.

```bash
sage-wiki serve --ui
# http://127.0.0.1:3333 에서 열림
```

기능:

- **문서 브라우저** — 렌더링된 마크다운, 구문 강조, 클릭 가능한 `[[위키링크]]` 포함
- **하이브리드 검색** — 순위가 매겨진 결과와 스니펫
- **지식 그래프** — 개념과 그 연결을 보여주는 대화형 포스 다이렉트 시각화
- **스트리밍 Q&A** — 질문을 하면 소스 인용이 포함된 LLM 합성 답변을 받음
- **목차** — 스크롤 스파이 포함, 또는 그래프 뷰로 전환
- **다크/라이트 모드** — 시스템 설정 감지 토글
- **깨진 링크 감지** — 누락된 문서 링크가 회색으로 표시

웹 UI는 Preact + Tailwind CSS로 구축되어 `go:embed`를 통해 Go 바이너리에 임베딩됩니다. 바이너리 크기에 약 1.2 MB (gzip 압축)가 추가됩니다. 웹 UI 없이 빌드하려면 `-tags webui` 플래그를 생략하세요 — 바이너리는 여전히 모든 CLI 및 MCP 작업에서 작동합니다.

옵션:

- `--port 3333` — 포트 변경 (기본값 3333)
- `--bind 0.0.0.0` — 네트워크에 노출 (기본값은 localhost만, 인증 없음)

## 설정

`config.yaml`은 `sage-wiki init`으로 생성됩니다. 전체 예시:

```yaml
version: 1
project: my-research
description: "Personal research wiki"

# 감시하고 컴파일할 소스 폴더
sources:
  - path: raw # 또는 Clippings/, Papers/ 같은 볼트 폴더
    type: auto # 파일 확장자에서 자동 감지
    watch: true

output: wiki # 컴파일된 출력 디렉토리 (볼트 오버레이는 _wiki)

# 읽거나 API로 보내지 않을 폴더 (볼트 오버레이 모드)
# ignore:
#   - Daily Notes
#   - Personal

# LLM 프로바이더
# 지원: anthropic, openai, gemini, ollama, openai-compatible, qwen
# OpenRouter 또는 기타 OpenAI 호환 프로바이더:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# 알리바바 클라우드 DashScope Qwen:
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY} # 환경 변수 확장 지원
  # auth: subscription          # api_key 대신 구독 자격 증명 사용
                                # 필요: sage-wiki auth login --provider <name>
                                # 지원 프로바이더: openai, anthropic, gemini
  # base_url:                   # 커스텀 엔드포인트 (OpenRouter, Azure 등)
  # rate_limit: 60              # 분당 요청 수
  # extra_params:               # 프로바이더별 파라미터, 요청 본문에 병합
  #   enable_thinking: false    # 예: Qwen 사고 모드 비활성화
  #   reasoning_effort: low     # 예: DeepSeek 추론 제어

# 작업별 모델 — 대량 작업에는 저렴한 모델, 글쓰기에는 고품질 모델 사용
models:
  summarize: gemini-3-flash-preview
  extract: gemini-3-flash-preview
  write: gemini-3-flash-preview
  lint: gemini-3-flash-preview
  query: gemini-3-flash-preview

# 임베딩 프로바이더 (선택사항 — API 프로바이더에서 자동 감지)
# 임베딩에 다른 프로바이더를 사용하려면 재정의
embed:
  provider: auto # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # 임베딩용 별도 키
  # base_url:                   # 별도 엔드포인트
  # rate_limit: 0              # 임베딩 RPM 제한 (0 = 제한 없음; Gemini Tier 1은 1200으로 설정)

# 멀티 프로바이더 참고:
# api 섹션은 모든 컴파일러 및 쿼리 작업(요약, 추출, 작성, 린트, 쿼리)에
# 사용되는 기본 LLM 프로바이더를 구성합니다. embed 섹션은 임베딩에 완전히
# 다른 프로바이더를 사용할 수 있으며, 각각 고유한 api_key, base_url,
# rate_limit를 가집니다. 이를 통해 비용이나 품질에 따라 프로바이더를 혼합할 수 있습니다:
#
#   api:
#     provider: anthropic                    # 생성에 Claude 사용
#     api_key: ${ANTHROPIC_API_KEY}
#   models:
#     summarize: claude-haiku-4-5-20251001   # 대량 작업에 저렴한 모델
#     write: claude-sonnet-4-20250514        # 문서 작성에 고품질 모델
#     query: claude-sonnet-4-20250514
#   embed:
#     provider: openai                       # 임베딩에 OpenAI 사용
#     model: text-embedding-3-small
#     api_key: ${OPENAI_API_KEY}
#
# 구독 인증으로 여러 프로바이더를 인증할 수 있습니다:
#   sage-wiki auth login --provider anthropic
#   sage-wiki auth import --provider gemini
# 그런 다음 생성에 Anthropic, 임베딩에 Gemini를 사용합니다.

compiler:
  max_parallel: 20 # 동시 LLM 호출 수 (적응형 백프레셔 포함)
  debounce_seconds: 2 # 감시 모드 디바운스
  summary_max_tokens: 2000
  article_max_tokens: 4000
  # extract_batch_size: 20     # 개념 추출 호출당 요약 수 (대규모 코퍼스에서 JSON 잘림 방지를 위해 줄임)
  # extract_max_tokens: 8192   # 개념 추출 최대 출력 토큰 (추출이 잘리면 16384로 증가)
  auto_commit: true # 컴파일 후 git 커밋
  auto_lint: true # 컴파일 후 린트 실행
  mode: auto # standard, batch, 또는 auto (auto = 10개 이상 소스일 때 batch)
  # estimate_before: false    # 컴파일 전 비용 추정 프롬프트
  # prompt_cache: true        # 프롬프트 캐싱 활성화 (기본값: true)
  # batch_threshold: 10       # auto-batch 모드 최소 소스 수
  # token_price_per_million: 0  # 가격 재정의 (0 = 내장 가격 사용)
  # timezone: Asia/Shanghai   # 사용자 대면 타임스탬프용 IANA 시간대 (기본값: UTC)
  # article_fields:           # LLM 응답에서 추출되는 커스텀 프론트매터 필드
  #   - language
  #   - domain

  # 계층화된 컴파일 — 빠르게 인덱싱하고, 중요한 것만 컴파일
  default_tier: 3 # 0=인덱스, 1=인덱스+임베드, 3=전체 컴파일
  # tier_defaults:             # 확장자별 티어 재정의
  #   json: 0                  # 구조화된 데이터 — 인덱스만
  #   yaml: 0
  #   lock: 0
  #   md: 1                    # 산문 — 인덱스 + 임베드
  #   go: 1                    # 코드 — 인덱스 + 임베드 + 파싱
  # auto_promote: true         # 쿼리 히트에 따라 티어 3으로 자동 승격
  # auto_demote: true          # 오래된 문서 자동 강등
  # split_threshold: 15000     # 글자 수 — 빠른 작성을 위해 대용량 문서 분할
  # dedup_threshold: 0.85      # 개념 중복 제거를 위한 코사인 유사도
  # backpressure: true         # 속도 제한 시 적응형 동시성

search:
  hybrid_weight_bm25: 0.7 # BM25 대 벡터 가중치
  hybrid_weight_vector: 0.3
  default_limit: 10
  # query_expansion: true     # Q&A용 LLM 쿼리 확장 (기본값: true)
  # rerank: true              # Q&A용 LLM 재순위 매기기 (기본값: true)
  # chunk_size: 800           # 인덱싱용 청크당 토큰 수 (100-5000)
  # graph_expansion: true     # Q&A용 그래프 기반 컨텍스트 확장 (기본값: true)
  # graph_max_expand: 10      # 그래프 확장으로 추가되는 최대 문서 수
  # graph_depth: 2            # 온톨로지 탐색 깊이 (1-5)
  # context_max_tokens: 8000  # 쿼리 컨텍스트 토큰 예산
  # weight_direct_link: 3.0   # 그래프 신호: 개념 간 온톨로지 관계
  # weight_source_overlap: 4.0 # 그래프 신호: 공유 소스 문서
  # weight_common_neighbor: 1.5 # 그래프 신호: Adamic-Adar 공통 이웃
  # weight_type_affinity: 1.0  # 그래프 신호: 엔티티 유형 쌍 보너스

serve:
  transport: stdio # stdio 또는 sse
  port: 3333 # SSE 모드 전용

# 출력 신뢰 — 검증될 때까지 쿼리 출력을 격리
# trust:
#   include_outputs: false       # "false" (기본값), "verified", "true" (레거시)
#   consensus_threshold: 3       # 자동 승격을 위한 확인 횟수
#   grounding_threshold: 0.8     # 최소 근거 점수 (0.0-1.0)
#   similarity_threshold: 0.85   # 질문 매칭 임계값
#   auto_promote: true           # 모든 임계값 충족 시 자동 승격

# 온톨로지 유형 (선택사항)
# 추가 동의어로 내장 유형을 확장하거나 커스텀 유형을 추가합니다.
# ontology:
#   relation_types:
#     - name: implements           # 더 많은 동의어로 내장 유형 확장
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates            # 커스텀 관계 유형 추가
#       synonyms: ["regulates", "regulated by", "调控"]
#   entity_types:
#     - name: decision
#       description: "근거가 기록된 의사결정"
```

### 멀티 프로바이더 설정

sage-wiki는 각 작업에 다른 LLM 프로바이더를 사용할 수 있습니다. `api` 섹션은 생성(요약, 추출, 작성, 린트, 쿼리)을 위한 기본 프로바이더를 설정하고, `embed`는 임베딩에 완전히 별도의 프로바이더를 사용할 수 있습니다 — 각각 고유한 자격 증명과 속도 제한을 가집니다.

**사용 사례:**
- **비용 최적화** — 대량 요약에는 저렴한 모델, 문서 작성에는 고품질 모델
- **최적 조합** — 생성에 Claude, 임베딩에 OpenAI, 로컬 검색에 Ollama
- **구독 혼합** — 생성에 ChatGPT 구독, 임베딩에 Gemini 구독 사용

**예시: Claude로 생성 + OpenAI 임베딩**

```yaml
api:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}

models:
  summarize: claude-haiku-4-5-20251001    # 대량 작업에 저렴한 모델
  extract: claude-haiku-4-5-20251001
  write: claude-sonnet-4-20250514         # 문서 작성에 고품질 모델
  lint: claude-haiku-4-5-20251001
  query: claude-sonnet-4-20250514

embed:
  provider: openai
  model: text-embedding-3-small
  api_key: ${OPENAI_API_KEY}
```

**예시: 두 프로바이더로 구독 인증**

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
  # api_key 불필요 — 가져온 Gemini 구독 자격 증명 사용
```

`models` 섹션은 기본 프로바이더 내에서 작업별로 사용할 모델을 제어합니다. 모델마다 비용/품질 트레이드오프가 매우 다릅니다 — 요약 같은 대량 패스에는 작은 모델(haiku, flash, mini)을, 문서 작성과 Q&A에는 큰 모델(sonnet, pro)을 사용하세요.

### 설정 가능한 관계

온톨로지에는 8개의 내장 관계 유형이 있습니다: `implements`, `extends`, `optimizes`, `contradicts`, `cites`, `prerequisite_of`, `trades_off`, `derived_from`. 각각 자동 추출에 사용되는 기본 키워드 동의어가 있습니다.

`config.yaml`의 `ontology.relations`를 통해 관계를 커스터마이즈할 수 있습니다:

- **내장 유형 확장** — 기존 유형에 동의어(예: 다국어 키워드)를 추가합니다. 기본 동의어는 유지되고 추가한 것이 덧붙여집니다.
- **커스텀 유형 추가** — 키워드 동의어와 함께 새 관계 이름을 정의합니다. 관계 이름은 소문자 `[a-z][a-z0-9_]*` 형식이어야 합니다.

관계는 블록 수준 키워드 근접성을 사용하여 추출됩니다 — 키워드가 같은 단락이나 제목 블록에서 `[[위키링크]]`와 함께 나타나야 합니다. 이는 단락 간 매칭으로 인한 잘못된 엣지를 방지합니다.

관계가 연결하는 엔티티 유형도 제한할 수 있습니다:

```yaml
ontology:
  relation_types:
    - name: curated_by
      synonyms: ["curated by", "organized by"]
      valid_sources: [exhibition, program]
      valid_targets: [artist]
```

`valid_sources`/`valid_targets`가 설정되면 소스/타겟 엔티티 유형이 일치하는 경우에만 엣지가 생성됩니다. 비어 있음 = 모든 유형 허용 (기본값).

설정 없이 = 기존 동작과 동일합니다. 기존 데이터베이스는 처음 열 때 자동으로 마이그레이션됩니다. 도메인별 예시, 유형 제한 관계, 추출 방식에 대해서는 [전체 가이드](docs/guides/configurable-relations.md)를 참조하세요.

## 비용 최적화

sage-wiki는 모든 컴파일에서 토큰 사용량을 추적하고 비용을 추정합니다. 비용을 줄이는 세 가지 전략:

**프롬프트 캐싱** (기본값: 켜짐) — 컴파일 패스 내에서 LLM 호출 간에 시스템 프롬프트를 재사용합니다. Anthropic과 Gemini는 명시적으로 캐싱하고, OpenAI는 자동으로 캐싱합니다. 입력 토큰의 50-90%를 절약합니다.

**Batch API** — 모든 소스를 단일 비동기 배치로 제출하여 50% 비용 절감. Anthropic과 OpenAI에서 사용 가능합니다.

```bash
sage-wiki compile --batch       # 배치 제출, 체크포인트, 종료
sage-wiki compile               # 상태 폴링, 완료 시 결과 수신
```

**비용 추정** — 실행 전 비용 미리보기:

```bash
sage-wiki compile --estimate    # 비용 분석 표시, 종료
```

또는 설정에서 `compiler.estimate_before: true`를 설정하면 매번 프롬프트합니다.

**자동 모드** — `compiler.mode: auto`와 `compiler.batch_threshold: 10`을 설정하면 10개 이상의 소스를 컴파일할 때 자동으로 배치를 사용합니다.

## 구독 인증

API 키 대신 기존 LLM 구독을 사용하세요. ChatGPT Plus/Pro, Claude Pro/Max, GitHub Copilot, Google Gemini를 지원합니다.

```bash
# 브라우저를 통한 로그인 (OpenAI 또는 Anthropic)
sage-wiki auth login --provider openai

# 또는 기존 CLI 도구에서 가져오기
sage-wiki auth import --provider claude
sage-wiki auth import --provider copilot
sage-wiki auth import --provider gemini
```

그런 다음 `config.yaml`에서 `api.auth: subscription`을 설정합니다:

```yaml
api:
  provider: openai
  auth: subscription
```

모든 명령어가 구독 자격 증명을 사용합니다. 토큰은 자동으로 갱신됩니다. 토큰이 만료되어 갱신할 수 없으면 sage-wiki는 경고와 함께 `api_key`로 폴백합니다.

**제한사항:** 구독 인증에서는 배치 모드를 사용할 수 없습니다 (자동 비활성화). 일부 모델은 구독 토큰을 통해 접근할 수 없을 수 있습니다. 자세한 내용은 [구독 인증 가이드](docs/guides/subscription-auth.md)를 참조하세요.

## 출력 신뢰

sage-wiki가 질문에 답할 때, 그 답변은 LLM이 생성한 주장이지 검증된 사실이 아닙니다. 안전장치가 없으면 잘못된 답변이 위키에 인덱싱되어 향후 쿼리를 오염시킵니다. 출력 신뢰 시스템은 새로운 출력을 격리하고 검색 가능한 코퍼스에 들어가기 전에 검증을 요구합니다.

```yaml
# config.yaml
trust:
  include_outputs: verified  # "false" (모두 제외), "verified" (확인된 것만), "true" (레거시)
  consensus_threshold: 3     # 자동 승격에 필요한 확인 횟수
  grounding_threshold: 0.8   # 최소 근거 점수
  similarity_threshold: 0.85 # 질문 매칭을 위한 코사인 유사도
  auto_promote: true          # 임계값 충족 시 자동 승격
```

**작동 방식:**

1. **쿼리** — sage-wiki가 질문에 답합니다. 출력은 `wiki/under_review/`에 대기 중 상태로 기록됩니다.
2. **합의** — 같은 질문이 다시 요청되고 다른 소스 청크에서 같은 답변이 나오면 확인이 누적됩니다. 독립성은 청크 집합 간의 Jaccard 거리로 평가됩니다.
3. **근거 확인** — `sage-wiki verify`를 실행하여 LLM 함의를 통해 소스 구절 대비 주장을 확인합니다.
4. **승격** — 합의와 근거 임계값이 모두 충족되면 출력이 `wiki/outputs/`로 승격되어 검색에 인덱싱됩니다.

```bash
# 대기 중인 출력 확인
sage-wiki outputs list

# 근거 검증 실행
sage-wiki verify --all

# 신뢰할 수 있는 출력을 수동으로 승격
sage-wiki outputs promote 2026-05-09-what-is-attention.md

# 충돌 해결 (하나를 승격, 나머지 거부)
sage-wiki outputs resolve 2026-05-09-what-is-attention.md

# 오래된 대기 중 출력 정리
sage-wiki outputs clean --older-than 90d

# 기존 출력을 신뢰 시스템으로 마이그레이션
sage-wiki outputs migrate
```

`sage-wiki compile` 중 소스가 변경되면 인용된 소스가 수정된 경우 확인된 출력이 자동으로 강등됩니다. 전체 아키텍처, 설정 참조, 문제 해결에 대해서는 [출력 신뢰 가이드](docs/guides/output-trust.md)를 참조하세요.

## 대규모 볼트로 확장

sage-wiki는 10K-100K+ 문서 규모의 볼트를 처리하기 위해 **계층화된 컴파일**을 사용합니다. 모든 것을 전체 LLM 파이프라인으로 컴파일하는 대신, 파일 유형과 사용량에 따라 소스를 티어로 라우팅합니다:

| 티어 | 처리 내용 | 비용 | 문서당 시간 |
|------|-------------|------|-------------|
| **0** — 인덱스만 | FTS5 전체 텍스트 검색 | 무료 | ~5ms |
| **1** — 인덱스 + 임베드 | FTS5 + 벡터 임베딩 | ~$0.00002 | ~200ms |
| **2** — 코드 파싱 | 정규식 파서를 통한 구조적 요약 (LLM 없음) | 무료 | ~10ms |
| **3** — 전체 컴파일 | 요약 + 개념 추출 + 문서 작성 | ~$0.05-0.15 | ~5-8분 |

기본적으로 (`default_tier: 3`), 모든 소스가 전체 LLM 파이프라인을 거칩니다 — 계층화된 컴파일 이전과 동일한 동작입니다. 대규모 볼트(10K+)의 경우 `default_tier: 1`로 설정하면 모든 것을 약 5.5시간 만에 인덱싱한 다음 필요할 때 컴파일합니다 — 에이전트가 주제를 쿼리하면 검색이 컴파일되지 않은 소스를 알려주고, `wiki_compile_topic`이 해당 클러스터만 컴파일합니다 (20개 소스에 약 2분).

**주요 기능:**
- **파일 유형 기본값** — JSON, YAML, lock 파일은 자동으로 Tier 0으로 건너뜁니다. `tier_defaults`로 확장자별 설정.
- **자동 승격** — 소스가 3회 이상 검색 히트를 받거나 주제 클러스터가 5개 이상 소스에 도달하면 Tier 3으로 승격.
- **자동 강등** — 오래된 문서(쿼리 없이 90일)가 Tier 1로 강등되어 다음 접근 시 재컴파일.
- **적응형 백프레셔** — 동시성이 프로바이더의 속도 제한에 맞춰 자동 조정됩니다. 20개 병렬로 시작, 429 오류 시 절반으로 줄이고, 자동 복구.
- **10개 코드 파서** — Go (go/ast 사용), TypeScript, JavaScript, Python, Rust, Java, C, C++, Ruby, 그리고 JSON/YAML/TOML 키 추출. 코드는 LLM 호출 없이 구조적 요약을 받습니다.
- **온디맨드 컴파일** — MCP를 통해 `wiki_compile_topic("flash attention")`으로 관련 소스를 실시간 컴파일.
- **품질 스코어링** — 문서별 소스 커버리지, 추출 완전성, 교차 참조 밀도가 자동으로 추적됩니다.

설정, 티어 재정의 예시, 성능 목표에 대해서는 [전체 확장 가이드](docs/guides/large-vault-performance.md)를 참조하세요.

## 검색 품질

sage-wiki는 [qmd](https://github.com/dmayboroda/qmd)의 검색 접근 방식을 분석하여 영감을 받은 향상된 검색 파이프라인을 Q&A 쿼리에 사용합니다:

- **청크 수준 인덱싱** — 문서가 약 800 토큰 청크로 분할되며, 각각 고유한 FTS5 항목과 벡터 임베딩을 가집니다. "flash attention" 검색이 3000 토큰 Transformer 문서 내의 관련 단락을 찾습니다.
- **LLM 쿼리 확장** — 단일 LLM 호출로 키워드 재작성(BM25용), 시맨틱 재작성(벡터 검색용), 가설적 답변(임베딩 유사도용)을 생성합니다. 상위 BM25 결과가 이미 높은 신뢰도일 때 확장을 건너뛰는 강신호 검사가 있습니다.
- **LLM 재순위 매기기** — 상위 15개 후보가 LLM에 의해 관련성 점수를 받습니다. 위치 인식 블렌딩은 높은 신뢰도 검색 결과를 보호합니다 (순위 1-3은 75% 검색 가중치, 순위 11+는 60% 재순위 매기기 가중치).
- **교차 언어 벡터 검색** — 모든 청크 벡터에 대한 완전한 브루트포스 코사인 검색, RRF 퓨전을 통해 BM25와 결합. 이를 통해 다국어 쿼리(예: 영어 콘텐츠에 대한 폴란드어 쿼리)가 어휘적 겹침이 전혀 없어도 의미적으로 관련된 결과를 찾습니다.
- **그래프 강화 컨텍스트 확장** — 검색 후, 4신호 그래프 스코어러가 온톨로지를 통해 관련 문서를 찾습니다: 직접 관계 (x3.0), 공유 소스 문서 (x4.0), Adamic-Adar를 통한 공통 이웃 (x1.5), 엔티티 유형 친화도 (x1.0). 이를 통해 키워드/벡터 검색에서 놓친 구조적으로 관련된 문서를 표면에 올립니다.
- **토큰 예산 제어** — 쿼리 컨텍스트가 설정 가능한 토큰 한도(기본값 8000)로 제한되며, 문서당 4000 토큰으로 잘립니다. 탐욕적 채우기가 가장 높은 점수의 문서를 우선합니다.

|                 | sage-wiki                                  | qmd               |
| --------------- | ------------------------------------------ | ----------------- |
| 청크 검색    | FTS5 + 벡터 (듀얼 채널)               | 벡터만       |
| 쿼리 확장 | LLM 기반 (lex/vec/hyde)                   | LLM 기반         |
| 재순위 매기기      | LLM + 위치 인식 블렌딩              | 크로스 인코더     |
| 그래프 컨텍스트   | 4신호 그래프 확장 + 1홉 탐색 | 그래프 없음          |
| 쿼리당 비용  | 무료 (Ollama) / ~$0.0006 (클라우드)           | 무료 (로컬 GGUF) |

설정 없이 = 모든 기능 활성화. Ollama 또는 기타 로컬 모델을 사용하면 향상된 검색이 완전히 무료입니다 — 재순위 매기기는 자동 비활성화됩니다(로컬 모델은 구조화된 JSON 스코어링에 어려움이 있음). 하지만 청크 수준 검색과 쿼리 확장은 여전히 작동합니다. 클라우드 LLM을 사용하면 추가 비용은 무시할 수준입니다(쿼리당 ~$0.0006). 확장과 재순위 매기기 모두 설정으로 토글할 수 있습니다. 설정, 비용 분석, 상세 비교에 대해서는 [전체 검색 품질 가이드](docs/guides/search-quality.md)를 참조하세요.

## 프롬프트 커스터마이징

sage-wiki는 요약 및 문서 작성에 내장 프롬프트를 사용합니다. 커스터마이즈하려면:

```bash
sage-wiki init --prompts    # 기본값으로 prompts/ 디렉토리 생성
```

편집 가능한 마크다운 파일이 생성됩니다:

```
prompts/
├── summarize-article.md    # 문서를 요약하는 방법
├── summarize-paper.md      # 논문을 요약하는 방법
├── write-article.md        # 개념 문서를 작성하는 방법
├── extract-concepts.md     # 개념을 식별하는 방법
└── caption-image.md        # 이미지를 설명하는 방법
```

파일을 편집하여 sage-wiki가 해당 유형을 처리하는 방식을 변경할 수 있습니다. `summarize-{type}.md`(예: `summarize-dataset.md`)를 만들어 새 소스 유형을 추가합니다. 파일을 삭제하면 내장 기본값으로 되돌아갑니다.

### 커스텀 프론트매터 필드

문서 프론트매터는 두 가지 소스로 구성됩니다: **사실 데이터**(개념 이름, 별칭, 소스, 타임스탬프)는 항상 코드에 의해 생성되고, **시맨틱 필드**는 LLM에 의해 평가됩니다.

기본적으로 `confidence`만이 LLM 평가 필드입니다. 커스텀 필드를 추가하려면:

1. `config.yaml`에 선언합니다:

```yaml
compiler:
  article_fields:
    - language
    - domain
```

2. `prompts/write-article.md` 템플릿을 업데이트하여 LLM에 이 필드들을 요청합니다:

```
At the end of your response, state:
Language: (the primary language of the concept)
Domain: (the academic field, e.g., machine learning, biology)
Confidence: high, medium, or low
```

LLM의 응답은 문서 본문에서 추출되어 자동으로 YAML 프론트매터에 병합됩니다. 결과 프론트매터는 다음과 같습니다:

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

사실 데이터 필드(`concept`, `aliases`, `sources`, `created_at`)는 항상 정확합니다 — LLM이 아닌 추출 패스에서 나옵니다. 시맨틱 필드(`confidence` + 커스텀 필드)는 LLM의 판단을 반영합니다.

## 에이전트 스킬 파일

sage-wiki에는 17개의 MCP 도구가 있지만, 에이전트의 컨텍스트에 *언제* 위키를 확인해야 하는지 알려주는 것이 없으면 사용하지 않습니다. 스킬 파일이 이 격차를 해소합니다 — 에이전트에게 언제 검색하고, 무엇을 캡처하며, 어떻게 효과적으로 쿼리할지 가르치는 생성된 스니펫입니다.

```bash
# 프로젝트 초기화 시 생성
sage-wiki init --skill claude-code

# 또는 기존 프로젝트에 추가
sage-wiki skill refresh --target claude-code

# 작성하지 않고 미리보기
sage-wiki skill preview --target cursor
```

이것은 에이전트의 지시 파일(CLAUDE.md, .cursorrules 등)에 프로젝트별 트리거, 캡처 가이드라인, config.yaml에서 파생된 쿼리 예시가 포함된 동작 스킬 섹션을 추가합니다.

**지원하는 에이전트:** `claude-code`, `cursor`, `windsurf`, `agents-md` (Antigravity/Codex), `gemini`, `generic`

**도메인 팩:** 생성기가 소스 유형에 따라 팩을 자동 선택합니다:
- `codebase-memory` — 코드 프로젝트 (기본값). API 변경, 리팩토링, 브레이킹 체인지 시 트리거.
- `research-library` — 논문/기사 프로젝트. 도메인 질문, 관련 연구 시 트리거.
- `meeting-notes` — 운영 용도 (수동 설정만: `--pack meeting-notes`).
- `documentation-curator` — 문서화 프로젝트 (수동 설정만: `--pack documentation-curator`).

`skill refresh`를 실행하면 표시된 스킬 섹션만 재생성합니다 — 다른 내용은 보존됩니다.

## MCP 통합

![MCP Integration](sage-wiki-interfaces.png)

### Claude Code

`.mcp.json`에 추가:

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

### SSE (네트워크 클라이언트)

```bash
sage-wiki serve --transport sse --port 3333
```

## AI 대화에서 지식 캡처

sage-wiki는 MCP 서버로 실행되므로 AI 대화에서 직접 지식을 캡처할 수 있습니다. Claude Code, ChatGPT, Cursor 또는 모든 MCP 클라이언트에 연결한 다음 다음과 같이 요청하세요:

> "커넥션 풀링에 대해 방금 알아낸 것을 내 위키에 저장해줘"

> "이 디버깅 세션의 주요 결정사항을 캡처해줘"

`wiki_capture` 도구는 LLM을 통해 대화 텍스트에서 지식 항목(결정, 발견, 수정)을 추출하고, 소스 파일로 작성하며, 컴파일 대기열에 넣습니다. 잡담, 재시도, 막다른 길 같은 잡음은 자동으로 필터링됩니다.

단일 사실의 경우 `wiki_learn`이 직접 저장합니다. 전체 문서의 경우 `wiki_add_source`가 파일을 수집합니다. `wiki_compile`을 실행하여 모든 것을 문서로 처리합니다.

전체 설정 가이드를 참조하세요: [에이전트 메모리 레이어 가이드](docs/guides/agent-memory-layer.md)

## 팀 설정

sage-wiki는 1인 위키에서 3-50명 팀을 위한 공유 지식 베이스까지 확장됩니다. 세 가지 배포 패턴:

**Git 동기화 저장소** (3-10명) — 위키가 Git 저장소에 있습니다. 모든 사람이 클론하고, 로컬에서 컴파일하며, 푸시합니다. 컴파일된 `wiki/` 디렉토리는 추적되고, 데이터베이스는 `.gitignore`되어 각 컴파일마다 재구축됩니다.

**공유 서버** (5-30명) — 웹 UI와 함께 서버에서 sage-wiki를 실행합니다. 팀원들은 브라우저에서 탐색하고 MCP over SSE를 통해 에이전트를 연결합니다.

**허브 연합** (멀티 프로젝트) — 각 프로젝트가 자체 위키를 가집니다. 허브 시스템이 이를 `sage-wiki hub search`를 통해 단일 검색 인터페이스로 연합합니다.

```bash
# 허브: 여러 위키를 등록하고 검색
sage-wiki hub add /projects/backend-wiki
sage-wiki hub add /projects/ml-wiki
sage-wiki hub search "deployment process"
```

**팀이 얻는 것:**

- **축적되는 조직 메모리.** 한 에이전트가 배운 것을 모든 에이전트가 알게 됩니다. 어떤 세션에서 캡처된 결정, 관례, 함정이 모든 사람에게 검색 가능합니다.
- **신뢰 게이트 출력.** [출력 신뢰 시스템](docs/guides/output-trust.md)은 근거가 검증되고 합의가 확인될 때까지 LLM 답변을 격리합니다. 한 에이전트의 환각이 공유 코퍼스를 오염시킬 수 없습니다.
- **에이전트 스킬 파일.** 생성된 지시가 각 팀원의 AI 에이전트에게 언제 위키를 확인하고, 무엇을 캡처하며, 어떻게 쿼리할지 가르칩니다. Claude Code, Cursor, Windsurf, Codex, Gemini를 지원합니다.
- **사용자별 구독 인증.** 각 개발자가 자신의 LLM 구독을 사용합니다 — 저장소에 공유 API 키가 없습니다. 설정에는 `auth: subscription`이라고 쓰고, 자격 증명은 `~/.sage-wiki/auth.json`에 사용자별로 저장됩니다.
- **완전한 감사 추적.** `auto_commit: true`는 모든 컴파일마다 git 커밋을 생성합니다. 누가 무엇을 언제 변경했는지 기록됩니다.

```yaml
# 추천 팀 설정
trust:
  include_outputs: verified    # 검증될 때까지 격리
compiler:
  default_tier: 1              # 빠르게 인덱싱, 필요할 때 컴파일
  auto_commit: true            # 감사 추적
```

소스 구성, 에이전트 통합 워크플로우, 지식 캡처 파이프라인, 확장 고려사항, 스타트업, 연구소, Obsidian 볼트 팀을 위한 즉시 사용 가능한 레시피에 대해서는 [전체 팀 설정 가이드](docs/guides/team-setup.md)를 참조하세요.

## 벤치마크

1,107개 소스(49.4 MB 데이터베이스, 2,832개 위키 파일)로 컴파일된 실제 위키에서 평가되었습니다.

프로젝트에서 `python3 eval.py .`를 실행하여 재현할 수 있습니다. 자세한 내용은 [eval.py](eval.py)를 참조하세요.

### 성능

| 작업                            |   p50 |   처리량 |
| ------------------------------------ | ----: | -----------: |
| FTS5 키워드 검색 (상위 10)         | 411µs |    1,775 qps |
| 벡터 코사인 검색 (2,858 × 3072d) |  81ms |       15 qps |
| 하이브리드 RRF (BM25 + 벡터)           |  80ms |       16 qps |
| 그래프 탐색 (BFS 깊이 ≤ 5)      |   1µs |     738K qps |
| 순환 감지 (전체 그래프)         | 1.4ms |            — |
| FTS 삽입 (배치 100)               |     — |    89,802 /s |
| 지속적 혼합 읽기                |  77µs | 8,500+ ops/s |

비 LLM 컴파일 오버헤드(해싱 + 의존성 분석)는 1초 미만입니다. 컴파일러의 실제 소요 시간은 전적으로 LLM API 호출에 의해 결정됩니다.

### 품질

| 지표                    |     점수 |
| ------------------------- | --------: |
| 검색 재현율@10          |  **100%** |
| 검색 재현율@1           |     91.6% |
| 소스 인용률      |     94.6% |
| 별칭 커버리지            |     90.0% |
| 사실 추출률      |     68.5% |
| 위키 연결성         |     60.5% |
| 교차 참조 무결성 |     50.0% |
| **전체 품질 점수** | **73.0%** |

### 평가 실행

```bash
# 전체 평가 (성능 + 품질)
python3 eval.py /path/to/your/wiki

# 성능만
python3 eval.py --perf-only .

# 품질만
python3 eval.py --quality-only .

# 기계 판독 가능한 JSON
python3 eval.py --json . > report.json
```

Python 3.10+ 필요. 약 10배 빠른 벡터 벤치마크를 위해 `numpy`를 설치하세요.

### 테스트 실행

```bash
# 전체 테스트 스위트 실행 (합성 픽스처 생성, 실제 데이터 불필요)
python3 -m unittest eval_test -v

# 독립형 테스트 픽스처 생성
python3 eval_test.py --generate-fixture ./test-fixture
python3 eval.py ./test-fixture
```

24개 테스트 포함: 픽스처 생성, CLI 모드(`--perf-only`, `--quality-only`, `--json`), JSON 스키마 검증, 점수 범위, 검색 재현율, 엣지 케이스(빈 위키, 대용량 데이터셋, 누락된 경로).

## 아키텍처

![Sage-Wiki Architecture](sage-wiki-architecture.png)

- **스토리지:** FTS5 (BM25 검색) + BLOB 벡터 (코사인 유사도) + 소스별 티어/상태 추적을 위한 compile_items 테이블이 있는 SQLite
- **온톨로지:** BFS 탐색과 순환 감지를 갖춘 타입화된 엔티티-관계 그래프
- **검색:** 청크 수준 FTS5 + 벡터 인덱싱, LLM 쿼리 확장, LLM 재순위 매기기, RRF 퓨전, 4신호 그래프 확장을 갖춘 향상된 파이프라인. 검색 응답은 온디맨드 컴파일을 위해 컴파일되지 않은 소스를 알립니다.
- **컴파일러:** 적응형 백프레셔, 동시 Pass 2 추출, 프롬프트 캐싱, Batch API (Anthropic + OpenAI + Gemini), 비용 추적, MCP를 통한 온디맨드 컴파일, 품질 스코어링, 캐스케이드 인식을 갖춘 계층화된 파이프라인 (Tier 0: 인덱스, Tier 1: 임베드, Tier 2: 코드 파싱, Tier 3: 전체 LLM 컴파일). 임베딩은 지수 백오프를 통한 재시도, 선택적 속도 제한, 긴 입력에 대한 평균 풀링을 포함합니다. 10개 내장 코드 파서 (go/ast를 통한 Go, 정규식을 통한 8개 언어, 구조화된 데이터 키 추출).
- **MCP:** stdio 또는 SSE를 통한 17개 도구 (읽기 6, 쓰기 9, 복합 2), 온디맨드 컴파일을 위한 `wiki_compile_topic`과 지식 추출을 위한 `wiki_capture` 포함
- **TUI:** 티어 분포 표시를 갖춘 bubbletea + glamour 4탭 터미널 대시보드 (탐색, 검색, Q&A, 컴파일)
- **웹 UI:** 빌드 태그(`-tags webui`)를 사용하여 `go:embed`로 임베딩된 Preact + Tailwind CSS
- **Scribe:** 대화에서 지식을 수집하기 위한 확장 가능한 인터페이스. 세션 스크라이브는 Claude Code JSONL 트랜스크립트를 처리합니다.

Zero CGO. 순수 Go. 크로스 플랫폼.

## 라이선스

MIT
