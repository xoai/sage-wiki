---
title: Frontmatter Semantics
tags: [meta, yaml]
custom_field: preserved
---
# Frontmatter Handling

Documents may carry YAML frontmatter. The compiler preserves unknown
fields, interprets `tags:` for search filtering, and must never let
frontmatter leak into the article body text.
