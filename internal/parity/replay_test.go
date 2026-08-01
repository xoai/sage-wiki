package parity

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalKeyStable(t *testing.T) {
	a := []byte(`{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "max_tokens": 100}`)
	b := []byte(`{ "max_tokens":100, "messages":[{"role":"user","content":"hi"}],"model":"gpt-4o" }`)
	ka, err := canonicalKey("POST", "/v1/chat/completions", a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := canonicalKey("POST", "/v1/chat/completions", b)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Errorf("canonical key not stable across key order/whitespace: %s vs %s", ka, kb)
	}
	kc, _ := canonicalKey("POST", "/v1/chat/completions", []byte(`{"model":"gpt-4o-mini"}`))
	if kc == ka {
		t.Error("different bodies must produce different keys")
	}
}

func TestReplayHitAndMiss(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	key, err := canonicalKey("POST", "/v1/chat/completions", body)
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := canonicalJSON(body)
	fx := Fixture{
		Request:  CanonicalRequest{Method: "POST", Path: "/v1/chat/completions", Body: canon},
		Response: CanonicalResponse{Status: 200, Body: json.RawMessage(`{"choices":[{"message":{"content":"canned"}}],"model":"gpt-4o","usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)},
	}
	raw, _ := json.Marshal(fx)
	if err := os.WriteFile(filepath.Join(dir, key+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := NewReplayServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	resp, err := http.Post(srv.URL()+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("hit: status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), "canned") {
		t.Errorf("hit: body %s", got)
	}

	miss, err := http.Post(srv.URL()+"/v1/chat/completions", "application/json", bytes.NewReader([]byte(`{"model":"other"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer miss.Body.Close()
	if !RecordMiss(miss.StatusCode) {
		t.Fatalf("miss: status %d, want 418", miss.StatusCode)
	}
	text, _ := io.ReadAll(miss.Body)
	if !strings.Contains(string(text), "replay cache miss") || !strings.Contains(string(text), `"model":"other"`) {
		t.Errorf("miss diff must carry the unmatched request, got:\n%s", text)
	}
	if !strings.Contains(string(text), key) {
		t.Errorf("miss diff must list known fixture keys, got:\n%s", text)
	}
}

func TestRecordWriteThrough(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"path":"` + r.URL.Path + `"}`))
	}))
	defer origin.Close()

	dir := t.TempDir()
	srv, err := NewRecordServer(origin.URL, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	resp, err := http.Post(srv.URL()+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("record: status %d", resp.StatusCode)
	}

	key, _ := canonicalKey("POST", "/v1/chat/completions", body)
	raw, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatalf("fixture not written: %v", err)
	}
	var fx Fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var body2 map[string]any
	if err := json.Unmarshal(fx.Response.Body, &body2); err != nil || body2["ok"] != true {
		t.Errorf("fixture response wrong: %s", fx.Response.Body)
	}
	if fx.Response.Status != 200 {
		t.Errorf("fixture status = %d", fx.Response.Status)
	}

	// And the fixture replays.
	replay, err := NewReplayServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	resp2, err := http.Post(replay.URL()+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("recorded fixture must replay: %d", resp2.StatusCode)
	}
}
