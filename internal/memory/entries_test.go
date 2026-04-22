package memory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestDB(t *testing.T) (*storage.DB, *Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, NewStore(db)
}

func TestAddAndGet(t *testing.T) {
	_, store := setupTestDB(t)

	entry := Entry{
		ID:          "e1",
		Content:     "Self-attention mechanism in transformers",
		Tags:        []string{"concept", "attention"},
		ArticlePath: "wiki/concepts/self-attention.md",
	}
	if err := store.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := store.Get("e1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected entry, got nil")
	}
	if got.Content != entry.Content {
		t.Errorf("content mismatch: %q vs %q", got.Content, entry.Content)
	}
	if len(got.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(got.Tags))
	}
}

func TestUpdate(t *testing.T) {
	_, store := setupTestDB(t)

	store.Add(Entry{ID: "e1", Content: "original", Tags: []string{"old"}, ArticlePath: "a.md"})
	store.Update(Entry{ID: "e1", Content: "updated", Tags: []string{"new"}, ArticlePath: "b.md"})

	got, _ := store.Get("e1")
	if got.Content != "updated" {
		t.Errorf("expected updated content, got %q", got.Content)
	}
	if got.ArticlePath != "b.md" {
		t.Errorf("expected b.md, got %q", got.ArticlePath)
	}
}

func TestDelete(t *testing.T) {
	_, store := setupTestDB(t)

	store.Add(Entry{ID: "e1", Content: "to delete", ArticlePath: "a.md"})
	store.Delete("e1")

	got, _ := store.Get("e1")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestSearchBM25(t *testing.T) {
	_, store := setupTestDB(t)

	entries := []Entry{
		{ID: "e1", Content: "Self-attention is the core mechanism of transformer architectures", Tags: []string{"attention"}, ArticlePath: "a.md"},
		{ID: "e2", Content: "Flash attention optimizes memory usage during attention computation", Tags: []string{"attention", "optimization"}, ArticlePath: "b.md"},
		{ID: "e3", Content: "Convolutional neural networks use filters for feature extraction", Tags: []string{"cnn"}, ArticlePath: "c.md"},
	}
	for _, e := range entries {
		if err := store.Add(e); err != nil {
			t.Fatalf("Add %s: %v", e.ID, err)
		}
	}

	results, err := store.Search("attention transformer", nil, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// First result should be about attention/transformers
	if results[0].ID != "e1" && results[0].ID != "e2" {
		t.Errorf("expected attention-related result first, got %s", results[0].ID)
	}
}

func TestSearchWithTagFilter(t *testing.T) {
	_, store := setupTestDB(t)

	store.Add(Entry{ID: "e1", Content: "attention mechanism", Tags: []string{"attention"}, ArticlePath: "a.md"})
	store.Add(Entry{ID: "e2", Content: "attention optimization", Tags: []string{"attention", "optimization"}, ArticlePath: "b.md"})

	results, err := store.Search("attention", []string{"optimization"}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with optimization tag, got %d", len(results))
	}
	if results[0].ID != "e2" {
		t.Errorf("expected e2, got %s", results[0].ID)
	}
}

func TestCount(t *testing.T) {
	_, store := setupTestDB(t)

	store.Add(Entry{ID: "e1", Content: "a", ArticlePath: "a.md"})
	store.Add(Entry{ID: "e2", Content: "b", ArticlePath: "b.md"})

	count, err := store.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestBuildFTSQuery(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"attention transformer", `"attention"* OR "transformer"*`},
		{"the attention", `"attention"*`},
		{"is are", `"is"* OR "are"*`}, // all stopwords: use them anyway
	}
	for _, tt := range tests {
		got := buildFTSQuery(tt.input)
		if got != tt.expected {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSanitizeFTSPreservesCJK(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"重大资产重组", "重大资产重组"},
		{"hello世界", "hello世界"},
		{"注意力机制", "注意力机制"},
		{"トランスフォーマー", "トランスフォーマー"},       // katakana
		{"ひらがな", "ひらがな"},                       // hiragana
		{"변환기", "변환기"},                         // hangul
		{"𠀀古字", "𠀀古字"},                         // CJK Extension B (U+20000+)
		{"test*注入\"attack", "test注入attack"},     // strips FTS operators, keeps CJK
		{"「引号」标点", "引号标点"},                     // strips CJK punctuation U+300x
	}
	for _, tt := range tests {
		got := SanitizeFTS(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeFTS(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildFTSQueryCJK(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"重大资产重组", `"重大资产重组"*`},
		{"注意力 机制", `"注意力"* OR "机制"*`},
		{"transformer 注意力", `"transformer"* OR "注意力"*`},
	}
	for _, tt := range tests {
		got := buildFTSQuery(tt.input)
		if got != tt.expected {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSearchCJKContent(t *testing.T) {
	_, store := setupTestDB(t)

	entries := []Entry{
		{ID: "e1", Content: "重大资产重组是上市公司进行资产置换的重要方式", ArticlePath: "a.md"},
		{ID: "e2", Content: "注意力机制是Transformer架构的核心组件", ArticlePath: "b.md"},
		{ID: "e3", Content: "卷积神经网络使用滤波器进行特征提取", ArticlePath: "c.md"},
	}
	for _, e := range entries {
		if err := store.Add(e); err != nil {
			t.Fatalf("Add %s: %v", e.ID, err)
		}
	}

	results, err := store.Search("重大资产重组", nil, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for CJK query '重大资产重组', got none")
	}
	if results[0].ID != "e1" {
		t.Errorf("expected e1 as top result, got %s", results[0].ID)
	}
}

func TestBuildFTSQueryInjection(t *testing.T) {
	// FTS5 special characters should be stripped
	tests := []struct {
		input string
	}{
		{`tags:secret`},
		{`"hello" NEAR "world"`},
		{`NOT attention`},
		{`{col1 col2}: test`},
	}
	for _, tt := range tests {
		got := buildFTSQuery(tt.input)
		// Should not contain raw FTS5 operators
		if strings.Contains(got, "NEAR") || strings.Contains(got, "NOT ") || strings.Contains(got, "{") || strings.Contains(got, "tags:") {
			t.Errorf("buildFTSQuery(%q) contains FTS5 operator: %q", tt.input, got)
		}
	}
}
