package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/fsutil"
	"github.com/xoai/sage-wiki/internal/graph/community"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/store"
)

// CommunitiesPass (P3-5): detect graph communities, persist membership,
// generate + cache summaries, and index them for global queries. Additive
// enrichment like the triples/resolve passes: it NEVER fails a compile —
// every failure is logged and the pass returns.
//
// Call it AFTER entity resolution and the supersession sweep, on all three
// entity-mutating paths (full pipeline, batch resume, re-extract).
func CommunitiesPass(
	ctx context.Context,
	projectDir string,
	ont store.OntologyStore,
	cs store.CommunityStore,
	mem store.EntryStore,
	vec store.VectorStore,
	embedder embed.Embedder,
	cfg *config.Config,
	client *llm.Client,
) {
	if cfg == nil || !cfg.Ontology.Communities.Enabled || ont == nil || cs == nil || client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "communities"), time.Now())
	client.SetPass("communities")
	defer client.SetPass("")

	ccfg := cfg.Ontology.Communities
	minMembers := ccfg.MinMembersOrDefault()

	if err := ctx.Err(); err != nil {
		return
	}

	nodes, edges, err := community.BuildInput(ont)
	if err != nil {
		log.Warn("communities: input build failed", "error", err)
		return
	}
	if len(nodes) == 0 {
		return
	}
	levels := community.Detect(nodes, edges, 4)

	// Flatten levels into community rows: ID c<level>-<seq>, seq over the
	// Detect-pinned order (min member ID). Parent links point at the level+1
	// community containing the majority... no — the FIRST level+1 community
	// whose member set contains this community's first member (levels are
	// partitions, so exactly one contains it).
	var rows []store.Community
	membersOf := map[string][]string{}
	for li, lvl := range levels {
		for si, members := range lvl.Communities {
			id := fmt.Sprintf("c%d-%d", li, si)
			parent := ""
			if li+1 < len(levels) {
				parent = findParent(levels[li+1].Communities, members[0], li+1)
			}
			rows = append(rows, store.Community{
				ID:          id,
				Level:       li,
				ParentID:    parent,
				MemberCount: len(members),
				UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
			})
			membersOf[id] = members
		}
	}
	// Edge counts per level-0 community (intra-community edges).
	commOfEntity := map[string]string{}
	for li, lvl := range levels {
		if li != 0 {
			continue
		}
		for si, members := range lvl.Communities {
			for _, e := range members {
				commOfEntity[e] = fmt.Sprintf("c0-%d", si)
			}
		}
	}
	for i := range rows {
		if rows[i].Level != 0 {
			continue
		}
		count := 0
		for _, e := range edges {
			if commOfEntity[e.From] == rows[i].ID && commOfEntity[e.To] == rows[i].ID {
				count++
			}
		}
		rows[i].EdgeCount = count
	}

	removed, err := cs.ReplaceDetection(rows, membersOf)
	if err != nil {
		log.Warn("communities: replace detection failed", "error", err)
		return
	}

	// Pre-loop artifact deletion (spec: no cancel window): cleared and
	// below-min communities lose their index/file artifacts. Below-min rows
	// also lose their STORED summary so a later min_members lowering
	// re-summarizes them (spec i5).
	current, err := cs.ListCommunities(-1)
	if err != nil {
		log.Warn("communities: list after replace failed", "error", err)
		return
	}
	toDelete := append([]string(nil), removed...)
	for _, c := range current {
		if c.SummaryHash == "" && c.Summary == "" {
			// Cleared by the hash-mismatch rule: artifacts are stale either way.
			toDelete = append(toDelete, c.ID)
			continue
		}
		if c.MemberCount < minMembers {
			toDelete = append(toDelete, c.ID)
			// Clear the stored summary so a future min_members lowering
			// regenerates (row keeps membership; summary fields blank).
			if c.Summary != "" || c.SummaryHash != "" {
				if err := cs.SetSummary(c.ID, "", "", ""); err != nil {
					log.Warn("communities: clear below-min summary failed", "id", c.ID, "error", err)
				}
			}
		}
	}
	outDir := filepath.Join(projectDir, cfg.Output)
	for _, id := range dedupe(toDelete) {
		deleteCommunityArtifacts(mem, vec, outDir, id)
	}

	// Summarize eligible communities whose member hash or model is stale.
	model := communityModel(cfg)
	for _, c := range current {
		if c.MemberCount < minMembers {
			continue
		}
		if err := ctx.Err(); err != nil {
			return
		}
		members := membersOf[c.ID]
		hash := store.MemberHash(members)
		if c.SummaryHash == hash && c.Model == model {
			continue // cached, unchanged
		}
		summary := summarizeCommunity(ctx, client, model, ccfg.MaxTokensOrDefault(), c, members, ont)
		if summary == "" {
			continue // empty/failed — retried next compile (hash still stale)
		}
		if err := cs.SetSummary(c.ID, summary, hash, model); err != nil {
			log.Warn("communities: summary store failed", "id", c.ID, "error", err)
			continue
		}
		keywords := extractKeywords(summary)
		if err := writeCommunityFile(outDir, c, members, summary, keywords); err != nil {
			log.Warn("communities: file write failed", "id", c.ID, "error", err)
		}
		indexCommunity(mem, vec, embedder, c, summary)
	}
	log.Info("communities detected", "levels", len(levels), "communities", len(rows))
}

func findParent(next [][]string, firstMember string, level int) string {
	for si, members := range next {
		for _, m := range members {
			if m == firstMember {
				return fmt.Sprintf("c%d-%d", level, si)
			}
		}
	}
	return ""
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func communityModel(cfg *config.Config) string {
	if m := cfg.Ontology.Communities.Model; m != "" {
		return m
	}
	if cfg.Models.Extract != "" {
		return cfg.Models.Extract
	}
	if cfg.Models.Summarize != "" {
		return cfg.Models.Summarize
	}
	return "gpt-4o-mini"
}

// summarizeCommunity generates one ~150-word theme summary. Empty on any
// failure — the caller treats empty as "retry next compile".
func summarizeCommunity(ctx context.Context, client *llm.Client, model string, maxTokens int, c store.Community, members []string, ont store.OntologyStore) string {
	// Intra-community edges with evidence (truncated) ground the summary.
	var lines []string
	set := map[string]bool{}
	for _, m := range members {
		set[m] = true
	}
	for _, m := range members {
		rels, err := ont.GetRelations(m, store.Outbound, "")
		if err != nil {
			continue
		}
		for _, r := range rels {
			if !set[r.TargetID] {
				continue
			}
			ev := r.Evidence
			if len(ev) > 200 {
				ev = ev[:200]
			}
			line := fmt.Sprintf("- %s %s %s", r.SourceID, r.Relation, r.TargetID)
			if ev != "" {
				line += fmt.Sprintf(" (%s)", ev)
			}
			lines = append(lines, line)
			if len(lines) >= 40 {
				break
			}
		}
		if len(lines) >= 40 {
			break
		}
	}

	prompt := fmt.Sprintf(`Summarize the theme of this knowledge-graph community in about 150 words, then list 3-5 keywords.

Entities: %s

Relations:
%s

Respond with the summary paragraph, then "Keywords:" followed by a comma-separated list.`,
		strings.Join(members, ", "), strings.Join(lines, "\n"))

	resp, err := client.ChatCompletionCtx(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
	if err != nil {
		log.Warn("communities: summary LLM failed", "id", c.ID, "error", err)
		return ""
	}
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		log.Warn("communities: empty summary", "id", c.ID, "details", resp.EmptyContentDetails())
	}
	return content
}

func extractKeywords(summary string) []string {
	idx := strings.LastIndex(summary, "Keywords:")
	if idx < 0 {
		return nil
	}
	parts := strings.Split(summary[idx+len("Keywords:"):], ",")
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeCommunityFile(outputDir string, c store.Community, members []string, summary string, keywords []string) error {
	dir := filepath.Join(outputDir, "communities")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\nlevel: %d\nmembers: %d\n", c.ID, c.Level, len(members))
	if len(keywords) > 0 {
		fmt.Fprintf(&b, "keywords: [%s]\n", strings.Join(keywords, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(summary)
	b.WriteString("\n")
	return fsutil.WriteFileAtomic(filepath.Join(dir, c.ID+".md"), []byte(b.String()), 0o644)
}

// indexCommunity upserts the summary into the FTS + vector indexes
// (delete-then-add: mem.Add errors on an existing ID).
func indexCommunity(mem store.EntryStore, vec store.VectorStore, embedder embed.Embedder, c store.Community, summary string) {
	docID := "community:" + c.ID
	_ = mem.Delete(docID) // absent is fine
	if err := mem.Add(store.Entry{ID: docID, Content: summary, ArticlePath: "communities/" + c.ID + ".md"}); err != nil {
		log.Warn("communities: FTS index failed", "id", c.ID, "error", err)
	}
	if embedder == nil {
		return
	}
	v, err := embedder.Embed(summary)
	if err != nil {
		log.Warn("communities: embed failed", "id", c.ID, "error", err)
		return
	}
	_ = vec.Delete(docID)
	if err := vec.Upsert(docID, v); err != nil {
		log.Warn("communities: vector index failed", "id", c.ID, "error", err)
	}
}

// deleteCommunityArtifacts removes a community's index entries and markdown
// file. Best-effort: each step logs and continues.
func deleteCommunityArtifacts(mem store.EntryStore, vec store.VectorStore, outputDir, id string) {
	docID := "community:" + id
	if mem != nil {
		_ = mem.Delete(docID)
	}
	if vec != nil {
		_ = vec.Delete(docID)
	}
	if err := os.Remove(filepath.Join(outputDir, "communities", id+".md")); err != nil && !os.IsNotExist(err) {
		log.Warn("communities: file delete failed", "id", id, "error", err)
	}
}
