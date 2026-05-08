package auth

import (
	"fmt"
	"net/http"
	"os"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
)

func resolveEnvKey(provider string) string {
	envVars := map[string]string{
		"openai":    "OPENAI_API_KEY",
		"anthropic": "ANTHROPIC_API_KEY",
		"gemini":    "GEMINI_API_KEY",
	}
	if envName, ok := envVars[provider]; ok {
		return os.Getenv(envName)
	}
	return ""
}

func ResolveCredential(cfg *config.Config) (apiKey string, isSubscription bool, err error) {
	if envKey := resolveEnvKey(cfg.API.Provider); envKey != "" {
		if cfg.API.Auth == "subscription" {
			log.Warn("environment variable overrides subscription auth — using API key from environment",
				"provider", cfg.API.Provider)
		}
		return envKey, false, nil
	}

	if cfg.API.Auth == "subscription" {
		store := NewStore(DefaultStorePath())
		cred, err := store.Get(cfg.API.Provider)
		if err == nil && cred.AccessToken != "" {
			return cred.AccessToken, true, nil
		}

		if cfg.API.APIKey != "" {
			log.Warn("subscription token unavailable, falling back to API key",
				"provider", cfg.API.Provider,
				"reason", fmt.Sprintf("%v", err))
			fmt.Fprintf(os.Stderr, "Warning: Subscription token unavailable for %s. Falling back to API key. "+
				"You may be billed via your API account. Run `sage-wiki auth login --provider %s` to re-authenticate.\n",
				cfg.API.Provider, cfg.API.Provider)
			return cfg.API.APIKey, false, nil
		}

		return "", false, fmt.Errorf("no credentials available for %s. "+
			"Run `sage-wiki auth login --provider %s` or set api.api_key in config.yaml",
			cfg.API.Provider, cfg.API.Provider)
	}

	return cfg.API.APIKey, false, nil
}

func NewLLMClient(cfg *config.Config) (*llm.Client, error) {
	apiKey, isSub, err := ResolveCredential(cfg)
	if err != nil {
		return nil, err
	}

	passKey := apiKey
	if isSub {
		passKey = ""
	}

	client, err := llm.NewClient(cfg.API.Provider, passKey, cfg.API.BaseURL, cfg.API.RateLimit, cfg.API.ExtraParams)
	if err != nil {
		return nil, err
	}

	if isSub {
		store := NewStore(DefaultStorePath())
		transport := NewAuthTransport(http.DefaultTransport, store, cfg.API.Provider)
		client.SetTransport(transport)
	}

	return client, nil
}
