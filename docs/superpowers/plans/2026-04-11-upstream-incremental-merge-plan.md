# Upstream Incremental Merge Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan.

**Goal:** Merge upstream PR#25/#27/#28/#29 + eval refactor into fork, add `language: zh` config.

**Architecture:** git merge → resolve conflicts → build/test → config.yaml update → prompts check.

**Tech Stack:** Go, git

---

### Task 1: Merge

- [ ] `git merge upstream/main`
- [ ] `git diff --name-only --diff-filter=U` — check conflicts
- [ ] If conflicts: resolve (take upstream, add fork additions)
- [ ] If clean: proceed

### Task 2: Build + Test

- [ ] `go build -o sage-wiki ./cmd/sage-wiki/`
- [ ] `go test ./...`
- [ ] `go vet ./...`

### Task 3: Config.yaml Update

- [ ] Add `language: zh` to `~/claude-workspace/wiki/config.yaml`
- [ ] `./sage-wiki doctor --project ~/claude-workspace/wiki` — verify

### Task 4: Prompts Compatibility

- [ ] Check fork prompts/ templates don't contain "Output ONLY a JSON" / "Return ONLY a JSON" (would incorrectly suppress language injection)
- [ ] Verify `./sage-wiki status --project ~/claude-workspace/wiki --format json` works

### Task 5: Commit merge + verify

- [ ] Complete merge commit
- [ ] `./sage-wiki --help` — confirm commands still 21
