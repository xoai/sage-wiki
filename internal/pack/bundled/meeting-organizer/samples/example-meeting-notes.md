# Sprint Planning - 2025-03-15

## Attendees

Alice (PM), Bob (Backend), Carol (Frontend), Dave (QA)

## Agenda

1. Sprint 12 retrospective
2. Sprint 13 planning
3. Release timeline

## Decisions

- Ship v2.1 by March 28 (decided: release includes auth changes but not the dashboard redesign)
- Use feature flags for the new onboarding flow (decided: gradual rollout to 10% first)

## Action items

- [ ] Bob: migrate auth endpoints to v2 by March 20
- [ ] Carol: implement feature flag toggle UI by March 22
- [ ] Dave: write E2E tests for auth flow by March 25
- [ ] Alice: update stakeholders on revised timeline by March 16

## Blockers

- CI pipeline is flaky on the auth branch (Bob investigating)
- Design handoff for dashboard is delayed (waiting on design team)

## Follow-ups

- Revisit dashboard timeline in next sprint planning
- Check feature flag metrics after 48 hours of 10% rollout
