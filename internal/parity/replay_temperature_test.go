package parity

import (
	"testing"
)

// TestCanonicalKey_StripsTemperature pins SPEC-04 D2: request matching must
// not depend on sampling parameters, so adding temperature:0 to compile
// requests keeps every committed fixture reachable.
func TestCanonicalKey_StripsTemperature(t *testing.T) {
	base := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	withTemp := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"temperature":0}`
	other := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"temperature":0.7}`

	k1, err := canonicalKey("POST", "/v1/chat/completions", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	k2, err := canonicalKey("POST", "/v1/chat/completions", []byte(withTemp))
	if err != nil {
		t.Fatal(err)
	}
	k3, err := canonicalKey("POST", "/v1/chat/completions", []byte(other))
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Errorf("key without temperature != key with temperature:0:\n%s\n%s", k1, k2)
	}
	if k1 != k3 {
		t.Errorf("key without temperature != key with temperature:0.7:\n%s\n%s", k1, k3)
	}
}

// TestCanonicalKey_NestedGenerationConfig strips provider-nested sampling
// params too (gemini generationConfig shape).
func TestCanonicalKey_NestedGenerationConfig(t *testing.T) {
	base := `{"contents":[{"parts":[{"text":"hi"}]}]}`
	nested := `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0,"maxOutputTokens":100}}`
	k1, err := canonicalKey("POST", "/v1beta/models/x:generateContent", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	k2, err := canonicalKey("POST", "/v1beta/models/x:generateContent", []byte(nested))
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Log("note: keys differ only because generationConfig carries maxOutputTokens — expected")
	}
	noTemp := `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":100}}`
	withTemp := `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":100,"temperature":0}}`
	k3, _ := canonicalKey("POST", "/v1beta/models/x:generateContent", []byte(noTemp))
	k4, _ := canonicalKey("POST", "/v1beta/models/x:generateContent", []byte(withTemp))
	if k3 != k4 {
		t.Errorf("nested temperature changed the key:\n%s\n%s", k3, k4)
	}
}
