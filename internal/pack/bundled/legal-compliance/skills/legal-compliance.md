## Legal Compliance — domain triggers

### When to search the wiki

- Before making a compliance decision → check for applicable regulations
- "What regulations apply to X?" → search for regulatory requirements
- Reviewing a policy → check if it's been superseded
- Preparing for an audit → search for controls and evidence requirements
- Handling personal data → search for retention and processing policies

### Domain-specific queries

- Find regulations: `wiki_search("data retention GDPR")`
- Check what regulates an activity: `wiki_ontology_query` with relation "regulates"
- Find superseded policies: `wiki_ontology_query` with relation "supersedes"
- Check compliance: `wiki_ontology_query` with relation "complies_with"
- Find obligations: `wiki_search("mandatory requirement deadline")`

### What to capture

- Regulatory requirement → `wiki_learn` type "regulation" with scope and deadline
- Policy decision → `wiki_learn` type "policy" with rationale and effective date
- Compliance control → `wiki_learn` type "control" with evidence requirements
- Audit finding → `wiki_capture` from audit report
- Obligation deadline → `wiki_learn` type "obligation" with responsible party and date
