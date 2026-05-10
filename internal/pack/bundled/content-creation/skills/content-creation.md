## Content Creation — domain triggers

### When to search the wiki

- Before writing a new piece → check for existing coverage and avoid duplication
- Researching a topic → search for source material and references
- Updating published content → search for what's changed since publication
- Planning a content series → search for related pieces and gaps
- Fact-checking a draft → search for sources that support or contradict claims

### Domain-specific queries

- Find related content: `wiki_search("microservices migration")`
- Find source references: `wiki_ontology_query` with relation "references"
- Check revisions: `wiki_ontology_query` with relation "revises"
- Find series connections: `wiki_ontology_query` with relation "series_with"
- Browse drafts: `wiki_list` with entity type "draft"

### What to capture

- Editorial decision → `wiki_learn` type "editorial" with style rationale
- Source reference → captured automatically via prompts during compile
- Content gap identified → `wiki_learn` type "gap" with topic and priority
- Audience insight → `wiki_learn` type "audience" with persona details
- Outline or structure → `wiki_capture` from planning notes
