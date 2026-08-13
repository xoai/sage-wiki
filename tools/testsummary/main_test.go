package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeFixtures(t *testing.T) {
	tests := []struct {
		name        string
		wantKind    FailureKind
		wantPackage string
		wantTest    string
		wantFile    string
		wantLine    int
		wantErr     bool
	}{
		{name: "success"},
		{name: "test-failure", wantKind: FailureTest, wantPackage: "example.com/project/widget", wantTest: "TestWidget", wantFile: "widget_test.go", wantLine: 42},
		{name: "build-failure", wantKind: FailureBuild, wantPackage: "example.com/project/broken", wantFile: "./broken.go", wantLine: 7},
		{name: "panic", wantKind: FailurePanic, wantPackage: "example.com/project/panic", wantTest: "TestPanics", wantFile: "/work/panic_test.go", wantLine: 19},
		{name: "timeout", wantKind: FailureTimeout, wantPackage: "example.com/project/slow", wantTest: "TestSlow", wantFile: "/work/slow_test.go", wantLine: 27},
		{name: "malformed", wantErr: true},
		{name: "empty", wantErr: true},
		{name: "package-failure", wantKind: FailurePackage, wantPackage: "example.com/project/setup"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := os.Open(filepath.Join("testdata", tt.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()

			summary, err := summarize(file)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("summarize = %#v, want error", summary)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantKind == "" {
				if len(summary.Failures) != 0 || summary.Packages != 1 || summary.PassedPackages != 1 {
					t.Fatalf("summary = %#v, want one passing package", summary)
				}
				return
			}
			if len(summary.Failures) == 0 {
				t.Fatalf("summary = %#v, want failure", summary)
			}
			failure := summary.Failures[0]
			if failure.Kind != tt.wantKind || failure.Package != tt.wantPackage || failure.Test != tt.wantTest || failure.File != tt.wantFile || failure.Line != tt.wantLine {
				t.Fatalf("failure = %#v, want kind=%q package=%q test=%q file=%q line=%d", failure, tt.wantKind, tt.wantPackage, tt.wantTest, tt.wantFile, tt.wantLine)
			}
		})
	}
}

func TestRenderFailureIsActionable(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "test-failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	summary, err := summarize(file)
	if err != nil {
		t.Fatal(err)
	}

	var annotations, markdown bytes.Buffer
	render(summary, ReportOptions{
		Standard:    "ordinary-go-correctness",
		Command:     "go test -json ./internal/widget -run '^TestWidget$'",
		Annotations: &annotations,
		Markdown:    &markdown,
	})
	for name, output := range map[string]string{"annotations": annotations.String(), "markdown": markdown.String()} {
		for _, want := range []string{"ordinary-go-correctness", "example.com/project/widget", "TestWidget", "widget_test.go", "42", "go test -json ./internal/widget -run '^TestWidget$'"} {
			if !strings.Contains(output, want) {
				t.Errorf("%s missing %q:\n%s", name, want, output)
			}
		}
	}
	if !strings.Contains(annotations.String(), "::error file=widget_test.go,line=42") {
		t.Fatalf("annotation has no source location:\n%s", annotations.String())
	}
}

func TestRunRejectsMalformedAndEmptyEvidence(t *testing.T) {
	for _, fixture := range []string{"malformed", "empty"} {
		t.Run(fixture, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{
				"--input", filepath.Join("testdata", fixture+".json"),
				"--standard", "ordinary-go-correctness",
				"--command", "go test -json ./...",
			}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("run succeeded: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunPassesThroughTheRawStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--input", filepath.Join("testdata", "success.json"),
		"--standard", "ordinary-go-correctness",
		"--command", "go test -json ./...",
		"--passthrough",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run = %d, stderr=%q", code, stderr.String())
	}
	want, err := os.ReadFile(filepath.Join("testdata", "success.json"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != string(want) {
		t.Fatalf("passthrough changed raw stream:\ngot:  %q\nwant: %q", stdout.String(), want)
	}
}

func TestRunGoTestsSelfTest(t *testing.T) {
	cmd := exec.Command("bash", "../../scripts/ci/run-go-tests.sh", "--self-test")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("self-test: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "run-go-tests self-test: OK") {
		t.Fatalf("unexpected self-test output: %s", output)
	}
}
