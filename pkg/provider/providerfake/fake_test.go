package providerfake

import (
	"context"
	"testing"

	"github.com/xoai/sage-wiki/pkg/provider"
)

func TestFakeCompleteScriptingAndDeterminism(t *testing.T) {
	f := New("default answer")
	f.Responses["attention"] = "attention answer"
	f.Model = "fake-1"

	resp, err := f.Complete(context.Background(), provider.CompleteRequest{
		Messages: []provider.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "tell me about attention"},
		},
		Model: "fake-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "attention answer" {
		t.Errorf("Content = %q, want scripted", resp.Content)
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Error("usage must be populated")
	}
	if !f.Called("attention") {
		t.Error("Called(attention) = false")
	}

	resp2, _ := f.Complete(context.Background(), provider.CompleteRequest{
		Messages: []provider.Message{{Role: "user", Content: "unmatched"}},
	})
	if resp2.Content != "default answer" {
		t.Errorf("unmatched = %q, want default", resp2.Content)
	}
}

func TestFakeEmbedDeterministic(t *testing.T) {
	f := New("")
	a, _ := f.Embed(context.Background(), []string{"hello", "world"})
	b, _ := f.Embed(context.Background(), []string{"hello", "world"})
	if len(a) != 2 || len(a[0]) != 8 {
		t.Fatalf("shape = %v", a)
	}
	for i := range a[0] {
		if a[0][i] != b[0][i] {
			t.Fatal("embeddings must be deterministic")
		}
	}
	if a[0][0] == a[1][0] {
		t.Error("different texts should embed differently (with high probability)")
	}
	models, _ := f.Models(context.Background())
	if len(models) != 1 || models[0].Pricing != nil {
		t.Error("Models must list one unpriced fake model")
	}
}
