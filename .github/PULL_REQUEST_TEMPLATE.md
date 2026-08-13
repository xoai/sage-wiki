## Checklist

- [ ] `make ci` passes on this branch (formatting, module checks, builds, vet, new-issue lint, responsibility validation, determinism, generated/API/skill drift, translation checks, non-race tests) — and `make ci-race` for concurrency-sensitive changes
- [ ] Hosted `CI required` is green on the latest PR SHA (a green local `make ci` is mandatory but not a substitute; stale hosted results do not count)
- [ ] If `README.md` changed: the six `docs/translations/README_*.md` translations are updated, or a commit carries `translations: lag-ok`
- [ ] User-facing changes are documented (CHANGELOG `[Unreleased]`, README, affected guides — all README translations for user-visible behavior)
- [ ] Tests added or updated for the change

## For maintainers

- [ ] CI has *run and passed* on this PR (fork PRs from first-time contributors sit at `action_required` — approve the workflows first; zero checks shown ≠ green)
