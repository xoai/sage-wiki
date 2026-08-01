# Golden Corpus (SPEC-09)

28 source documents exercising the compile/search/graph surface. Every
file is original content written for this corpus. The suite builds
these into a workspace from scratch and compares byte/graph/search
output against committed goldens.

## What each group exercises

| Group | Files | Exercises |
|---|---|---|
| `notes/` | wiki-links.md, frontmatter.md, link-integrity.md, tier-model.md, cache-economics.md | wikilink extraction + strip pass, YAML frontmatter, cross-ref integrity, tier docs |
| `unicode/` | cjk.md, rtl-arabic.md, emoji.md, mixed-scripts.md | CJK (zh/ja/ko), RTL (ar/he), emoji/ZWJ tokenization, script boundaries |
| `text/` | plain.txt, distributed-systems.txt | non-markdown sources |
| `code/` | example.go, example.py, example.js, example_test.go | Tier-2 code extraction (Go, Python, JS) |
| `contradiction/` | fact-v1.md (2024), fact-v2.md (2025, later `date:`) | bi-temporal invalidation: v2 supersedes v1 |
| `alias/` | kubernetes-k8s.md, k8s-usage.md, api-gateway.md | alias-rich entity resolution (Kubernetes/K8s, gateway/front door) |
| `multihop/` | a-links-b.md, b-links-c.md, c-terminal.md, delta-observability.md | Alpha→Beta→Gamma graph-hop chain |
| `adversarial/` | instruction-lookalike.md, system-prompt-bait.md | injection-lookalike content that must stay content |
| `dates/` | dated-2024.md (frontmatter `date:`), undated-notes.md | frontmatter-date provenance vs undated docs |

## Add a corpus document

1. Write the file under the right group (original content only, or
   license-noted).
2. Add its row to the table above.
3. `SAGE_PARITY_FORCE=1 make record-fixtures` (updates LLM fixtures for
   the new content), then `SAGE_PARITY_FORCE=1 make regen-goldens`.
4. Commit everything with a "Golden changes" section in the PR body
   explaining every diff category.
