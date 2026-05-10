# Contributing to sage-wiki

## Adding a file format parser

### Go (built-in)

1. Add a new case in `internal/extract/extract.go` matching the file extension
2. Implement the extraction function returning plain text content
3. Add tests in `internal/extract/extract_test.go`

### External (subprocess)

1. Write a parser script that reads file content from stdin and writes plain text to stdout
2. Create `parsers/parser.yaml` in your project or pack with the extension mapping:
   ```yaml
   parsers:
     - extensions: [".docx"]
       command: python3
       args: ["docx_parser.py"]
       timeout: 30s
   ```
3. Place the script in `parsers/` and ensure it's executable (relative paths in `command` and `args` are resolved against `parsers/`)
4. Enable external parsers in config: `parsers: { external: true }`

## Creating a pack

### Quick start

```bash
sage-wiki pack create my-pack
cd my-pack
# edit pack.yaml, add prompts, skills, samples
sage-wiki pack validate
```

### Pack directory structure

```
my-pack/
├── pack.yaml              # required — manifest
├── prompts/               # optional — prompt templates
│   └── summarize.txt
├── skills/                # optional — skill template files
├── parsers/               # optional — external parser scripts
│   ├── parser.yaml        # extension mappings
│   └── convert.py         # parser script
├── samples/               # optional — example source files
│   └── example.md
└── README.md              # optional — documentation
```

### Testing your pack

```bash
# validate schema and file references
sage-wiki pack validate ./my-pack

# install locally and apply to a test project
sage-wiki pack install ./my-pack
sage-wiki pack apply my-pack --mode merge

# verify config and ontology changes
sage-wiki status
```

### Submitting to the registry

1. Fork the [sage-wiki-packs](https://github.com/xoai/sage-wiki-packs) repository
2. Add your pack directory under `packs/`
3. Add an entry to `index.yaml`:
   ```yaml
   - name: my-pack
     version: 1.0.0
     description: Short description of what this pack does
     tier: community
     tags: [domain, keywords]
   ```
4. Run `sage-wiki pack validate packs/my-pack` to verify
5. Submit a pull request — CI validates the pack automatically

## Pack schema reference

### pack.yaml fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Kebab-case identifier (`^[a-z][a-z0-9-]*$`) |
| `version` | string | yes | Semantic version (e.g., `1.0.0`) |
| `description` | string | yes | One-line description |
| `author` | string | yes | Author name or organization |
| `license` | string | no | License identifier (e.g., `MIT`) |
| `min_version` | string | no | Minimum sage-wiki version required |
| `tags` | string[] | no | Discovery tags |
| `homepage` | string | no | URL to project homepage |
| `config` | map | no | Config overlay (merged into project config.yaml) |
| `ontology.entity_types` | object[] | no | Entity type definitions |
| `ontology.relation_types` | object[] | no | Relation type definitions |
| `article_fields` | string[] | no | Custom article metadata fields |
| `prompts` | string[] | no | Prompt template filenames in `prompts/` |
| `skills` | string[] | no | Skill template filenames in `skills/` |
| `parsers` | string[] | no | Parser script filenames in `parsers/` |
| `samples` | string[] | no | Sample source filenames in `samples/` |

### Config overlay

Only these top-level config keys are allowed in pack config overlays:

- `compiler` — compilation settings (default_tier, etc.)
- `search` — search and retrieval settings
- `linting` — linting rules
- `ontology` — ontology configuration
- `trust` — output trust settings
- `type_signals` — type signal configuration
- `ignore` — ignore patterns

Keys like `api`, `embed`, `models`, `parsers`, `serve`, `vault`, `sources`,
`output`, and `project` are stripped for security.

### Entity and relation types

```yaml
ontology:
  entity_types:
    - name: finding
      description: A research finding or result
  relation_types:
    - name: cites
      synonyms: [references, builds_on]
      valid_sources: [article]  # optional
      valid_targets: [article]  # optional
```

### Apply modes

- **merge** (default) — fill-only: pack values apply only where the project has no value. Existing files are skipped as conflicts.
- **replace** — overwrites existing values and files.
