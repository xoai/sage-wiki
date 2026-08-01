package engine

import "testing"

func TestValidateWorkspaceName(t *testing.T) {
	valid := []string{
		"a", "0abc", "a-b_c", "personal", "work-2026",
		"a123456789012345678901234567890123456789012345678901234567890123", // 64 chars
	}
	invalid := []string{
		"..", ".", "a/b", "a\\b", "%2e%2e", "..%2f", "%2f", "a%2fb",
		"", "Abc", "-a", "_a", "a b", "wörk", "a.b",
		"a1234567890123456789012345678901234567890123456789012345678901234", // 65 chars
		"/abs", "a/", "/",
	}
	for _, name := range valid {
		if err := ValidateWorkspaceName(name); err != nil {
			t.Errorf("ValidateWorkspaceName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range invalid {
		if err := ValidateWorkspaceName(name); err == nil {
			t.Errorf("ValidateWorkspaceName(%q) = nil, want error", name)
		}
	}
}
