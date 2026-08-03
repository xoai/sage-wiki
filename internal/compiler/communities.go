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
	"github.com/xoai/sage-wiki/internal/ontology"
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
		// The graph shrank to nothing: clear stale communities (spec M2 —
		// an early return would leave GlobalQA answering from summaries of
		// a graph that no longer exists).
		removed, err := cs.ReplaceDetection(nil, nil)
		if err != nil {
			log.Warn("communities: clear on empty graph failed", "error", err)
			return
		}
		outDir := filepath.Join(projectDir, cfg.Output)
		for _, id := range removed {
			deleteCommunityArtifacts(mem, vec, outDir, id)
		}
		sweepCommunityFiles(outDir, map[string]bool{})
		return
	}
	levels := community.Detect(nodes, edges, 4)

	// Flatten levels into community rows: ID c<level>-<seq>, seq over the
	// Detect-pinned order (min member ID). Parent = the level+1 community
	// containing this community's first member — levels are partitions of
	// the same node set, so exactly one contains it.
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
				UpdatedAt:   config.NowUTC().Format(time.RFC3339),
			})
			membersOf[id] = members
		}
	}
	// Intra-community edge counts at every level, one pass over the edges
	// per level (O(edges × levels), not O(communities × edges)): the input
	// edge list is the base graph for all levels since higher levels
	// flatten to original entities.
	byLevel := map[int][]store.Community{}
	for _, r := range rows {
		byLevel[r.Level] = append(byLevel[r.Level], r)
	}
	counts := map[string]int{}
	for li := range byLevel {
		commOfEntity := map[string]string{}
		for si, members := range levels[li].Communities {
			id := fmt.Sprintf("c%d-%d", li, si)
			for _, e := range members {
				commOfEntity[e] = id
			}
		}
		for _, e := range edges {
			if id := commOfEntity[e.From]; id != "" && id == commOfEntity[e.To] {
				counts[id]++
			}
		}
	}
	for i := range rows {
		rows[i].EdgeCount = counts[rows[i].ID]
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
	// os.ReadDir sweep: any file without a current row is an orphan —
	// covers the commit-then-cleanup crash window, where the DB row is gone
	// and no future ReplaceDetection will ever return that ID (spec M1).
	keepFiles := map[string]bool{}
	for _, c := range current {
		keepFiles[c.ID] = true
	}
	sweepCommunityFiles(outDir, keepFiles)

	// Summarize eligible communities whose member hash or model is stale.
	model := communityModel(cfg)
	// One relation scan for the whole pass (gates i2): summarizeCommunity
	// filters this slice per community instead of scanning per community.
	allRels, err := ont.AllRelations()
	if err != nil {
		log.Warn("communities: relations read failed", "error", err)
		return
	}
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
		summary := summarizeCommunity(ctx, client, model, ccfg.MaxTokensOrDefault(), cfg.Compiler.CompileTemperature(), c, members, allRels)
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
func summarizeCommunity(ctx context.Context, client *llm.Client, model string, maxTokens int, temp *float64, c store.Community, members []string, all []store.Relation) string {
	// Intra-community edges with evidence (truncated) ground the summary,
	// filtered to live edges — the same LiveAt rule detection used.
	var lines []string
	set := map[string]bool{}
	for _, m := range members {
		set[m] = true
	}
	now := config.NowUTC()
	for _, r := range all {
		if !set[r.SourceID] || !set[r.TargetID] {
			continue
		}
		if r.Relation == "cites" {
			continue // document links — detection excluded them too
		}
		if !ontology.LiveAt(r, now) {
			continue
		}
		ev := r.Evidence
		if runes := []rune(ev); len(runes) > 200 {
			ev = string(runes[:200]) // rune-safe: a byte cut can split a multi-byte rune
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

	prompt := fmt.Sprintf(`Summarize the theme of this knowledge-graph community in about 150 words, then list 3-5 keywords.

Entities: %s

Relations:
%s

Respond with the summary paragraph, then "Keywords:" followed by a comma-separated list.`,
		strings.Join(members, ", "), strings.Join(lines, "\n"))

	resp, err := client.ChatCompletionCtx(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: maxTokens, Temperature: temp})
	if err != nil {
		log.Warn("communities: summary LLM failed", "id", c.ID, "error", err)
		return ""
	}
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		log.Warn("communities: empty summary", "id", c.ID, "details", resp.EmptyContentDetails())
		return ""
	}
	// The body is unsanitized LLM output written to disk: strip lines that
	// look like frontmatter delimiters so no future consumer that splits on
	// --- ever re-parses a fake block (frontmatter itself is quoted safely).
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "---" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
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
		quoted := make([]string, len(keywords))
		for i, k := range keywords {
			quoted[i] = fmt.Sprintf("%q", k)
		}
		fmt.Fprintf(&b, "keywords: [%s]\n", strings.Join(quoted, ", "))
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
	if mem != nil {
		_ = mem.Delete(docID) // absent is fine
		if err := mem.Add(store.Entry{ID: docID, Content: summary, ArticlePath: "communities/" + c.ID + ".md"}); err != nil {
			log.Warn("communities: FTS index failed", "id", c.ID, "error", err)
		}
	}
	if embedder == nil || vec == nil {
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

// sweepCommunityFiles deletes communities/*.md files not in keep. Best-effort.
func sweepCommunityFiles(outDir string, keep map[string]bool) {
	entries, err := os.ReadDir(filepath.Join(outDir, "communities"))
	if err != nil {
		return // no directory yet
	}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".md")
		if e.IsDir() || id == e.Name() || keep[id] {
			continue
		}
		if err := os.Remove(filepath.Join(outDir, "communities", e.Name())); err != nil {
			log.Warn("communities: orphan file sweep failed", "file", e.Name(), "error", err)
		}
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
