package scribe

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// maxEntitiesPerSession caps entity extraction to prevent noise.
const maxEntitiesPerSession = 10

// SessionScribe processes Claude Code session JSONL files.
// Based on @andreasbauer80's proven pattern:
// 1. Compress: strip thinking blocks (99.4% reduction)
// 2. Extract: LLM identifies entity candidates with role separation
// 3. Compare: search existing entities, disposition ADD/UPDATE/NONE
type SessionScribe struct {
	client      *llm.Client
	model       string
	ontStore    *ontology.Store
	entityTypes []string // configured valid entity types
}

// NewSessionScribe creates a session scribe.
// entityTypes are the valid entity types from config — the LLM prompt
// will list only these. If empty, defaults to standard types.
func NewSessionScribe(client *llm.Client, model string, ontStore *ontology.Store, entityTypes ...string) *SessionScribe {
	types := entityTypes
	if len(types) == 0 {
		types = []string{"concept", "technique", "source", "claim", "artifact"}
	}
	return &SessionScribe{
		client:      client,
		model:       model,
		ontStore:    ontStore,
		entityTypes: types,
	}
}

func (s *SessionScribe) Name() string { return "session" }

func (s *SessionScribe) Process(ctx context.Context, input []byte) (*Result, error) {
	result := &Result{InputSize: len(input)}

	// Step 1: Compress — strip thinking blocks
	compressed := compressSession(input)
	result.CompressedTo = len(compressed)

	if len(compressed) == 0 {
		return result, nil
	}

	// Step 2: Extract entity candidates via LLM
	candidates, err := s.extractEntities(ctx, compressed)
	if err != nil {
		return nil, fmt.Errorf("session scribe: extract: %w", err)
	}
	result.Extracted = len(candidates)

	// Step 3: Compare against existing entities — disposition each
	for _, c := range candidates {
		// Specificity gate: must have a valid kebab-case ID
		if !isKebabCase(c.ID) {
			result.Skipped++
			log.Info("scribe: skipping non-specific entity", "id", c.ID, "name", c.Name)
			continue
		}

		// Check if entity already exists
		existing, _ := s.ontStore.GetEntity(c.ID)

		if existing != nil {
			// Entity exists — check if update is warranted
			if c.Definition != "" && c.Definition != existing.Definition {
				// UPDATE: new definition differs — update it
				existing.Definition = c.Definition
				existing.UpdatedAt = c.UpdatedAt
				if err := s.ontStore.AddEntity(*existing); err != nil {
					log.Warn("scribe: update entity failed", "id", c.ID, "error", err)
				}
				result.Entities = append(result.Entities, *existing)
				result.Kept++
			} else {
				result.Skipped++ // NONE: no meaningful update
			}
			continue
		}

		// ADD: new entity
		entity := ontology.Entity{
			ID:         c.ID,
			Type:       c.Type,
			Name:       c.Name,
			Definition: c.Definition,
			CreatedAt:  c.UpdatedAt,
			UpdatedAt:  c.UpdatedAt,
		}
		if err := s.ontStore.AddEntity(entity); err != nil {
			log.Warn("scribe: add entity failed", "id", c.ID, "error", err)
			result.Skipped++
			continue
		}
		result.Entities = append(result.Entities, entity)
		result.Kept++

		// Add relations if specified
		for _, rel := range c.Relations {
			r := ontology.Relation{
				SourceID: c.ID,
				TargetID: rel.Target,
				Relation: rel.Type,
			}
			if err := s.ontStore.AddRelation(r); err != nil {
				log.Warn("scribe: add relation failed", "source", c.ID, "target", rel.Target, "error", err)
			} else {
				result.Relations = append(result.Relations, r)
			}
		}
	}

	log.Info("session scribe complete",
		"input_size", result.InputSize,
		"compressed", result.CompressedTo,
		"extracted", result.Extracted,
		"kept", result.Kept,
		"skipped", result.Skipped)

	return result, nil
}

// entityCandidate is the LLM output format for extracted entities.
type entityCandidate struct {
	ID         string           `json:"id"`         // kebab-case unique identifier
	Name       string           `json:"name"`       // human-readable name
	Type       string           `json:"type"`       // concept, technique, decision, etc.
	Definition string           `json:"definition"` // one-line definition
	Relations  []entityRelation `json:"relations,omitempty"`
	UpdatedAt  string           `json:"updated_at,omitempty"`
}

type entityRelation struct {
	Target string `json:"target"` // target entity ID
	Type   string `json:"type"`   // relation type
}

// compressSession strips thinking blocks and tool results from session JSONL.
// Preserves user and assistant text content only.
// Handles both string content (user messages) and array-of-blocks content
// (assistant messages in Claude sessions: [{"type":"text","text":"..."}]).
func compressSession(input []byte) string {
	var compressed strings.Builder

	for _, line := range strings.Split(string(input), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Type    string          `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // skip non-JSON lines
		}

		// Skip thinking blocks, tool calls, tool results
		if msg.Type == "thinking" || msg.Type == "tool_use" || msg.Type == "tool_result" {
			continue
		}

		// Keep user and assistant text messages
		if msg.Role == "user" || msg.Role == "assistant" {
			content := extractTextContent(msg.Content)
			// Strip thinking tags inline
			content = stripThinkingTags(content)
			if strings.TrimSpace(content) == "" {
				continue
			}
			compressed.WriteString(fmt.Sprintf("[%s] %s\n\n", msg.Role, content))
		}
	}

	return compressed.String()
}

// extractTextContent handles both string and array-of-blocks content formats.
// Claude sessions use arrays: [{"type":"text","text":"..."},{"type":"tool_use",...}]
// User messages typically use plain strings.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try as plain string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

var thinkingTagRe = regexp.MustCompile(`(?s)<thinking>.*?</thinking>`)

func stripThinkingTags(s string) string {
	return thinkingTagRe.ReplaceAllString(s, "")
}

// extractEntities uses the LLM to identify entity candidates from compressed session text.
func (s *SessionScribe) extractEntities(ctx context.Context, text string) ([]entityCandidate, error) {
	// Truncate very long sessions (rune-safe to avoid splitting multi-byte UTF-8)
	if len(text) > 50000 {
		text = truncateUTF8(text, 50000) + "\n[...truncated...]"
	}

	typeList := strings.Join(s.entityTypes, ", ")
	prompt := fmt.Sprintf(`Extract knowledge entities from this conversation session.

Rules:
- Maximum %d entities per session. Most sessions produce 0-3.
- Each entity MUST have a unique kebab-case ID (e.g., "flash-attention", "wal-mode-sqlite").
- No ID = not specific enough to be an entity. Skip vague concepts.
- Role separation: user messages contain decisions/preferences; assistant messages contain events/techniques.
- Entity types (use ONLY these): %s.

Output valid JSON array only:
[{"id": "entity-id", "name": "Human Name", "type": "concept", "definition": "One-line definition", "relations": [{"target": "other-id", "type": "implements"}]}]

%s`, maxEntitiesPerSession, typeList, prompts.WrapUntrusted(text))

	resp, err := s.client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You extract structured knowledge entities from conversation transcripts. Output valid JSON only."},
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: s.model, MaxTokens: 4096})
	if err != nil {
		return nil, err
	}

	// Parse JSON (strip code fences if present)
	content := resp.Content
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		if idx := strings.Index(content[3:], "\n"); idx >= 0 {
			content = content[3+idx+1:]
		}
		if idx := strings.LastIndex(content, "```"); idx >= 0 {
			content = content[:idx]
		}
	}

	var candidates []entityCandidate
	if err := json.Unmarshal([]byte(content), &candidates); err != nil {
		return nil, fmt.Errorf("parse entity JSON: %w (content: %.200s)", err, content)
	}

	// Cap at max
	if len(candidates) > maxEntitiesPerSession {
		candidates = candidates[:maxEntitiesPerSession]
	}

	return candidates, nil
}

var kebabRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// isKebabCase checks if a string is valid kebab-case.
func isKebabCase(s string) bool {
	return len(s) >= 2 && kebabRe.MatchString(s)
}

// truncateUTF8 truncates a string to at most maxBytes bytes without splitting
// a multi-byte UTF-8 character. It walks back from the byte limit to find
// the last valid rune boundary.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk back from maxBytes to find a valid rune start
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
