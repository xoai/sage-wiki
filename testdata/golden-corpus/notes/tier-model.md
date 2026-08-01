# Tiered Compilation

Tier 0 indexes raw sources for lexical search. Tier 1 adds embeddings
for vector retrieval. Tier 2 extracts structure from code. Tier 3 runs
the full LLM pipeline: summarize, extract concepts, write articles.
Most corpora should mix tiers by source type rather than compile
everything at Tier 3.
