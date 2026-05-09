package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MigrateResult struct {
	Migrated int
	Skipped  int
}

func MigrateExistingOutputs(store *Store, projectDir string, outputDir string, deindex func(id string)) (*MigrateResult, error) {
	outputsPath := filepath.Join(projectDir, outputDir, "outputs")
	entries, err := os.ReadDir(outputsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &MigrateResult{}, nil
		}
		return nil, fmt.Errorf("trust: read outputs dir: %w", err)
	}

	result := &MigrateResult{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		id := e.Name()

		existing, _ := store.Get(id)
		if existing != nil {
			result.Skipped++
			continue
		}

		absPath := filepath.Join(outputsPath, id)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		question, answer, sources := parseOutputFile(string(data))
		relPath := filepath.Join(outputDir, "outputs", id)

		sourcesJSON := "[]"
		if len(sources) > 0 {
			if b, err := json.Marshal(sources); err == nil {
				sourcesJSON = string(b)
			}
		}
		sourcesHash := ComputeSourcesHash(projectDir, sourcesJSON)

		o := &PendingOutput{
			ID:            id,
			Question:      question,
			QuestionHash:  HashQuestion(question),
			Answer:        answer,
			AnswerHash:    HashAnswer(answer),
			State:         StatePending,
			Confirmations: 0,
			SourcesHash:   sourcesHash,
			SourcesUsed:   sourcesJSON,
			FilePath:      relPath,
			CreatedAt:     time.Now(),
		}

		if err := store.InsertPending(o); err != nil {
			return nil, fmt.Errorf("trust: insert pending %s: %w", id, err)
		}

		if deindex != nil {
			deindex(id)
		}

		result.Migrated++
	}

	return result, nil
}

func parseOutputFile(content string) (question, answer string, sources []string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content, nil
	}
	endIdx := strings.Index(content[4:], "\n---\n")
	if endIdx < 0 {
		return "", content, nil
	}

	frontmatter := content[4 : 4+endIdx]
	body := strings.TrimSpace(content[4+endIdx+5:])

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "question:") {
			q := strings.TrimSpace(line[9:])
			q = strings.Trim(q, `"'`)
			question = q
		}
		if strings.HasPrefix(line, "sources:") {
			raw := strings.TrimSpace(line[8:])
			raw = strings.Trim(raw, "[]")
			if raw != "" {
				for _, s := range strings.Split(raw, ",") {
					s = strings.TrimSpace(s)
					s = strings.Trim(s, `"' `)
					if s != "" {
						sources = append(sources, s)
					}
				}
			}
		}
	}

	return question, body, sources
}
