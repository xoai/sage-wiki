## Meeting Organizer — domain triggers

### When to search the wiki

- Preparing for a meeting → search for the person, company, or topic
- "What did we decide about X?" → search for past decisions
- Following up on action items → search by assignee or topic
- Checking meeting history → search for previous meeting notes
- Onboarding someone new → search for key decisions and context

### Domain-specific queries

- Find decisions: `wiki_search("authentication decision")`
- Find action items by owner: `wiki_search("alice action item")`
- Check what's blocked: `wiki_ontology_query` with relation "blocked_by"
- Find follow-ups: `wiki_ontology_query` with relation "followed_up"
- Browse attendees: `wiki_list` with entity type "attendee"

### What to capture

- Decision made in meeting → `wiki_learn` type "decision" with rationale
- Action item assigned → `wiki_learn` type "action" with owner and deadline
- Blocker raised → `wiki_learn` type "blocker" with context
- Meeting outcome → `wiki_capture` from meeting notes
- Follow-up needed → `wiki_learn` type "follow-up" with timeline
