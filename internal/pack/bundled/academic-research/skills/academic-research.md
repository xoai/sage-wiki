## Academic Research — domain triggers

### When to search the wiki

- Before citing a claim → verify it exists in the knowledge base
- "What's the state of the art in X?" → search for recent findings
- Discussing methodology → check for documented approaches
- Encountering a contradiction → search for conflicting findings
- Writing a literature review → search broadly, then follow citations

### Domain-specific queries

- Find related work: `wiki_search("attention mechanism")`
- Trace citations: `wiki_ontology_query` with entity + relation "cites"
- Find contradictions: `wiki_ontology_query` with relation "contradicts"
- Check methodology: `wiki_search("methodology randomized control")`
- Find prerequisites: `wiki_ontology_query` with relation "prerequisite_of"

### What to capture

- New research finding → `wiki_learn` type "finding" with evidence summary
- Methodology description → `wiki_learn` type "methodology"
- Hypothesis to test → `wiki_learn` type "hypothesis"
- Paper-to-paper citation → captured automatically via prompts during compile
- Contradictory evidence → `wiki_learn` type "contradiction" referencing both sources
