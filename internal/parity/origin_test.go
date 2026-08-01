package parity

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOriginDeterministic(t *testing.T) {
	srv := NewOriginServer()
	defer srv.Close()

	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"### Source: raw/attention.md\nSome text about attention mechanisms."}]}`)
	get := func() string {
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return string(raw)
	}
	a, b := get(), get()
	if a != b {
		t.Fatal("origin must be byte-deterministic")
	}
	if !strings.Contains(a, "## Key claims") {
		t.Errorf("summarize class not hit: %s", a)
	}

	// Concept extraction class.
	extract := []byte(`{"model":"m","messages":[{"role":"user","content":"You are a concept extraction system.\n### Source: raw/attention.md\nsummary text"}]}`)
	resp, _ := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(extract))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `\"name\": \"attention\"`) {
		t.Errorf("extract class marker wrong: %s", raw)
	}

	// Embeddings deterministic + right shape.
	embody := []byte(`{"model":"text-embedding-3-small","input":["hello","world"]}`)
	resp2, _ := http.Post(srv.URL+"/v1/embeddings", "application/json", bytes.NewReader(embody))
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw2, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 || len(out.Data[0].Embedding) != 8 {
		t.Fatalf("embedding shape wrong: %v", out)
	}
	if out.Data[0].Embedding[0] == out.Data[1].Embedding[0] {
		t.Error("different texts should embed differently")
	}
}
