package auth

import "fmt"

var Providers = map[string]ProviderConfig{
	"openai": {
		AuthorizeURL: "https://auth.openai.com/oauth/authorize",
		TokenURL:     "https://auth.openai.com/oauth/token",
		ClientID:     "app_EMoamEEZ73f0CkXaXp7hrann",
		RedirectPort: 1455,
		RedirectPath: "/auth/callback",
		Scopes:       []string{"openid", "profile", "email", "offline_access"},
		ExtraAuthParams: map[string]string{
			"codex_cli_simplified_flow": "true",
		},
		FlowType:       FlowPKCE,
		ImportPath:     "~/.codex/auth.json",
		AccountIDClaim: "https://api.openai.com/auth.chatgpt_account_id",
	},
	"anthropic": {
		AuthorizeURL: "https://claude.ai/oauth/authorize",
		TokenURL:     "https://platform.claude.com/v1/oauth/token",
		ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
		RedirectPort: 53692,
		RedirectPath: "/callback",
		Scopes:       []string{"user:inference"},
		FlowType:     FlowPKCE,
		ImportPath:   "~/.claude/.credentials.json",
	},
	"github-copilot": {
		FlowType:   FlowImportOnly,
		ImportPath: "~/.copilot/settings.json",
	},
	"gemini": {
		FlowType:   FlowImportOnly,
		ImportPath: "~/.gemini/oauth_creds.json",
	},
}

var providerAliases = map[string]string{
	"claude":         "anthropic",
	"copilot":        "github-copilot",
	"github-copilot": "github-copilot",
	"google":         "gemini",
}

func ResolveProviderName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("auth: provider name is required")
	}
	if _, ok := Providers[name]; ok {
		return name, nil
	}
	if canonical, ok := providerAliases[name]; ok {
		return canonical, nil
	}
	return "", fmt.Errorf("auth: unknown provider %q (valid: openai, anthropic, claude, copilot, gemini)", name)
}
