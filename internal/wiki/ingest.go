package wiki

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/pathsafe"
)

// IngestResult holds the outcome of an ingest operation.
type IngestResult struct {
	SourcePath string
	// Hash is the content hash registered in the manifest ("sha256:<hex>").
	Hash string
	Type string
	Size int64
}

// SkipSSRFCheck disables SSRF validation. Only for testing.
var SkipSSRFCheck bool

// binaryExtractorExts are the extensions the compile-time extractor
// dispatches to binary-aware tiers (internal/extract). Invalid-UTF-8
// content with one of these extensions routes to the source folder
// (extension preserved); anything else non-UTF-8 is rejected — binary
// bytes are never fed raw to FTS (SPEC-08 D3).
var binaryExtractorExts = map[string]bool{
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true,
	".epub": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".bmp": true,
}

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF32LE = []byte{0xFF, 0xFE, 0x00, 0x00}
	bomUTF32BE = []byte{0x00, 0x00, 0xFE, 0xFF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// EncodingGate applies the SPEC-08 ingestion encoding gate to raw content.
// BOM detection PRECEDES the extractor branch (spec edge case): UTF-16/32
// content is rejected even when the extension matches a binary extractor.
// Valid UTF-8 (after an optional UTF-8 BOM) passes as text. Invalid UTF-8
// routes only when the extension belongs to a known binary extractor tier.
// Exported so pkg/engine's capture surface applies the identical gate
// (one implementation per behavior — memchain ground rule 2).
func EncodingGate(path string, data []byte) error {
	base := filepath.Base(path)
	switch {
	case bytes.HasPrefix(data, bomUTF32LE), bytes.HasPrefix(data, bomUTF32BE),
		bytes.HasPrefix(data, bomUTF16LE), bytes.HasPrefix(data, bomUTF16BE):
		return fmt.Errorf("ingest: %s: UTF-16/32 content is not supported (detected via BOM): %w", base, limits.ErrEncoding)
	}
	body := bytes.TrimPrefix(data, bomUTF8)
	if utf8.Valid(body) {
		return nil
	}
	if binaryExtractorExts[strings.ToLower(filepath.Ext(path))] {
		return nil // routed to the extractor tier at compile time
	}
	return fmt.Errorf("ingest: %s: content is not valid UTF-8 and no binary extractor handles %q: %w",
		base, filepath.Ext(path), limits.ErrEncoding)
}

func IngestURL(projectDir string, url string) (*IngestResult, error) {
	// Validate URL scheme
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, fmt.Errorf("ingest: only http/https URLs are supported")
	}

	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, err
	}
	lim := cfg.Limits.Resolve()

	// Download with SSRF-safe client (validates IP at dial time, not before)
	client := safeHTTPClient(SkipSSRFCheck)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ingest: download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ingest: HTTP %d for %s", resp.StatusCode, url)
	}

	// Cap+1 read: an oversized body is an error, never a silent truncation
	// (SPEC-08: the old LimitReader swallowed the overflow).
	body, err := io.ReadAll(io.LimitReader(resp.Body, lim.MaxDocBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ingest: read body: %w", err)
	}
	if int64(len(body)) > lim.MaxDocBytes {
		return nil, limits.New(limits.WhichDocBytes, lim.MaxDocBytes, int64(len(body)))
	}

	if err := EncodingGate(url, body); err != nil {
		return nil, err
	}

	// Convert to markdown-ish format (basic: wrap in frontmatter)
	content := fmt.Sprintf("---\nsource_url: %s\ningested_at: %s\n---\n\n%s",
		url, cfg.Compiler.UserNow(), string(body))

	// Find first article-type source folder
	destDir := findSourceFolder(projectDir, cfg, "article")
	if destDir == "" {
		return nil, fmt.Errorf("ingest: no article source folder configured")
	}

	// Generate filename from URL
	filename := slugifyURL(url) + ".md"
	destPath := filepath.Join(destDir, filename)
	relPath, _ := filepath.Rel(projectDir, destPath)

	if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("ingest: write: %w", err)
	}

	// Update manifest under the exclusive lock (D4) so a concurrent compile or
	// another writer cannot clobber this source (lost update).
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
	mfPath := filepath.Join(projectDir, ".manifest.json")
	if err := manifest.Mutate(context.Background(), mfPath, func(mf *manifest.Manifest) error {
		mf.AddSource(relPath, hash, "article", int64(len(content)))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ingest: update manifest: %w", err)
	}

	log.Info("ingested URL", "url", url, "path", relPath)
	return &IngestResult{SourcePath: relPath, Hash: hash, Type: "article", Size: int64(len(content))}, nil
}

// IngestPath copies a local file to the appropriate source folder.
func IngestPath(projectDir string, srcPath string) (*IngestResult, error) {
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, err
	}
	lim := cfg.Limits.Resolve()

	absPath, err := filepath.Abs(srcPath)
	if err != nil {
		return nil, fmt.Errorf("ingest: invalid path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("ingest: file not found: %w", err)
	}

	// Size cap BEFORE reading the file into memory (SPEC-08 D3).
	if info.Size() > lim.MaxDocBytes {
		return nil, limits.New(limits.WhichDocBytes, lim.MaxDocBytes, info.Size())
	}

	var contentHead string
	if len(cfg.TypeSignals) > 0 {
		contentHead = extract.ReadHead(absPath, extract.DefaultHeadRunes)
	}
	signals := make([]extract.TypeSignal, len(cfg.TypeSignals))
	for i, s := range cfg.TypeSignals {
		signals[i] = extract.TypeSignal{
			Type:             s.Type,
			Pattern:          s.Pattern,
			FilenameKeywords: s.FilenameKeywords,
			ContentKeywords:  s.ContentKeywords,
			MinContentHits:   s.MinContentHits,
		}
	}
	srcType := extract.DetectSourceTypeWithSignals(absPath, contentHead, signals)
	destDir := findSourceFolder(projectDir, cfg, srcType)
	if destDir == "" {
		// Fallback to first source folder
		if len(cfg.Sources) > 0 {
			destDir = filepath.Join(projectDir, cfg.Sources[0].Path)
		} else {
			return nil, fmt.Errorf("ingest: no source folder configured")
		}
	}

	os.MkdirAll(destDir, 0755)

	destPath := filepath.Join(destDir, filepath.Base(absPath))
	// Defense-in-depth containment: destDir is config-derived and Base
	// neutralizes traversal, but the write target is verified anyway
	// (SPEC-08 D3 — pathsafe is the single containment answer).
	contained, err := pathsafe.Contained(projectDir, destPath)
	if err != nil || !contained {
		return nil, fmt.Errorf("ingest: destination escapes the workspace: %s: %w", destPath, limits.ErrTraversalTooWide)
	}
	relPath, _ := filepath.Rel(projectDir, destPath)

	// Copy file
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("ingest: read source: %w", err)
	}
	// Defense-in-depth re-check after the read: a file that grew between the
	// stat (line 172) and this ReadFile would bypass max_doc_bytes (TOCTOU).
	// The cost is one length comparison against an already-in-memory buffer.
	if int64(len(data)) > lim.MaxDocBytes {
		return nil, limits.New(limits.WhichDocBytes, lim.MaxDocBytes, int64(len(data)))
	}
	if err := EncodingGate(absPath, data); err != nil {
		return nil, err
	}
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return nil, fmt.Errorf("ingest: write dest: %w", err)
	}

	// Update manifest under the exclusive lock (D4) so a concurrent compile or
	// another writer cannot clobber this source (lost update).
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	mfPath := filepath.Join(projectDir, ".manifest.json")
	if err := manifest.Mutate(context.Background(), mfPath, func(mf *manifest.Manifest) error {
		mf.AddSource(relPath, hash, srcType, info.Size())
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ingest: update manifest: %w", err)
	}

	log.Info("ingested file", "source", absPath, "dest", relPath)
	return &IngestResult{SourcePath: relPath, Hash: hash, Type: srcType, Size: info.Size()}, nil
}

func findSourceFolder(projectDir string, cfg *config.Config, sourceType string) string {
	// Map source types to config types
	typeMap := map[string]string{
		"article": "article",
		"paper":   "paper",
		"code":    "code",
	}
	configType := typeMap[sourceType]

	// First try exact type match
	for _, s := range cfg.Sources {
		if s.Type == configType || s.Type == "auto" {
			return filepath.Join(projectDir, s.Path)
		}
	}

	// Fallback to first source
	if len(cfg.Sources) > 0 {
		return filepath.Join(projectDir, cfg.Sources[0].Path)
	}

	return ""
}

// safeHTTPClient creates an HTTP client that validates resolved IPs at dial time.
// This prevents DNS rebinding attacks (TOCTOU) where DNS returns a public IP
// for validation but a private IP for the actual connection.
func safeHTTPClient(skipSSRF bool) http.Client {
	if skipSSRF {
		return http.Client{Timeout: 30 * time.Second}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}

			// Resolve and validate IP before connecting
			ips, err := net.LookupHost(host)
			if err != nil {
				return nil, fmt.Errorf("ingest: DNS lookup failed for %s: %w", host, err)
			}

			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					continue
				}
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
					return nil, fmt.Errorf("ingest: blocked connection to private address %s (%s)", host, ipStr)
				}
			}

			// Connect to the validated address
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		},
	}

	return http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// Slugify reduces a label to a safe filename slug: lowercase alphanumerics,
// everything else collapsed to single hyphens, trimmed, capped at 80 chars.
// Shared by URL ingestion and capture-type labels (one implementation per
// behavior — memchain ground rule 2).
func Slugify(s string) string {
	var result strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}

	slug := result.String()
	// Clean up multiple dashes
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return slug
}

func slugifyURL(rawURL string) string {
	// Remove protocol
	s := strings.TrimPrefix(rawURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	return Slugify(s)
}
