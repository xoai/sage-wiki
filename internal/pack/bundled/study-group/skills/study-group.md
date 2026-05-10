## Study Group — domain triggers

### When to search the wiki

- Before studying a new chapter → check for prerequisites
- "What do I need to know before learning X?" → search for prerequisite chains
- Reviewing for an exam → search for definitions and key concepts
- Working on an exercise → search for related examples and explanations
- Encountering an unfamiliar term → search for the definition

### Domain-specific queries

- Find prerequisites: `wiki_ontology_query` with relation "prerequisite_of"
- Find definitions: `wiki_search("binary search tree definition")`
- Find examples: `wiki_ontology_query` with relation "example_of"
- Find explanations: `wiki_ontology_query` with relation "explains"
- Browse topics: `wiki_list` with entity type "topic" or "definition"

### What to capture

- New definition learned → `wiki_learn` type "definition" with formal statement
- Worked example → `wiki_learn` type "example" with problem and solution
- Prerequisite relationship discovered → captured automatically via prompts
- Study question → `wiki_learn` type "question" for review
- Concept explanation in your own words → `wiki_capture` to index your understanding
