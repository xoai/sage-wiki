# Configurable Ontology Relations

sage-wiki builds a knowledge graph where concepts are connected by typed relations. By default there are 8 built-in relation types. You can extend these with additional keyword synonyms (useful for multilingual wikis) or define entirely new relation types for your domain.

## How relations work

When sage-wiki writes an article (Pass 3 of the compiler), it scans the text for `[[wikilinks]]` and checks whether any keyword synonyms appear near the linked concept. If a match is found, a typed relation edge is created in the ontology graph.

For example, if an article about Flash Attention contains:

> Flash Attention **optimizes** the memory access pattern of [[Self-Attention]]

sage-wiki detects the keyword "optimizes" near the wikilink and creates an edge:

```
Flash Attention --optimizes--> Self-Attention
```

These edges power the knowledge graph visualization, Q&A context retrieval, and the linter's connectivity analysis.

## Built-in relation types

| Type | Keywords | Notes |
|------|----------|-------|
| `implements` | implements, implementation of, is an implementation, 实现了, 实现方式 | |
| `extends` | extends, extension of, builds on, builds upon, 扩展了, 基于 | |
| `optimizes` | optimizes, optimization of, improves upon, faster than, 优化了, 改进了, 提升了 | |
| `contradicts` | contradicts, conflicts with, disagrees with, challenges, 矛盾, 冲突, 挑战了 | |
| `cites` | *(none)* | Created programmatically when articles reference sources |
| `prerequisite_of` | prerequisite, requires knowledge of, depends on, built on top of, 前提, 依赖于, 前置条件 | |
| `trades_off` | trade-off, tradeoff, trades off, at the cost of, 取舍, 权衡, 代价是 | |
| `derived_from` | *(none)* | Created programmatically by the Q&A system when filing answers |

Built-in types are always present. They cannot be removed, but you can add synonyms to them.

## Configuration

Add an `ontology` section to your `config.yaml`:

```yaml
ontology:
  relations:
    # Extend a built-in type with additional synonyms
    - name: implements
      synonyms: ["thực hiện", "triển khai"]

    # Add a domain-specific custom type
    - name: regulates
      synonyms: ["regulates", "regulated by", "调控", "调节"]
```

### Extending a built-in type

When the `name` matches a built-in type, your synonyms are **appended** to the defaults. The built-in synonyms are never replaced.

```yaml
ontology:
  relations:
    - name: implements
      synonyms: ["thực hiện", "triển khai"]
```

After merging, `implements` will match all of its original English and Chinese keywords **plus** the Vietnamese ones you added.

This is useful for:
- **Multilingual wikis** — add keywords in your language so the compiler detects relations in non-English content
- **Domain jargon** — add field-specific phrases that mean the same thing (e.g., "is a subclass of" for `extends`)

### Adding a custom type

When the `name` doesn't match any built-in, a new relation type is created:

```yaml
ontology:
  relations:
    - name: regulates
      synonyms: ["regulates", "regulated by", "调控", "调节"]
    - name: inspired_by
      synonyms: ["inspired by", "draws from", "influenced by"]
```

Custom types work exactly like built-in types: the compiler scans for their keywords near wikilinks and creates ontology edges. They appear in the knowledge graph, are queryable via the MCP tools, and are validated on input.

### Naming rules

Relation names must:
- Start with a lowercase letter
- Contain only lowercase letters, digits, and underscores
- Match the pattern `^[a-z][a-z0-9_]*$`

Valid: `regulates`, `inspired_by`, `part_of`, `v2_replaces`
Invalid: `Regulates`, `has-effect`, `part of`, `123abc`

Invalid names are rejected at config load time with a clear error message.

## Examples by domain

### Biology / Life Sciences

```yaml
ontology:
  relations:
    - name: regulates
      synonyms: ["regulates", "regulated by", "activates", "inhibits", "调控"]
    - name: encodes
      synonyms: ["encodes", "encoded by", "codes for"]
    - name: binds_to
      synonyms: ["binds to", "binding partner", "interacts with"]
```

### Software Architecture

```yaml
ontology:
  relations:
    - name: depends_on
      synonyms: ["depends on", "dependency of", "requires"]
    - name: wraps
      synonyms: ["wraps", "wrapper for", "facade for", "delegates to"]
    - name: replaces
      synonyms: ["replaces", "supersedes", "deprecated by", "successor of"]
```

### Philosophy / Humanities

```yaml
ontology:
  relations:
    - name: inspired_by
      synonyms: ["inspired by", "influenced by", "draws from", "in the tradition of"]
    - name: critiques
      synonyms: ["critiques", "critique of", "responds to", "counters"]
    - name: synthesizes
      synonyms: ["synthesizes", "synthesis of", "combines", "unifies"]
```

## How it works internally

1. **Config load** — `config.Validate()` checks relation names against the regex
2. **Merge** — `ontology.MergedRelations()` combines built-in defaults with config entries (deduplicating synonyms)
3. **Validation** — `ontology.NewStore()` receives the merged list of valid names. `AddRelation()` rejects any type not in the list
4. **Extraction** — `ontology.RelationPatterns()` builds keyword patterns from the merged list (skipping types with no synonyms like `cites` and `derived_from`). The compiler uses these patterns during article writing
5. **Migration** — Existing databases are automatically migrated on first open after upgrade. The SQL CHECK constraint is replaced with application-layer validation, so custom types can be stored

## Zero config

If you don't add an `ontology` section, behavior is identical to previous versions. The 8 built-in types work with their default synonyms. No migration issues, no breaking changes.

## MCP tools

The `wiki_add_ontology` MCP tool validates relation types through the same `AddRelation()` path. If an MCP client tries to create a relation with an unknown type, it gets a clear error:

```
ontology: unknown relation type "invalid_type"
```

This prevents invalid data from entering the knowledge graph regardless of whether relations are created by the compiler or by external tools.
