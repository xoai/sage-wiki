package compiler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestSummarizeOneForceBypassesCache 锁住 issue #219：`--force` 必须绕过磁盘摘要缓存。
//
// 现场：改完 prompts/*.md 再 `compile --force`，摘要文件的 mtime 与内容都不变
// （模板哈希进了 compile key ⇒ 文档确实被排进重编队列，但摘要正文被缓存顶掉）
// ⇒ 用户看不到任何变化，`--force` 的 "Recompile every doc" 承诺形同虚设。
func TestSummarizeOneForceBypassesCache(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := "output"
	sourceHash := "sha256:abc123"
	cachedBody := strings.Repeat("cached-", 20) // ≥100 字符；**不带首尾空格**（复用路径会 TrimSpace，带空格会让断言失真）

	buildSummaryFile(t, filepath.Join(projectDir, outputDir), "sources-doc", sourceHash, cachedBody)

	info := SourceInfo{Path: "sources/doc.md", Hash: sourceHash, Type: "article"}
	// regenerate=true：这里没有 LLM client，所以这次调用注定失败 —— 但**绝不能**把缓存
	// 正文当成结果返回，那正是 bug 的形态（"重编了"但内容一字未变）。
	result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, true)

	if result.Summary == cachedBody {
		t.Fatalf("--force 下不得复用磁盘摘要（issue #219）：拿到 %q", truncateForMsg(result.Summary))
	}
}

// TestSummarizeOneWithoutForceStillReuses 对照：不是 --force 时必须照旧复用缓存，
// 否则"中断后重跑"会重新计费（缓存的本意）。
func TestSummarizeOneWithoutForceStillReuses(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := "output"
	sourceHash := "sha256:abc123"
	cachedBody := strings.Repeat("cached-", 20)

	buildSummaryFile(t, filepath.Join(projectDir, outputDir), "sources-doc", sourceHash, cachedBody)

	info := SourceInfo{Path: "sources/doc.md", Hash: sourceHash, Type: "article"}
	result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, false)

	if result.Summary != cachedBody {
		t.Fatalf("非 --force 时应复用缓存，实际 %q", truncateForMsg(result.Summary))
	}
}

func truncateForMsg(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
