package query

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// GlobalQA answers corpus-wide questions ("main themes across everything?")
// by map-reducing over pre-generated community summaries (P3-5, GraphRAG
// pattern). Selection: over-fetch → community: prefix filter → chosen-level
// intersection → member_count filter. Citations are community-level.
type GlobalQAOpts struct {
	Model          string
	MaxCommunities int
	MaxTokens      int
	MinMembers     int
	MaxParallel    int
	Embedder       interface {
		Embed(string) ([]float32, error)
	}
}

// CommunityCitation ties a global answer claim to its community.
type CommunityCitation struct {
	ID          string `json:"id"`
	Level       int    `json:"level"`
	MemberCount int    `json:"member_count"`
	SummaryHash string `json:"summary_hash,omitempty"`
}

// GlobalQAResult is the synthesized answer plus community citations.
type GlobalQAResult struct {
	Answer string              `json:"answer"`
	Level  int                 `json:"level"`
	Cited  []CommunityCitation `json:"cited"`
}

// ErrNoCommunities is returned when no summarized community is available
// (communities disabled, never compiled with them, or all summaries failed).
var ErrNoCommunities = fmt.Errorf("no summarized communities — run a compile with ontology.communities.enabled")

func GlobalQA(ctx context.Context, cs store.CommunityStore, searcher *hybrid.Searcher,
	client *llm.Client, question string, opts GlobalQAOpts) (GlobalQAResult, error) {
	if question == "" {
		return GlobalQAResult{}, fmt.Errorf("globalqa: question is required")
	}
	maxC := opts.MaxCommunities
	if maxC <= 0 {
		maxC = 8
	}
	minMembers := opts.MinMembers
	if minMembers <= 0 {
		minMembers = 3
	}
	maxParallel := opts.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 1
	}

	// Level walk: highest level with >1 community AND >=1 summarized
	// community; else level 0 if it has summaries; else the sentinel.
	chosen, summarized := pickLevel(cs, minMembers)
	if chosen < 0 {
		return GlobalQAResult{}, ErrNoCommunities
	}

	// Over-fetch, then scope: community: prefix → chosen level → member_count.
	fetchK := maxC * 4
	if fetchK < 20 {
		fetchK = 20
	}
	var queryVec []float32
	if opts.Embedder != nil {
		queryVec, _ = opts.Embedder.Embed(question)
	}
	hits, err := searcher.Search(hybrid.SearchOpts{Query: question, Limit: fetchK}, queryVec)
	if err != nil {
		return GlobalQAResult{}, fmt.Errorf("globalqa: search: %w", err)
	}
	summarizedIDs := make(map[string]store.Community, len(summarized))
	for _, c := range summarized {
		summarizedIDs["community:"+c.ID] = c
	}
	var selected []store.Community
	for _, h := range hits {
		c, ok := summarizedIDs[h.ID]
		if !ok {
			continue
		}
		selected = append(selected, c)
		if len(selected) >= maxC {
			break
		}
	}
	if len(selected) == 0 {
		// Communities exist but none match this question — different
		// failure from "never built"; say which.
		return GlobalQAResult{}, fmt.Errorf("no communities match this question (selection over %d summarized communities found nothing)", len(summarized))
	}

	// Map: one partial answer per selected community, bounded parallelism.
	type partial struct {
		id   string
		text string
	}
	partials := make([]partial, len(selected))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, c := range selected {
		wg.Add(1)
		go func(i int, c store.Community) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := client.ChatCompletionCtx(ctx, []llm.Message{
				{Role: "user", Content: fmt.Sprintf(
					"Question: %s\n\nCommunity summary:\n%s\n\nAnswer the question using ONLY this community's summary, in 2-3 sentences. If the summary is not relevant, say IRRELEVANT.",
					question, c.Summary)},
			}, llm.CallOpts{Model: opts.Model, MaxTokens: opts.MaxTokens})
			if err != nil {
				log.Warn("globalqa: map call failed", "community", c.ID, "error", err)
				return
			}
			partials[i] = partial{id: c.ID, text: strings.TrimSpace(resp.Content)}
		}(i, c)
	}
	wg.Wait()

	// Reduce: synthesize from non-IRRELEVANT partials.
	var relevant []partial
	var cited []CommunityCitation
	byID := map[string]store.Community{}
	for _, c := range selected {
		byID[c.ID] = c
	}
	for _, p := range partials {
		if p.text == "" || strings.HasPrefix(p.text, "IRRELEVANT") {
			continue
		}
		relevant = append(relevant, p)
		c := byID[p.id]
		cited = append(cited, CommunityCitation{
			ID: c.ID, Level: c.Level, MemberCount: c.MemberCount, SummaryHash: c.SummaryHash,
		})
	}
	if len(relevant) == 0 {
		return GlobalQAResult{Answer: "the communities do not cover this question", Level: chosen, Cited: []CommunityCitation{}}, nil
	}

	var sb strings.Builder
	for _, p := range relevant {
		fmt.Fprintf(&sb, "[%s] %s\n\n", p.id, p.text)
	}
	resp, err := client.ChatCompletionCtx(ctx, []llm.Message{
		{Role: "user", Content: fmt.Sprintf(
			"Question: %s\n\nPartial answers from knowledge communities:\n%s\nSynthesize one global answer. Cite communities by their [id] inline.",
			question, sb.String())},
	}, llm.CallOpts{Model: opts.Model, MaxTokens: opts.MaxTokens})
	if err != nil {
		return GlobalQAResult{}, fmt.Errorf("globalqa: reduce: %w", err)
	}

	return GlobalQAResult{
		Answer: strings.TrimSpace(resp.Content),
		Level:  chosen,
		Cited:  cited,
	}, nil
}

// pickLevel walks levels from MaxLevel down, returning the first level with
// >1 community AND >=1 summarized community meeting minMembers; else level 0
// if it has summaries; else -1.
func pickLevel(cs store.CommunityStore, minMembers int) (int, []store.Community) {
	maxLevel, err := cs.MaxLevel()
	if err != nil || maxLevel < 0 {
		return -1, nil
	}
	var zero []store.Community
	for level := maxLevel; level >= 0; level-- {
		comms, err := cs.ListCommunities(level)
		if err != nil {
			continue
		}
		var ok []store.Community
		for _, c := range comms {
			if c.Summary != "" && c.MemberCount >= minMembers {
				ok = append(ok, c)
			}
		}
		if level == 0 {
			zero = ok
		}
		if len(comms) > 1 && len(ok) > 0 {
			return level, ok
		}
	}
	if len(zero) > 0 {
		return 0, zero
	}
	return -1, nil
}
