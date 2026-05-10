package extract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadExternalParsers_Valid(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "parsers"), 0o755)

	// use "echo" as a test command (available on all platforms)
	yaml := `parsers:
  - extensions: [ipynb, notebook]
    command: echo
    timeout: 10s
`
	os.WriteFile(filepath.Join(dir, "parsers", "parser.yaml"), []byte(yaml), 0o644)

	reg, err := LoadExternalParsers(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !reg.Supports(".ipynb") {
		t.Error("should support .ipynb")
	}
	if !reg.Supports(".notebook") {
		t.Error("should support .notebook")
	}
	if reg.Supports(".md") {
		t.Error("should not support .md")
	}
}

func TestLoadExternalParsers_MissingFile(t *testing.T) {
	reg, err := LoadExternalParsers(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reg.HasParsers() {
		t.Error("should have no parsers")
	}
}

func TestLoadExternalParsers_MissingCommand(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "parsers"), 0o755)

	yaml := `parsers:
  - extensions: [xyz]
    command: nonexistent-command-12345
`
	os.WriteFile(filepath.Join(dir, "parsers", "parser.yaml"), []byte(yaml), 0o644)

	reg, err := LoadExternalParsers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Supports(".xyz") {
		t.Error("parser with missing command should be skipped")
	}
}

func TestExternalRegistry_Parse_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo test requires unix shell")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "parsers"), 0o755)

	// create a parser script that reads stdin and outputs it uppercased
	script := filepath.Join(dir, "parsers", "upper.sh")
	os.WriteFile(script, []byte("#!/bin/sh\ntr '[:lower:]' '[:upper:]'\n"), 0o755)

	yaml := `parsers:
  - extensions: [custom]
    command: ` + script + `
    timeout: 5s
`
	os.WriteFile(filepath.Join(dir, "parsers", "parser.yaml"), []byte(yaml), 0o644)

	reg, err := LoadExternalParsers(dir)
	if err != nil {
		t.Fatal(err)
	}

	reg.Trusted = true
	result, err := reg.Parse([]byte("hello world"), ".custom")
	if err != nil {
		t.Fatal(err)
	}

	trimmed := strings.TrimSpace(result)
	if trimmed != "HELLO WORLD" {
		t.Errorf("result = %q, want HELLO WORLD", trimmed)
	}
}

func TestExternalRegistry_Parse_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep test requires unix shell")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "parsers"), 0o755)

	script := filepath.Join(dir, "parsers", "slow.sh")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755)

	yaml := `parsers:
  - extensions: [slow]
    command: ` + script + `
    timeout: 1s
`
	os.WriteFile(filepath.Join(dir, "parsers", "parser.yaml"), []byte(yaml), 0o644)

	reg, err := LoadExternalParsers(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg.Trusted = true

	_, err = reg.Parse([]byte("input"), ".slow")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestExternalRegistry_Parse_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exit test requires unix shell")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "parsers"), 0o755)

	script := filepath.Join(dir, "parsers", "fail.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'parse error' >&2\nexit 1\n"), 0o755)

	yaml := `parsers:
  - extensions: [fail]
    command: ` + script + `
`
	os.WriteFile(filepath.Join(dir, "parsers", "parser.yaml"), []byte(yaml), 0o644)

	reg, err := LoadExternalParsers(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg.Trusted = true

	_, err = reg.Parse([]byte("input"), ".fail")
	if err == nil {
		t.Fatal("expected error from non-zero exit")
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("expected stderr in error, got: %v", err)
	}
}

func TestExternalRegistry_NilSafe(t *testing.T) {
	var reg *ExternalRegistry
	if reg.Supports(".xyz") {
		t.Error("nil registry should not support anything")
	}
	if reg.HasParsers() {
		t.Error("nil registry should have no parsers")
	}
}

func TestExternalRegistry_Parse_FailClosed(t *testing.T) {
	if canSandbox() {
		t.Skip("sandbox is available — fail-closed not triggered")
	}

	reg := &ExternalRegistry{
		parsers: map[string]*ExternalParser{
			".test": {Extensions: []string{"test"}, Command: "echo"},
		},
		Trusted: false, // not trusted, no sandbox
	}

	_, err := reg.Parse([]byte("input"), ".test")
	if err == nil {
		t.Fatal("expected error: should refuse without sandbox or trust")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should mention 'blocked', got: %v", err)
	}
}

func TestExtract_ExternalParserInvoked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires unix shell")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "parsers"), 0o755)

	script := filepath.Join(dir, "parsers", "custom.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'extracted by external parser'\n"), 0o755)

	yaml := `parsers:
  - extensions: [xyz]
    command: ` + script + `
`
	os.WriteFile(filepath.Join(dir, "parsers", "parser.yaml"), []byte(yaml), 0o644)

	reg, err := LoadExternalParsers(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg.Trusted = true

	// create a .xyz file
	testFile := filepath.Join(dir, "test.xyz")
	os.WriteFile(testFile, []byte("raw data"), 0o644)

	// without opts — falls back to plain text
	content, err := Extract(testFile, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if content.ExtractEngine == "external" {
		t.Error("should not use external parser without opts")
	}

	// with opts but parsers disabled — falls back to plain text
	opts := ExtractOpts{ExternalParsers: reg, ParsersEnabled: false}
	content, err = Extract(testFile, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if content.ExtractEngine == "external" {
		t.Error("should not use external parser when disabled")
	}

	// with opts and parsers enabled — uses external parser
	opts = ExtractOpts{ExternalParsers: reg, ParsersEnabled: true}
	content, err = Extract(testFile, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if content.ExtractEngine != "external" {
		t.Errorf("ExtractEngine = %q, want external", content.ExtractEngine)
	}
	if !strings.Contains(content.Text, "extracted by external parser") {
		t.Errorf("text = %q, expected external parser output", content.Text)
	}
}

func TestExtract_BuiltinTakesPrecedence(t *testing.T) {
	// .md files should always use built-in parser even with external parsers
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.md")
	os.WriteFile(testFile, []byte("# Hello"), 0o644)

	reg := &ExternalRegistry{parsers: map[string]*ExternalParser{
		".md": {Extensions: []string{"md"}, Command: "echo"},
	}}

	opts := ExtractOpts{ExternalParsers: reg, ParsersEnabled: true}
	content, err := Extract(testFile, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if content.ExtractEngine == "external" {
		t.Error("built-in parser should take precedence over external")
	}
}

func TestSandboxEnv(t *testing.T) {
	sandboxDir := "/tmp/test-sandbox"
	env := sandboxEnv(sandboxDir)
	allowed := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "SYSTEMROOT": true}
	for _, e := range env {
		key, val, _ := strings.Cut(e, "=")
		if !allowed[key] {
			t.Errorf("unexpected env var: %s", key)
		}
		// HOME must point to sandbox dir, not real home
		if key == "HOME" && val != sandboxDir {
			t.Errorf("HOME = %q, want %q (sandbox dir)", val, sandboxDir)
		}
	}
}
