[English](../../README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | **한국어** | [Tiếng Việt](README_vi.md) | [Français](README_fr.md) | [Русский](README_ru.md)

<!-- translations: may-lag -->
> ⚠️ 이 번역은 README.md보다 뒤처질 수 있습니다 — 영어 버전이 정본입니다.

# sage-wiki

**sage-wiki**는 AI 에이전트와 사람이 함께 구축하고 질의하는 그래프 메모리이자 지식 베이스입니다. 문서를 넣기만 하면 LLM 컴파일러가 이를 지식 그래프를 갖춘 상호 연결된 위키로 변환합니다 — 에이전트는 MCP를 통해 질의하고, 사람은 일반 마크다운으로 탐색합니다. 옵트인 그래프 패스를 활성화하면 *근거가 있는* 그래프가 됩니다: 타입화된 엔티티, 출처를 담은 관계, 해소된 별칭, 그리고 답변의 사실 단위 인용. 하나의 Go 바이너리로 개인 볼트에서 팀 허브, 회사 지식 그래프까지 확장됩니다.

**→ 시작하기: [설치](#설치) · [빠른 시작](#빠른-시작)**

LLM으로 컴파일되는 개인 지식 베이스라는 [Andrej Karpathy의 아이디어](https://x.com/karpathy/status/2039805659525644595)에서 출발했으며, [Sage Framework](https://github.com/xoai/sage)로 만들어졌습니다. 그 과정에서 배운 교훈들은 [여기](https://x.com/xoai/status/2040936964799795503)에서 확인할 수 있습니다.

- **인용이 있는 그래프 메모리.** `wiki_graph_query`를 통해 관계형 질문을 하세요 — 답변은 직렬화된 그래프 엣지에만 근거합니다. 근거 그래프가 활성화되면 각 인용에 소스 문서와 신뢰도가 함께 담깁니다.
- **에이전트와 사람 모두를 위한 설계.** 19개의 MCP 도구와 생성된 스킬 파일이 에이전트에게 언제 검색하고, 캡처하고, 컴파일할지 가르칩니다. 사람은 같은 데이터 위에서 Obsidian 네이티브 마크다운, TUI, 웹 UI를 사용합니다.
- **신뢰와 출처.** 쿼리 출력은 검증될 때까지 격리되며, 근거가 있는 모든 관계는 어떤 문서가 그것을 주장했는지 기록합니다.
- **소스를 넣으면 위키가 나옵니다.** 컴파일 파이프라인이 논문, 노트, 코드, 이메일을 읽고, 요약하고, 개념을 추출하고, 상호 연결된 문서를 작성합니다 — 위의 모든 것을 떠받치는 수집 레이어입니다. 새로운 소스가 추가될 때마다 기존 문서가 풍부해지며, 위키는 성장할수록 축적됩니다.
- **위키에 질문하세요.** LLM 쿼리 확장, 재순위 매기기, 그래프 인식 컨텍스트 조합을 갖춘 청크 수준 하이브리드 검색이 인용이 달린 답변을 반환합니다.
- **100K+ 문서까지 확장.** 계층화된 컴파일이 모든 것을 빠르게 인덱싱하고 LLM 예산은 중요한 곳에만 사용합니다.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_외곽 경계의 점들은 지식 베이스에 있는 모든 문서의 요약을 나타내고, 안쪽 원의 점들은 지식 베이스에서 추출된 개념을 나타내며, 링크는 그 개념들이 서로 어떻게 연결되는지를 보여줍니다._

## 개인 볼트에서 회사 지식 그래프까지

- **개인** — 기존 Obsidian 볼트에 오버레이하고(`init --vault`), [로컬 모델](../guides/local-models.md)로 비용 없이 실행하며, 근거 그래프가 필요할 때 그래프 패스(`ontology.triples` + `ontology.resolve`)를 옵트인하세요.
- **팀** — git 또는 [셀프 호스팅 서버](../guides/self-hosted-server.md)로 하나의 위키를 공유하고, 엔티티 해소 제안과 [출력 신뢰](../guides/output-trust.md)를 함께 검토하며, 허브로 여러 위키를 연합하세요. [팀 설정](../guides/team-setup.md)을 참조하세요.
- **회사** — 스토리지를 [PostgreSQL/pgvector](../guides/storage-backends.md)로 옮기고, [메트릭](../guides/metrics.md)을 켜고, 서버 앞단에 인증을 두고, [계층화된 컴파일](../guides/large-vault-performance.md)로 수집을 확장하세요.

## 지식 그래프와 그래프 메모리

![sage-wiki 그래프 엔진](../../assets/sage-wiki-graph-engine.png)

벡터 검색은 질의와 *비슷해 보이는* 구절을 찾아옵니다. 그래프는 여기에 더해 **사물이 어떻게 연결되는지**를 저장하므로, 두세 단계를 거쳐야 하는 질문도 하나의 청크가 전체 사슬을 담고 있기를 기대하는 대신 순회로 답할 수 있습니다. sage-wiki는 이 그래프를 컴파일 산출물로 만듭니다 — 따로 동기화해야 하는 두 번째 데이터베이스가 아닙니다.

- **엔티티와 타입이 있는 관계.** 컴파일마다 엔티티(개념·출처·산출물)를 추출하고 타입이 지정된 관계로 연결합니다. 관계 어휘는 직접 정의합니다 —
  [설정 가능한 관계](../guides/configurable-relations.md) 참고.
- **근거가 붙은 간선.** 관계는 `evidence`(근거가 되는 구절), `confidence`(0–1), `source_doc`을 가질 수 있어, 결론을 문서 단위가 아니라 그 간선을 뒷받침한 문장까지 추적할 수 있습니다.
- **트리플.** 선택적 구조화 출력 패스가 주어 → 관계 → 목적어를 직접 추출합니다. 명시적 활성화(`ontology.triples`): 문서당 LLM 호출이 하나 늘어나므로, 기본값이 사용자의 키를 말없이 쓰지 않습니다.
- **엔티티 해소.** “K8s”와 “Kubernetes”를 한 노드로 합칩니다. 제안은 기본적으로 검토를 거치며 조용히 병합되지 않습니다.

**그래프는 부가 화면이 아니라 검색 채널입니다.** 모든 검색은 세 채널(어휘 BM25·벡터·그래프 근접)을 융합합니다: 질의어가 엔티티를 촉발하고, 제한된 순회가 그 이웃을 순위화하며, `search.hybrid_weight_graph`에서 융합됩니다. 온톨로지가 비어 있으면 비용이 없고 결과는 바이트 단위로 동일합니다.

직접 질의하거나, MCP를 통해 에이전트가 하도록 맡길 수 있습니다:

```bash
sage-wiki ontology query --entity kubernetes --depth 3 --direction both
sage-wiki provenance "service mesh"    # 이 개념을 만든 출처
```

엣지는 이중 시간(bi-temporal)입니다: 사실이 바뀌면 이전 엣지가 충돌 대신 무효화되고, 기본 답변은 모순이 없으며, `as_of` 쿼리로 "1월에는 무엇을 사실로 믿었는가"를 물을 수 있습니다. 모호한 모순은 여전히
[출력 신뢰](../guides/output-trust.md) 검토로 드러납니다. 코퍼스 전반의 질문("전체의 주요 주제는 무엇인가?")에는 옵트인 커뮤니티 탐지(`ontology.communities.enabled`)가 캐시된 커뮤니티 요약을 생성하고 `wiki_graph_query` `mode: "global"`로 답합니다. 자세한 내용:
[그래프 메모리](../guides/graph-memory.md).

## 가이드

| 가이드 | 설명 |
|-------|-------------|
| [에이전트 메모리 레이어](../guides/agent-memory-layer.md) | MCP 설정, 스킬 파일, 캡처 워크플로우, 읽기-캡처-진화 루프 |
| [HTTP API](../guides/http-api.md) | /v1 REST 표면: 인증, 에러 모델, 멱등성, 비동기 작업 |
| [그래프 메모리](../guides/graph-memory.md) | 근거가 있는 관계, 트리플 추출, 엔티티 해소, 그래프 QA |
| [설정](../guides/configuration.md) | 전체 주석이 달린 config.yaml, 멀티 프로바이더 설정, serve 워커 |
| [팀 설정](../guides/team-setup.md) | Git 동기화, 공유 서버, 허브 연합 배포 패턴 |
| [검색 품질](../guides/search-quality.md) | 청크 인덱싱, 쿼리 확장, 재순위 매기기, 그래프 확장, ANN |
| [대규모 볼트 성능](../guides/large-vault-performance.md) | 계층화된 컴파일, 백프레셔, 코드 파서, 100K+ 스케일링 |
| [출력 신뢰](../guides/output-trust.md) | 근거 검증, 합의, 승격/강등 라이프사이클 |
| [구독 인증](../guides/subscription-auth.md) | OAuth 로그인, 토큰 가져오기, 자격 증명 관리 |
| [셀프 호스팅 서버](../guides/self-hosted-server.md) | Docker Compose, Syncthing, 리버스 프록시, VPS 배포 |
| [스토리지 백엔드](../guides/storage-backends.md) | SQLite vs PostgreSQL/pgvector 설정, 전환, 풀 크기 조정 |
| [설정 가능한 관계](../guides/configurable-relations.md) | 커스텀 온톨로지 유형, 다국어 동의어, 유형 제한 |
| [프롬프트 커스터마이징](../guides/customizing-prompts.md) | 프롬프트 스캐폴딩, 유형별 재정의, 커스텀 프론트매터 필드 |
| [로컬 모델](../guides/local-models.md) | Ollama 설정, GPU/CPU 라우팅, 패스별 모델 설정 |
| [메트릭](../guides/metrics.md) | 로그 스냅샷, /metrics 엔드포인트, 카디널리티 제어 |
| [기여 팩](../../CONTRIBUTING.md) | 팩 생성, 파서 작성, 레지스트리 제출 |

## 설치

```bash
# CLI 전용 (웹 UI 없음)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# 웹 UI 포함 (프론트엔드 에셋 빌드를 위해 Node.js 필요)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## 빠른 시작

![컴파일러 파이프라인](../../assets/sage-wiki-compiler-pipeline.png)

### 새 프로젝트 (Greenfield)

```bash
sage-wiki init my-wiki && cd my-wiki
# raw/에 소스 추가
cp ~/papers/*.pdf raw/
# config.yaml을 편집하여 API 키 추가 및 LLM 선택
sage-wiki compile                                  # 첫 컴파일
sage-wiki search "attention mechanism"             # 하이브리드 검색
sage-wiki query "How does flash attention work?"   # 인용이 달린 Q&A
sage-wiki tui                                      # 터미널 대시보드
sage-wiki serve --ui                               # 브라우저 (webui 빌드)
sage-wiki compile --watch                          # 폴더 감시
```

모든 `config.yaml` 키에 한 줄씩 주석이 달려 있습니다: [설정](../guides/configuration.md).

**프로젝트 구조** (`init`이 생성하는 것 — 일부 항목, 예시이며 전체는 아님):

```
my-wiki/
├── config.yaml           # 프로바이더, 모델, 컴파일러, 검색, 온톨로지
├── raw/                  # 소스를 여기에 추가 (기사, 논문, 코드, 이미지)
├── wiki/                 # 컴파일 출력 — Obsidian 호환 마크다운
│   ├── summaries/        # 소스별 LLM 요약
│   ├── concepts/         # 개념 문서 (지식 그래프)
│   ├── images/           # 비전 캡션 이미지 설명
│   ├── outputs/          # 파일링된 질의 응답 (trust.include_outputs: "true")
│   ├── under_review/     # 검토 대기 중인 응답 (기본값)
│   └── archive/          # 정리된 문서
├── .sage/wiki.db         # 단일 SQLite 파일: FTS 인덱스, 벡터, 온톨로지, 큐
└── .manifest.json        # 소스↔문서 매핑 + 컴파일 상태
```

### 볼트 오버레이 (기존 Obsidian 볼트)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# config.yaml을 편집하여 소스/무시 폴더 설정, API 키 추가, LLM 선택
sage-wiki compile --watch
```

컨테이너를 선호하시나요? 미리 빌드된 멀티 아키텍처 Docker 이미지와 compose 파일은
[셀프 호스팅 서버 가이드](../guides/self-hosted-server.md)에서 다룹니다.

## 지원하는 소스 형식

| 형식        | 확장자                                  | 추출 내용                                                   |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | 프론트매터를 별도로 파싱한 본문 텍스트                      |
| PDF         | `.pdf`                                  | 순수 Go 추출을 통한 전체 텍스트                             |
| Word        | `.docx`                                 | XML에서 추출한 문서 텍스트                                  |
| Excel       | `.xlsx`                                 | 셀 값과 시트 데이터                                         |
| PowerPoint  | `.pptx`                                 | 슬라이드 텍스트 내용                                        |
| CSV         | `.csv`                                  | 헤더 + 행 (최대 1000행)                                     |
| EPUB        | `.epub`                                 | XHTML에서 추출한 챕터 텍스트                                |
| 이메일      | `.eml`                                  | 헤더 (발신/수신/제목/날짜) + 본문                           |
| 일반 텍스트 | `.txt`, `.log`                          | 원시 내용                                                   |
| 자막        | `.vtt`, `.srt`                          | 원시 내용                                                   |
| 이미지      | `.png`, `.jpg`, `.gif`, `.webp`, `.svg`, `.bmp` | 비전 LLM을 통한 설명 (캡션, 내용, 표시된 텍스트) |
| 코드        | `.go`, `.py`, `.js`, `.ts`, `.rs` 등    | 소스 코드                                                   |

소스 폴더에 파일을 넣기만 하면 sage-wiki가 형식을 자동으로 감지합니다. 이미지는 비전 지원 LLM (Gemini, Claude, GPT-4o)이 필요합니다. 목록에 없는 형식이 필요하신가요? sage-wiki는 [외부 파서](#외부-파서)를 지원합니다 — stdin을 읽고 텍스트를 stdout에 쓰는 모든 언어의 스크립트입니다.

## 그래프 메모리

기본 상태에서 위키는 키워드 근접성으로 지식 그래프를 구축합니다 —
관계 키워드가 같은 블록에서 `[[wikilink]]`와 함께 나타나는 곳의
개념들이 연결됩니다. **옵트인 그래프 패스**를 활성화하면 이것이
근거가 있는 그래프로 바뀝니다:

- **트리플 추출** (`ontology.triples.enabled`) — 완전히 컴파일된 문서당
  한 번의 추가 LLM 호출로 타입화된 엔티티와 관계를 추출하며, 각각
  근거 구간, 신뢰도, 소스 문서를 담습니다.
- **엔티티 해소** (`ontology.resolve.enabled`) — 표기 변형("NASA" /
  "National Aeronautics and Space Administration")이 정규 엔티티로
  연결됩니다. 높은 신뢰도의 제안은 자동으로 적용되며(임계값 0.85;
  검토 전용으로 하려면 정확히 `1.0`으로 설정), 모든 연결은
  `ontology resolve --unlink`로 정확히 되돌릴 수 있습니다.
- **그래프 QA** — `wiki_graph_query` MCP 도구는 *오직* 경계가 정해진
  직렬화된 엣지 집합에만 근거하여 멀티홉 관계형 질문에 답합니다.
  엣지에 근거가 있으면 인용에 `source_doc`과 `confidence`가
  담깁니다(키워드 근접성 엣지에는 둘 다 없습니다). 일반 Q&A
  컨텍스트도 각 관련 문서 아래에 연결 엣지를 명시합니다.

깊이, 비용, 검토 워크플로우, 실행 취소 의미론: [그래프 메모리](../guides/graph-memory.md).

## 명령어

핵심 명령어들입니다. 플래그는 `sage-wiki <command> --help`로 확인하세요.

| 명령어 | 설명 |
| ------- | ----------- |
| `sage-wiki init [dir] [--vault] [--skill <agent>] [--pack <name>] [--prompts] [--force]` | 프로젝트 초기화 (새 프로젝트 또는 볼트 오버레이) |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | 소스를 위키 문서로 컴파일 |
| `sage-wiki serve [--transport stdio\|sse] [--ui] [--port 3333]` | MCP 서버 / 웹 UI |
| `sage-wiki reindex [--drop-chunk-vectors]` | 현재의 `chunk_size` / `chunk_overlap_tokens` 로 디스크의 문서에서 청크 인덱스를 재구축 |
| `sage-wiki search "query" [--tags ...] [--boost-tags ...] [--limit N] [--channels bm25,vector,graph] [--expand] [--rerank]` | 하이브리드 검색 (BM25 + 벡터 + 온톨로지 그래프) |
| `sage-wiki query "question"` | 인용이 포함된 위키 기반 Q&A |
| `sage-wiki tui` | 대화형 터미널 대시보드 |
| `sage-wiki ontology <query\|list\|add\|resolve>` | 온톨로지 그래프 쿼리, 관리, 해소 |
| `sage-wiki ingest <url\|path>` / `sage-wiki add-source <path>` | 소스 추가 |
| `sage-wiki source <show\|list>` / `sage-wiki coverage` | 소스와 컴파일 커버리지 검사 |
| `sage-wiki status` / `sage-wiki doctor` / `sage-wiki diff` | 상태 점검, 설정 검증, 대기 중인 변경사항 |
| `sage-wiki lint [--fix]` / `sage-wiki list` / `sage-wiki write <summary\|article>` | 유지 보수 및 수동 작성 |
| `sage-wiki hub <init\|add\|remove\|search\|status\|list\|compile>` | 멀티 프로젝트 허브 |
| `sage-wiki learn "text"` / `sage-wiki capture "text"` / `sage-wiki scribe <session-file>` | 지식 캡처 |
| `sage-wiki skill <refresh\|preview> [--target <agent>]` | 에이전트 스킬 파일 생성 또는 갱신 |
| `sage-wiki provenance <source-or-concept>` / `sage-wiki version` | 출처 매핑, 버전 |

주제별 명령어 계열은 해당 가이드와 함께 있습니다: `pack *`은
[CONTRIBUTING](../../CONTRIBUTING.md)에, `auth *`(login, import, status, logout,
migrate)는 [구독 인증](../guides/subscription-auth.md)에,
`verify` / `outputs *`는 [출력 신뢰](../guides/output-trust.md)에 있습니다.

## TUI

```bash
sage-wiki tui
```

4개의 탭을 갖춘 풀 기능 터미널 대시보드:

- **[F1] 탐색** — 섹션별로 문서를 탐색합니다 (개념, 요약, 출력). 방향키로 선택, Enter로 glamour 렌더링된 마크다운 읽기, Esc로 뒤로 가기.
- **[F2] 검색** — 분할 창 미리보기가 있는 퍼지 검색. 입력하면 필터링되고, 결과는 하이브리드 점수로 순위가 매겨지며, Enter로 `$EDITOR`에서 엽니다.
- **[F3] Q&A** — 대화형 스트리밍 Q&A. 질문을 하면 소스 인용이 포함된 LLM 합성 답변을 받습니다. Ctrl+S로 답변을 outputs/에 저장합니다.
- **[F4] 컴파일** — 라이브 컴파일 대시보드. 소스 디렉토리의 변경사항을 감시하고 자동으로 재컴파일합니다. 미리보기로 컴파일된 파일을 탐색합니다.

탭 전환: 모든 탭에서 `F1`-`F4`, 탐색/컴파일 탭에서 `1`-`4`, `Esc`로 탐색 탭으로 돌아갑니다. 종료는 `Ctrl+C`.

## 웹 UI

```bash
sage-wiki serve --ui        # http://127.0.0.1:3333, -tags webui 빌드 필요
```

- **문서 브라우저** — 렌더링된 마크다운, 구문 강조, 클릭 가능한 `[[wikilinks]]`
- **하이브리드 검색** — 순위가 매겨진 결과와 스니펫
- **지식 그래프** — 개념과 그 연결을 보여주는 대화형 포스 다이렉티드 시각화
- **스트리밍 Q&A** — 질문을 하면 소스 인용이 포함된 LLM 합성 답변을 받습니다
- **목차** — 스크롤 스파이 포함; 시스템 설정을 감지하는 다크/라이트 모드; 깨진 문서 링크는 회색으로 표시

Preact + Tailwind로 구축되어 `go:embed`로 임베딩됩니다 (~1.2 MB, gzip 시 ~420 KB). CLI/MCP 전용 바이너리를 원하면 `-tags webui`를 생략하세요. 인증 토큰, 허용 호스트, 배포 하드닝: [셀프 호스팅 서버](../guides/self-hosted-server.md).

## MCP 통합

![MCP 통합](../../assets/sage-wiki-interfaces.png)

`.mcp.json`에 추가하세요 (Claude Code 기준; 다른 에이전트는 [에이전트 메모리 레이어 가이드](../guides/agent-memory-layer.md) 참조):

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

네트워크 클라이언트: `sage-wiki serve --transport sse --port 3333`. 서버는
19개의 도구를 노출합니다 — 검색, 읽기, 그래프 쿼리, 캡처, `wiki_query`
(검토용 파일링이 포함된 질의응답), 온디맨드 컴파일 등. 에이전트별 설정과 캡처 워크플로우는
[에이전트 메모리 레이어 가이드](../guides/agent-memory-layer.md)에 있습니다.

**에이전트 스킬 파일** — `sage-wiki skill refresh --target <agent>`는
에이전트의 지시 파일(CLAUDE.md, .cursorrules 등)에 언제 검색하고,
무엇을 캡처하고, 어떻게 쿼리할지 가르치는 동작 섹션을 작성합니다.
내용은 설정에서 파생됩니다. 대상: `claude-code`, `cursor`,
`windsurf`, `agents-md` (Antigravity), `codex`, `gemini`, `generic`.

### 에이전트 스킬

sage-wiki의 레퍼런스 스킬을 설치하면 코딩 어시스턴트가 이 README를 읽지
않고도 전체 도구 표면—19개 MCP 도구, `/v1` REST 대응, 옵트인 플래그, 티어,
비동기 컴파일 의미, 에러 코드—을 알게 됩니다:

```bash
# Claude Code
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki

# 또는 수동으로: skills/sage-wiki/SKILL.md를 .claude/skills/에 복사
```

파이프라인 스킬 `sage-wiki-integrate`는 새 리포지토리에 sage-wiki를 대화형으로
연결합니다(언어 감지 → 클라이언트 설치 또는 MCP 설정 → 저장-검색 스모크 테스트):

```bash
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki-integrate
```

두 스킬 모두 라이브 MCP 레지스트리에서 생성되며(`go run ./tools/skillgen/`)
CI에서 드리프트 검사를 받습니다 — 도구가 바뀌어도 오래되지 않습니다.
Pre-1.0 — 버전을 고정하세요.

**지식 캡처** — 에이전트는 `wiki_capture` / `wiki_learn`을 통해
인사이트를 다시 저장하여 읽기-캡처-진화 루프를 완성합니다. 워크플로우와 팁:
[에이전트 메모리 레이어](../guides/agent-memory-layer.md).

## 클라이언트 SDK

`/v1` REST API용 타입 클라이언트 (Pre-1.0 — 버전을 고정하세요):

**Python** — `pip install sagewiki` (≥3.9, `httpx`만 사용):

```python
from sagewiki import SageWiki

c = SageWiki()  # 환경 변수 SAGE_WIKI_URL / SAGE_WIKI_TOKEN
for r in c.search("attention", limit=5).results:
    print(r.final_score, r.content[:80])
job = c.compile(topic="attention")
job.wait(timeout=600)  # 명시적 타임아웃 필수
```

**TypeScript** — `npm install sagewiki` (런타임 의존성 제로, 전역
`fetch`; Node ≥18, Deno, Bun, 엣지 런타임):

```ts
import { SageWikiClient } from "sagewiki";

const c = new SageWikiClient();
const results = await c.search("attention", { limit: 5 });
const job = await c.compile({ topic: "attention" });
await job.waitUntilDone({ timeoutMs: 600_000 });
```

두 클라이언트 모두 `/v1` 표면 전체를 다룹니다: 검색, 출처, 그래프 쿼리,
컴파일된 wiki, 캡처/쓰기, 비동기 compile/lint 작업과 코드 기반 에러 분류.
문서: [Python](../../clients/python/README.md) · [TypeScript](../../clients/typescript/README.md) ·
[HTTP API 가이드](../guides/http-api.md). Go 프로그램은 HTTP를 완전히
생략할 수 있습니다 — [Go 프로그램에 임베딩](#go-프로그램에-임베딩) 참조.

### 예제

실제 서버를 대상으로 CI에서 검증되는 복사 가능한 프레임워크 통합:

- [`examples/langgraph/`](../../examples/langgraph/) — 메모리 기반 LangGraph
  노드 (Python 클라이언트): `uncompiled_sources` → 토픽 컴파일 패턴의
  검색과 캡처.
- [`examples/vercel-ai-sdk/`](../../examples/vercel-ai-sdk/) — `search`,
  `graphQuery`, `provenance`를 Vercel AI SDK 도구로 제공 (TypeScript
  클라이언트). 엣지 배포 가능.

### Go 프로그램에 임베딩

서브프로세스, stdio, 포트 관리 없이 자체 Go 프로세스에서 동일한 도구를 호출하려면 mcp-go의 인프로세스 트랜스포트와 함께 `pkg/sagewiki`를 사용하세요:

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

프로젝트가 미리 존재해야 하며 호출자가 데이터베이스 핸들을 소유하므로 `Close`가 필수입니다 — `serve`와 달리 다른 무언가가 닫아주지 않습니다. 로그는 호스트의 stderr로 출력되고, `initialize`는 sage-wiki 빌드 버전을 보고합니다(일반 `go build`에서는 `dev`). 시작 시 `sagewiki.SetVersion`을 호출하면 자체 버전 문자열을 보고할 수 있습니다.

이 패키지는 sage-wiki가 1.0 미만인 동안 **실험적**입니다: Go 시그니처는 유지될 예정이지만 도구 이름, 인자 스키마, `config.yaml` 레이아웃은 릴리스마다 바뀔 수 있습니다. 버전을 고정하세요.

## 운영

- **스토리지** — 기본은 SQLite (단일 파일, 제로 설정); 서버 배포에는
  PostgreSQL + pgvector. 전환과 풀 크기 조정: [스토리지 백엔드](../guides/storage-backends.md).
- **관측 가능성** — 구조화된 로그 스냅샷과 옵트인 `/metrics`
  엔드포인트: [메트릭](../guides/metrics.md).
- **구조화된 출력** — LLM 추출 패스는 각 프로바이더의 네이티브
  메커니즘(Anthropic 도구 사용, OpenAI `response_format`, Gemini
  `responseSchema`)을 사용하며, 검증하는 펜스 제거 폴백을 갖추고 있습니다.
- **자격 증명** — 구독 토큰은 가능한 경우 OS 키체인에 저장됩니다.
  파일에 저장된 자격 증명을 옮기려면 `sage-wiki auth migrate`를 한 번
  실행하세요. [구독 인증](../guides/subscription-auth.md).
- **설정** — 모든 키에 주석이 달려 있으며, 멀티 프로바이더 레시피와
  serve 모드 컴파일 워커 포함: [설정](../guides/configuration.md).
- **엔티티 해소** — 0.85에서 자동 적용, `--unlink`로 정확히 되돌릴 수 있습니다. 위의 [그래프 메모리](#그래프-메모리)를 참조하세요.
- **커스텀 관계/엔티티 유형** — 내장 유형을 확장하거나 직접 추가할 수
  있으며(`ontology.relation_types`), 다국어 동의어와 유형 제한을
  지원합니다: [설정 가능한 관계](../guides/configurable-relations.md).
- **출력 신뢰** — 쿼리 출력은 근거가 확인되거나, 합의로 확정되거나,
  수동으로 승격될 때까지 격리됩니다: [출력 신뢰](../guides/output-trust.md).
- **검색 튜닝** — 청킹, 확장, 재순위 매기기, 그래프 확장,
  옵트인 ANN: [검색 품질](../guides/search-quality.md).

### 비용

sage-wiki는 모든 컴파일에서 토큰 사용량을 추적하고 비용을 추정합니다.
**프롬프트 캐싱**(기본값: 켜짐)은 컴파일 패스 내에서 호출 간에 시스템
프롬프트를 재사용합니다 — Anthropic과 Gemini는 명시적으로, OpenAI는
자동으로 캐싱합니다 — 입력 토큰의 50-90%를 절약합니다. **Batch API**
(Anthropic, OpenAI, Gemini)는 대규모 컴파일의 비용을 절반으로 줄입니다:

```bash
sage-wiki compile --batch       # 배치 제출, 체크포인트, 종료
sage-wiki compile               # 상태 폴링, 완료 시 결과 수신
```

`compile --estimate`는 비용을 미리 보여주고, `compiler.mode: auto`는
임계값을 넘으면 자동으로 배치를 사용합니다. 자세한 내용: [설정](../guides/configuration.md).

### 대규모 볼트로 확장

계층화된 컴파일은 모든 것을 LLM으로 컴파일하는 대신 각 소스를
유형과 사용량에 따라 라우팅합니다:

| 티어 | 처리 내용 | 비용 | 문서당 시간 |
|------|-------------|------|-------------|
| **0** — 인덱스만 | FTS5 전체 텍스트 검색 | 무료 | ~5ms |
| **1** — 인덱스 + 임베드 | FTS5 + 벡터 임베딩 | ~$0.00002 | ~200ms |
| **2** — 코드 파싱 | 정규식 파서를 통한 구조적 요약 (LLM 없음) | 무료 | ~10ms |
| **3** — 전체 컴파일 | 요약 + 개념 추출 + 문서 작성 | ~$0.05-0.15 | ~5-8분 |

대규모 볼트의 경우: 모든 것을 Tier 1로 인덱싱한 다음(100K 문서 볼트를
~5.5시간에), 필요할 때 컴파일하세요 — 자동 승격, 백프레셔, 코드 파서는
[대규모 볼트 성능](../guides/large-vault-performance.md)에서 다룹니다.

## 에코시스템

### 기여 팩

팩은 특정 도메인을 위한 온톨로지 유형, 프롬프트, 스킬 트리거를 번들합니다.
8개의 번들 팩이 오프라인으로 작동합니다:

| 팩 | 대상 | 주요 온톨로지 |
|------|----------|-------------|
| `academic-research` | 연구자 | cites, contradicts, finding, research_hypothesis |
| `software-engineering` | 개발팀 | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | 노트 작성자 | relates_to, inspired_by, fleeting_note |
| `study-group` | 학생 | explains, prerequisite_of, definition |
| `meeting-organizer` | 관리자 | decided, assigned_to, action_item |
| `content-creation` | 작가 | references, revises, draft, published |
| `legal-compliance` | 법무팀 | regulates, supersedes, policy, control |

`sage-wiki init --pack academic-research`는 초기화 시 팩 하나를 적용하고,
`pack install <name|url>`로 더 추가할 수 있습니다. 팩 생성과 게시:
[CONTRIBUTING](../../CONTRIBUTING.md).

### 외부 파서

어떤 파일 형식이든 모든 언어의 스크립트로 처리할 수 있습니다(stdin →
stdout으로 텍스트 출력). `parsers/parser.yaml`에 선언하며 이중 옵트인
뒤에 있습니다 — 샌드박스 없는 서브프로세스로 실행되지만 타임아웃 강제와
환경 변수 제거가 적용됩니다. 작성과 하드닝 세부사항: [CONTRIBUTING](../../CONTRIBUTING.md);
신뢰 경계에 대한 논의: [팀 설정](../guides/team-setup.md).

### 팀

세 가지 공유 패턴 — git 동기화, 공유 서버, 허브 연합 — 그리고
팀 신뢰 검토와 비용 관리: [팀 설정](../guides/team-setup.md).

## 벤치마크

두 스위트는 서로 다른 질문에 답합니다. 자세한 내용:
[eval/benchmarks/REPORT.md](../../eval/benchmarks/REPORT.md) · [eval/REPORT.md](../../eval/REPORT.md)

**메모리 벤치마크** — 긴 대화에 대한 질문에 답할 수 있는가. 공개 데이터셋을 LLM이 채점하며,
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks) 의 프롬프트와 절차를 그대로 쓰고 백엔드만 sage-wiki로 교체했습니다(응답·판정 모두 gpt-5, 표본 추출):

| 벤치마크 | Score | Mem0 Platform |
|---|---|---|
| LOCOMO (150 q) | **92.0%** @ top-50 | 91.8% @ top-50 |
| LongMemEval-S (30 q) | **93.3%** @ top-50 | 94.8% @ top-50 |
| BEAM 100K (60 q) | **0.691** mean nugget | 0.641 @ 1M |

엄밀한 순위 비교가 아닙니다: mem0는 관리형 플랫폼에서 전체 문항을 실행하고, 여기서는 표본(±4~5%p)이며 컴파일 파이프라인도 다릅니다. 유의사항은 리포트에 정리했습니다.

**품질·성능 평가** — 위키가 잘 구성되어 있고 빠른가. 컴파일된 위키라면 어디서든, API 키 없이 수 초 만에 실행됩니다. 실제 위키 10개 중앙값: 종합 **87.4%**, 사실 추출 100%, recall@10 100%, 상호 참조 무결성 100%. 인프로세스 검색: FTS5 top-10 **0.035 ms**, 하이브리드 RRF **4.9 ms**, 그래프 BFS **0.001 ms**.

```bash
python3 eval/eval.py .                      # 위키 품질 + 성능
python3 -m pytest eval/eval_test.py -q      # 하네스 자체 테스트
```

## 라이선스

MIT
