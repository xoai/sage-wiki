## Product Management — domain triggers

### When to search the wiki

- Before writing a PRD → check for existing requirements and decisions
- "What user problem does this solve?" → search for user stories
- Prioritizing features → search for hypotheses and validation results
- Reviewing metrics → search for success criteria from past PRDs
- Planning a sprint → search for blockers and dependencies

### Domain-specific queries

- Find user stories: `wiki_search("onboarding user story")`
- Check what a feature addresses: `wiki_ontology_query` with relation "addresses"
- Find validated hypotheses: `wiki_search("hypothesis validated")`
- Check priorities: `wiki_ontology_query` with relation "prioritizes"
- Find blockers: `wiki_ontology_query` with relation "blocks"

### What to capture

- Product decision → `wiki_learn` type "decision" with rationale and alternatives
- User story → `wiki_learn` type "user-story" in As-a/I-want/So-that format
- Hypothesis result → `wiki_learn` type "hypothesis" with outcome
- Metric definition → `wiki_learn` type "metric" with target and measurement method
- Customer feedback insight → `wiki_capture` from interview or support notes
