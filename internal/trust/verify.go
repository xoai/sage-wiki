package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xoai/sage-wiki/internal/llm"
)

type VerifyOpts struct {
	All                bool
	Since              time.Duration
	Question           string
	Limit              int
	AutoPromote        bool
	Threshold          float64
	ConsensusThreshold int
	Stores             *IndexStores
}

type VerifyResult struct {
	Output         *PendingOutput
	GroundingScore float64
	Promoted       bool
	Error          error
}

func Verify(store *Store, client *llm.Client, model string, projectDir string, opts VerifyOpts) ([]VerifyResult, error) {
	outputs, err := store.ListByState(StatePending)
	if err != nil {
		return nil, fmt.Errorf("trust: list pending: %w", err)
	}

	if !opts.All && opts.Since > 0 {
		cutoff := time.Now().Add(-opts.Since)
		var filtered []*PendingOutput
		for _, o := range outputs {
			if o.CreatedAt.After(cutoff) {
				filtered = append(filtered, o)
			}
		}
		outputs = filtered
	}

	if opts.Question != "" {
		var filtered []*PendingOutput
		for _, o := range outputs {
			if o.Question == opts.Question {
				filtered = append(filtered, o)
			}
		}
		outputs = filtered
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if len(outputs) > limit {
		outputs = outputs[:limit]
	}

	var results []VerifyResult
	for _, o := range outputs {
		r := VerifyResult{Output: o}

		sources, err := loadSourceContent(projectDir, o.SourcesUsed)
		if err != nil {
			r.Error = fmt.Errorf("load sources: %w", err)
			results = append(results, r)
			continue
		}

		score, err := ComputeGroundingScore(o.Answer, sources, client, model)
		if err != nil {
			r.Error = fmt.Errorf("grounding check: %w", err)
			results = append(results, r)
			continue
		}

		r.GroundingScore = score
		if err := store.UpdateGroundingScore(o.ID, score); err != nil {
			r.Error = fmt.Errorf("update score: %w", err)
			results = append(results, r)
			continue
		}

		if opts.AutoPromote && score >= opts.Threshold {
			confs, _ := store.GetConfirmations(o.ID)
			independence := IndependenceScore(confs)
			threshold := opts.ConsensusThreshold
			if threshold <= 0 {
				threshold = 3
			}
			if ShouldAutoPromote(o.Confirmations, independence, threshold) {
				if opts.Stores != nil {
					if err := PromoteOutput(store, o.ID, projectDir, *opts.Stores); err != nil {
						r.Error = fmt.Errorf("promote: %w", err)
					} else {
						r.Promoted = true
					}
				} else {
					if err := store.Promote(o.ID); err != nil {
						r.Error = fmt.Errorf("promote: %w", err)
					} else {
						r.Promoted = true
					}
				}
			}
		}

		results = append(results, r)
	}

	return results, nil
}

func loadSourceContent(projectDir string, sourcesJSON string) ([]string, error) {
	if sourcesJSON == "" {
		return nil, nil
	}

	var paths []string
	if err := json.Unmarshal([]byte(sourcesJSON), &paths); err != nil {
		return nil, fmt.Errorf("parse sources_used: %w", err)
	}

	var contents []string
	for _, p := range paths {
		absPath := filepath.Join(projectDir, p)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		contents = append(contents, string(data))
	}
	return contents, nil
}
