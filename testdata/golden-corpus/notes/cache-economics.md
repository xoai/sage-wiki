# Cache Economics

Prefix caching reuses the shared system prompt across calls in a
compile pass, cutting input-token spend substantially on long
documents. Cache-aware pricing must distinguish cache-read from
cache-write tokens: they are billed at different rates by providers
that expose the split.
