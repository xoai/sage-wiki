package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")

	content := `---
title: Test Article
tags: [attention, transformer]
---

# Self-Attention

Self-attention is a mechanism for computing contextual representations.

## How it works

It uses queries, keys, and values.
`
	os.WriteFile(path, []byte(content), 0644)

	result, err := Extract(path, "article")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if result.Type != "article" {
		t.Errorf("expected article, got %s", result.Type)
	}
	if result.Frontmatter == "" {
		t.Error("expected frontmatter to be extracted")
	}
	if result.Text == "" {
		t.Error("expected body text")
	}
	// Frontmatter should be stripped from text
	if result.Text[:1] == "-" {
		t.Error("frontmatter should be stripped from text")
	}
}

func TestExtractCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte("package main\nfunc main() {}"), 0644)

	result, err := Extract(path, "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Type != "code" {
		t.Errorf("expected code, got %s", result.Type)
	}
}

func TestChunkIfNeededSmall(t *testing.T) {
	content := &SourceContent{
		Text: "Short text that fits in one chunk.",
	}
	ChunkIfNeeded(content, 1000)

	if content.ChunkCount != 1 {
		t.Errorf("expected 1 chunk, got %d", content.ChunkCount)
	}
}

func TestChunkByHeadings(t *testing.T) {
	text := `# Introduction

This is the intro section with plenty of text to make it substantial enough.

## Methods

This is the methods section with details about the approach.

## Results

This is the results section with findings.
`
	content := &SourceContent{Text: text}
	ChunkIfNeeded(content, 20) // Very small token limit to force chunking

	if content.ChunkCount < 2 {
		t.Errorf("expected multiple chunks, got %d", content.ChunkCount)
	}

	// Each chunk should have content
	for i, chunk := range content.Chunks {
		if chunk.Text == "" {
			t.Errorf("chunk %d is empty", i)
		}
	}
}

func TestChunkByParagraphs(t *testing.T) {
	// No headings — should split on double newlines
	text := "Paragraph one with some text.\n\nParagraph two with more text.\n\nParagraph three with even more text."

	content := &SourceContent{Text: text}
	ChunkIfNeeded(content, 10) // Very small limit

	if content.ChunkCount < 2 {
		t.Errorf("expected multiple chunks, got %d", content.ChunkCount)
	}
}

func TestExtractCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.csv")
	os.WriteFile(path, []byte("name,age,city\nAlice,30,NYC\nBob,25,LA\n"), 0644)

	result, err := Extract(path, "dataset")
	if err != nil {
		t.Fatalf("Extract CSV: %v", err)
	}
	if result.Type != "dataset" {
		t.Errorf("expected dataset, got %s", result.Type)
	}
	if !strings.Contains(result.Text, "Alice") {
		t.Error("expected CSV content to contain 'Alice'")
	}
	if !strings.Contains(result.Text, "Headers:") {
		t.Error("expected CSV to have headers line")
	}
}

func TestExtractPlainText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.log")
	os.WriteFile(path, []byte("2026-04-06 INFO: System started\n2026-04-06 ERROR: Something failed\n"), 0644)

	result, err := Extract(path, "")
	if err != nil {
		t.Fatalf("Extract log: %v", err)
	}
	if !strings.Contains(result.Text, "System started") {
		t.Error("expected log content")
	}
}

func TestExtractEmail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.eml")
	eml := "From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Test Email\r\nDate: Mon, 06 Apr 2026 10:00:00 +0000\r\n\r\nHello Bob,\r\nThis is a test email.\r\n"
	os.WriteFile(path, []byte(eml), 0644)

	result, err := Extract(path, "")
	if err != nil {
		t.Fatalf("Extract email: %v", err)
	}
	if !strings.Contains(result.Text, "Subject: Test Email") {
		t.Error("expected email subject")
	}
	if !strings.Contains(result.Text, "Hello Bob") {
		t.Error("expected email body")
	}
}

func TestEstimateTokensEmpty(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Errorf("expected 0 for empty string, got %d", EstimateTokens(""))
	}
}

func TestEstimateTokensWhitespace(t *testing.T) {
	tokens := EstimateTokens("   \n\t  ")
	if tokens != 1 { // 7 chars / 4 = 1
		t.Errorf("expected 1 for whitespace, got %d", tokens)
	}
}

func TestEstimateTokensSingleCJK(t *testing.T) {
	tokens := EstimateTokens("你")
	if tokens != 1 { // int(1 * 1.5) = 1
		t.Errorf("expected 1 for single CJK char, got %d", tokens)
	}
}

func TestEstimateTokensLatin(t *testing.T) {
	// 400 chars of ASCII → ~100 tokens
	text := strings.Repeat("abcd", 100)
	tokens := EstimateTokens(text)
	if tokens < 90 || tokens > 110 {
		t.Errorf("expected ~100 tokens for 400 Latin chars, got %d", tokens)
	}
}

func TestEstimateTokensCJK(t *testing.T) {
	// 100 CJK characters → ~150 tokens
	text := strings.Repeat("自注意力机制", 17) // 102 CJK chars
	tokens := EstimateTokens(text)
	if tokens < 140 || tokens > 160 {
		t.Errorf("expected ~150 tokens for ~100 CJK chars, got %d", tokens)
	}
}

func TestEstimateTokensMixed(t *testing.T) {
	// Mix of CJK and Latin
	text := "Self-Attention 自注意力机制 computes representations 计算表示"
	tokens := EstimateTokens(text)
	// 9 CJK chars (~13.5 tokens) + ~40 Latin chars (~10 tokens) ≈ 23
	if tokens < 15 || tokens > 35 {
		t.Errorf("expected ~23 tokens for mixed text, got %d", tokens)
	}
}

func TestChunkCJKText(t *testing.T) {
	// CJK text with paragraph breaks — should chunk based on token estimation
	// Each paragraph: 10 CJK chars ≈ 15 tokens. 20 paragraphs ≈ 300 tokens.
	var paragraphs []string
	for i := 0; i < 20; i++ {
		paragraphs = append(paragraphs, "这是一段中文测试文本内容。")
	}
	cjkText := strings.Join(paragraphs, "\n\n")
	content := &SourceContent{Text: cjkText}
	ChunkIfNeeded(content, 50) // 50 token limit → should produce ~6 chunks

	if content.ChunkCount < 3 {
		t.Errorf("expected 3+ chunks for CJK paragraphs at 50 token limit, got %d", content.ChunkCount)
	}
}

func TestDetectSourceType(t *testing.T) {
	// Backward compat: nil signals = extension-only
	tests := []struct {
		path     string
		expected string
	}{
		{"paper.pdf", "paper"},
		{"article.md", "article"},
		{"notes.txt", "article"},
		{"main.go", "code"},
		{"script.py", "code"},
		{"data.csv", "dataset"},
		{"report.docx", "article"},
		{"slides.pptx", "article"},
		{"data.xlsx", "dataset"},
		{"book.epub", "article"},
		{"mail.eml", "article"},
		{"output.log", "article"},
		{"transcript.vtt", "article"},
	}
	for _, tt := range tests {
		got := DetectSourceTypeWithSignals(tt.path, "", nil)
		if got != tt.expected {
			t.Errorf("DetectSourceTypeWithSignals(%s, \"\", nil) = %s, want %s", tt.path, got, tt.expected)
		}
	}
}

func TestDetectSourceTypeWithSignals(t *testing.T) {
	signals := []TypeSignal{
		{
			Type:             "regulation",
			FilenameKeywords: []string{"法规", "办法"},
			ContentKeywords:  []string{"第一条", "第二条", "为了规范"},
			MinContentHits:   2,
		},
		{
			Type:             "research",
			FilenameKeywords: []string{"研报"},
			ContentKeywords:  []string{"投资评级", "目标价"},
			MinContentHits:   1,
		},
	}

	tests := []struct {
		name        string
		path        string
		contentHead string
		expected    string
	}{
		{"filename match", "/path/证券法规汇编.pdf", "", "regulation"},
		{"content match", "/path/document.pdf", "第一条 为了规范证券市场 第二条 适用范围", "regulation"},
		{"content below threshold", "/path/doc.pdf", "第一条 只有一个关键词", "paper"},
		{"research filename", "/path/AI研报.pdf", "", "research"},
		{"research content", "/path/report.pdf", "本报告投资评级为买入", "research"},
		{"no match fallback pdf", "/path/random.pdf", "no keywords here", "paper"},
		{"no match fallback md", "/path/notes.md", "no keywords here", "article"},
		{"signal priority", "/path/法规研报.pdf", "", "regulation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectSourceTypeWithSignals(tt.path, tt.contentHead, signals)
			if got != tt.expected {
				t.Errorf("DetectSourceTypeWithSignals(%s) = %s, want %s", tt.name, got, tt.expected)
			}
		})
	}
}

func TestReadHead(t *testing.T) {
	dir := t.TempDir()

	// ASCII file
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("Hello, World! This is a test file with some content."), 0644)
	got := ReadHead(path, 5)
	if got != "Hello" {
		t.Errorf("ReadHead(5) = %q, want %q", got, "Hello")
	}

	// Chinese content
	cnPath := filepath.Join(dir, "chinese.txt")
	os.WriteFile(cnPath, []byte("第一条 为了规范证券发行"), 0644)
	got = ReadHead(cnPath, 10)
	if len([]rune(got)) > 10 {
		t.Errorf("ReadHead(10) returned %d runes, want <= 10", len([]rune(got)))
	}

	// Non-existent file
	got = ReadHead("/nonexistent/file.txt", 100)
	if got != "" {
		t.Errorf("ReadHead(nonexistent) = %q, want empty", got)
	}

	// File shorter than limit
	got = ReadHead(path, 10000)
	if got != "Hello, World! This is a test file with some content." {
		t.Errorf("ReadHead(10000) = %q, want full content", got)
	}
}
