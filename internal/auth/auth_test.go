package auth

import (
	"testing"
	"time"
)

func TestCredentialExpiresWithin(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		dur      time.Duration
		expected bool
	}{
		{
			name:     "expired token",
			cred:     Credential{ExpiresAt: time.Now().Add(-1 * time.Hour).Unix()},
			dur:      5 * time.Minute,
			expected: true,
		},
		{
			name:     "expires in 3 minutes, buffer is 5 minutes",
			cred:     Credential{ExpiresAt: time.Now().Add(3 * time.Minute).Unix()},
			dur:      5 * time.Minute,
			expected: true,
		},
		{
			name:     "expires in 10 minutes, buffer is 5 minutes",
			cred:     Credential{ExpiresAt: time.Now().Add(10 * time.Minute).Unix()},
			dur:      5 * time.Minute,
			expected: false,
		},
		{
			name:     "zero expiry treated as expired",
			cred:     Credential{ExpiresAt: 0},
			dur:      5 * time.Minute,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cred.ExpiresWithin(tt.dur); got != tt.expected {
				t.Errorf("ExpiresWithin(%v) = %v, want %v", tt.dur, got, tt.expected)
			}
		})
	}
}

func TestCredentialString(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		expected string
	}{
		{
			name:     "normal token",
			cred:     Credential{Provider: "openai", AccessToken: "sk-abc123xyz"},
			expected: "openai:****3xyz",
		},
		{
			name:     "short token",
			cred:     Credential{Provider: "anthropic", AccessToken: "ab"},
			expected: "anthropic:****ab",
		},
		{
			name:     "empty token",
			cred:     Credential{Provider: "gemini", AccessToken: ""},
			expected: "gemini:****",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cred.String(); got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCredentialExtraHeaders(t *testing.T) {
	t.Run("openai with account ID", func(t *testing.T) {
		cred := Credential{Provider: "openai", AccountID: "acct-123"}
		headers := cred.ExtraHeaders()
		if headers == nil {
			t.Fatal("expected headers, got nil")
		}
		if headers["ChatGPT-Account-ID"] != "acct-123" {
			t.Errorf("ChatGPT-Account-ID = %q, want %q", headers["ChatGPT-Account-ID"], "acct-123")
		}
	})

	t.Run("openai without account ID", func(t *testing.T) {
		cred := Credential{Provider: "openai", AccountID: ""}
		if headers := cred.ExtraHeaders(); headers != nil {
			t.Errorf("expected nil headers, got %v", headers)
		}
	})

	t.Run("anthropic returns nil", func(t *testing.T) {
		cred := Credential{Provider: "anthropic"}
		if headers := cred.ExtraHeaders(); headers != nil {
			t.Errorf("expected nil headers, got %v", headers)
		}
	})
}

func TestResolveProviderName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{"openai", "openai", false},
		{"anthropic", "anthropic", false},
		{"claude", "anthropic", false},
		{"copilot", "github-copilot", false},
		{"github-copilot", "github-copilot", false},
		{"gemini", "gemini", false},
		{"google", "gemini", false},
		{"unknown", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ResolveProviderName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("ResolveProviderName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestProviderRegistryCompleteness(t *testing.T) {
	expected := []string{"openai", "anthropic", "github-copilot", "gemini"}
	for _, name := range expected {
		if _, ok := Providers[name]; !ok {
			t.Errorf("provider %q not found in registry", name)
		}
	}
}

func TestFlowTypes(t *testing.T) {
	if Providers["openai"].FlowType != FlowPKCE {
		t.Error("openai should use FlowPKCE")
	}
	if Providers["anthropic"].FlowType != FlowPKCE {
		t.Error("anthropic should use FlowPKCE")
	}
	if Providers["github-copilot"].FlowType != FlowImportOnly {
		t.Error("github-copilot should use FlowImportOnly")
	}
	if Providers["gemini"].FlowType != FlowImportOnly {
		t.Error("gemini should use FlowImportOnly")
	}
}
