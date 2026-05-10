## Software Engineering — domain triggers

### When to search the wiki

- Before modifying a public API or cross-module interface
- Before refactoring or redesigning a component
- "Why is it done this way?" → search for ADRs
- When an incident occurs → search for related runbooks
- Before adding a new dependency → check existing patterns

### Domain-specific queries

- Find ADRs: `wiki_search("authentication decision")`
- Check dependencies: `wiki_ontology_query` with relation "depends_on"
- Find implementations: `wiki_ontology_query` with relation "implements"
- Check deprecations: `wiki_ontology_query` with relation "deprecates"
- Find runbooks: `wiki_search("high memory runbook")`

### What to capture

- Architecture decision → `wiki_learn` type "decision" with context and alternatives
- Non-obvious bug fix → `wiki_learn` type "gotcha" with root cause
- Established convention → `wiki_learn` type "convention"
- Runbook or procedure → `wiki_capture` from incident response notes
- API contract change → `wiki_learn` type "breaking-change" with migration path
