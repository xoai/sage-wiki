package wiki

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
)

// DoctorResult holds diagnostic findings.
type DoctorResult struct {
	Checks []DoctorCheck
}

// DoctorCheck is a single diagnostic check.
type DoctorCheck struct {
	Name    string
	Status  string // ok, warn, error
	Message string
}

// RunDoctor validates configuration and connectivity.
func RunDoctor(projectDir string) *DoctorResult {
	result := &DoctorResult{}

	// Check config exists
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		result.add("config", "error", fmt.Sprintf("Failed to load config: %v", err))
		return result
	}
	result.add("config", "ok", fmt.Sprintf("Project %q loaded", cfg.Project))

	// Check project structure
	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		result.add("database", "error", "wiki.db not found — run sage-wiki init first")
	} else {
		result.add("database", "ok", "wiki.db exists")
	}

	// Check source directories
	sourcePaths := cfg.ResolveSources(projectDir)
	for _, sp := range sourcePaths {
		if _, err := os.Stat(sp); os.IsNotExist(err) {
			result.add("sources", "warn", fmt.Sprintf("Source directory not found: %s", sp))
		}
	}
	if len(sourcePaths) > 0 {
		if _, err := os.Stat(sourcePaths[0]); err == nil {
			result.add("sources", "ok", fmt.Sprintf("%d source directories configured", len(sourcePaths)))
		}
	}

	// Check API provider
	if cfg.API.Provider == "" {
		result.add("api", "warn", "No API provider configured — compile will fail")
	} else if cfg.API.APIKey == "" && cfg.API.Provider != "ollama" {
		result.add("api", "error", fmt.Sprintf("Provider %q configured but API key is empty", cfg.API.Provider))
	} else {
		result.add("api", "ok", fmt.Sprintf("Provider: %s", cfg.API.Provider))

		// Test connectivity
		client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, 1000, cfg.API.ExtraParams)
		if err != nil {
			result.add("connectivity", "error", fmt.Sprintf("Failed to create LLM client: %v", err))
		} else {
			model := cfg.Models.Summarize
			if model == "" {
				model = "gpt-4o-mini"
			}
			_, err := client.ChatCompletion([]llm.Message{
				{Role: "user", Content: "Reply with exactly: ok"},
			}, llm.CallOpts{Model: model, MaxTokens: 100})
			if err != nil {
				result.add("connectivity", "error", fmt.Sprintf("LLM API test failed: %v", err))
			} else {
				result.add("connectivity", "ok", fmt.Sprintf("LLM API reachable (model: %s)", model))
			}
		}
	}

	// Check embedding
	embedder := embed.NewFromConfig(cfg)
	if embedder != nil {
		result.add("embedding", "ok", fmt.Sprintf("Embedding: %s (%d-dim)", embedder.Name(), embedder.Dimensions()))
	} else {
		result.add("embedding", "warn", "No embedding provider — vector search disabled. Install Ollama or use a provider with embedding support.")
	}

	// Check Ollama
	ollamaClient := http.Client{Timeout: 2 * time.Second}
	resp, err := ollamaClient.Get("http://localhost:11434/api/tags")
	if err != nil {
		result.add("ollama", "info", "Ollama not running (optional)")
	} else {
		resp.Body.Close()
		result.add("ollama", "ok", "Ollama running at localhost:11434")
	}

	// Check git
	outputDir := cfg.ResolveOutput(projectDir)
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		result.add("output", "warn", fmt.Sprintf("Output directory not found: %s", outputDir))
	} else {
		result.add("output", "ok", fmt.Sprintf("Output: %s", cfg.Output))
	}

	return result
}

func (r *DoctorResult) add(name, status, message string) {
	r.Checks = append(r.Checks, DoctorCheck{
		Name:    name,
		Status:  status,
		Message: message,
	})
}

// HasErrors returns true if any check has error status.
func (r *DoctorResult) HasErrors() bool {
	for _, c := range r.Checks {
		if c.Status == "error" {
			return true
		}
	}
	return false
}

// FormatDoctor renders the result as a human-readable string.
func FormatDoctor(r *DoctorResult) string {
	var out string
	icons := map[string]string{
		"ok":    "[OK]",
		"warn":  "[WARN]",
		"error": "[ERROR]",
		"info":  "[INFO]",
	}

	for _, c := range r.Checks {
		icon := icons[c.Status]
		if icon == "" {
			icon = "[?]"
		}
		out += fmt.Sprintf("  %s %s: %s\n", icon, c.Name, c.Message)
	}

	if r.HasErrors() {
		out += "\nSome checks failed. Fix errors above before compiling.\n"
	} else {
		out += "\nAll checks passed. Ready to compile.\n"
	}

	return out
}
