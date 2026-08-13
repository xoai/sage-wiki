package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type FailureKind string

const (
	FailureTest    FailureKind = "test"
	FailureBuild   FailureKind = "build"
	FailurePanic   FailureKind = "panic"
	FailureTimeout FailureKind = "timeout"
	FailurePackage FailureKind = "package"
)

type testEvent struct {
	Action      string  `json:"Action"`
	ImportPath  string  `json:"ImportPath"`
	Package     string  `json:"Package"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	FailedBuild string  `json:"FailedBuild"`
}

type Failure struct {
	Kind    FailureKind
	Package string
	Test    string
	File    string
	Line    int
	Message string
}

type Summary struct {
	Events         int
	Packages       int
	PassedPackages int
	Failures       []Failure
}

type ReportOptions struct {
	Standard    string
	Command     string
	Annotations io.Writer
	Markdown    io.Writer
}

type packageState struct {
	started     bool
	terminal    bool
	passed      bool
	activeTests map[string]struct{}
	output      []string
	testOutput  map[string][]string
	failures    []Failure
}

var sourceLocation = regexp.MustCompile(`(?m)([^\s:]+\.go):(\d+)(?::\d+)?`)

func summarize(input io.Reader) (Summary, error) {
	return summarizeStream(input, nil)
}

func summarizeStream(input io.Reader, passthrough io.Writer) (Summary, error) {
	states := map[string]*packageState{}
	buildOutput := map[string][]string{}
	buildFailed := map[string]bool{}
	scanner := bufio.NewScanner(input)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	var summary Summary
	for scanner.Scan() {
		line := scanner.Bytes()
		if passthrough != nil {
			if _, err := passthrough.Write(append(append([]byte(nil), line...), '\n')); err != nil {
				return Summary{}, fmt.Errorf("write go test JSON passthrough: %w", err)
			}
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			return Summary{}, errors.New("blank line in go test JSON stream")
		}
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return Summary{}, fmt.Errorf("event %d: malformed go test JSON: %w", summary.Events+1, err)
		}
		if event.Action == "build-output" || event.Action == "build-fail" {
			if event.ImportPath == "" {
				return Summary{}, fmt.Errorf("event %d: build event import path is empty", summary.Events+1)
			}
			summary.Events++
			if event.Action == "build-output" {
				buildOutput[event.ImportPath] = append(buildOutput[event.ImportPath], event.Output)
			} else {
				buildFailed[event.ImportPath] = true
			}
			continue
		}
		if event.Package == "" {
			return Summary{}, fmt.Errorf("event %d: package is empty", summary.Events+1)
		}
		if !validAction(event.Action) {
			return Summary{}, fmt.Errorf("event %d: unknown action %q", summary.Events+1, event.Action)
		}
		summary.Events++
		state := states[event.Package]
		if state == nil {
			state = &packageState{activeTests: map[string]struct{}{}, testOutput: map[string][]string{}}
			states[event.Package] = state
		}
		if state.terminal {
			return Summary{}, fmt.Errorf("event %d: package %s emitted %s after its terminal event", summary.Events, event.Package, event.Action)
		}
		switch event.Action {
		case "start":
			if event.Test != "" || state.started {
				return Summary{}, fmt.Errorf("event %d: invalid duplicate or test-level start", summary.Events)
			}
			state.started = true
		case "run", "cont":
			if event.Test == "" {
				return Summary{}, fmt.Errorf("event %d: %s action has no test", summary.Events, event.Action)
			}
			state.activeTests[event.Test] = struct{}{}
		case "pause":
			if event.Test == "" {
				return Summary{}, fmt.Errorf("event %d: pause action has no test", summary.Events)
			}
			delete(state.activeTests, event.Test)
		case "output":
			state.output = append(state.output, event.Output)
			if event.Test != "" {
				state.testOutput[event.Test] = append(state.testOutput[event.Test], event.Output)
			}
		case "pass", "skip", "bench":
			if event.Test != "" {
				delete(state.activeTests, event.Test)
			} else if event.Action != "bench" {
				state.terminal = true
				state.passed = event.Action == "pass" || event.Action == "skip"
			}
		case "fail":
			if event.Test != "" {
				delete(state.activeTests, event.Test)
				state.failures = append(state.failures, failureFromOutput(event.Package, event.Test, event.FailedBuild, state.testOutput[event.Test]))
			} else {
				state.terminal = true
				if event.FailedBuild != "" {
					if !buildFailed[event.FailedBuild] {
						return Summary{}, fmt.Errorf("event %d: failed build %s has no build-fail event", summary.Events, event.FailedBuild)
					}
					state.failures = append(state.failures, failureFromOutput(event.Package, "", event.FailedBuild, buildOutput[event.FailedBuild]))
				} else if len(state.failures) == 0 {
					state.failures = append(state.failures, packageFailure(event.Package, state))
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, fmt.Errorf("read go test JSON: %w", err)
	}
	if summary.Events == 0 {
		return Summary{}, errors.New("go test JSON stream is empty")
	}
	for pkg, state := range states {
		if !state.started {
			return Summary{}, fmt.Errorf("package %s has no start event", pkg)
		}
		if !state.terminal {
			return Summary{}, fmt.Errorf("package %s has no terminal event", pkg)
		}
		summary.Packages++
		if state.passed {
			summary.PassedPackages++
		}
		summary.Failures = append(summary.Failures, state.failures...)
	}
	for importPath := range buildFailed {
		found := false
		for _, state := range states {
			for _, failure := range state.failures {
				if failure.Kind == FailureBuild && failure.Package == importPath {
					found = true
				}
			}
		}
		if !found {
			return Summary{}, fmt.Errorf("build failure %s has no terminal test event", importPath)
		}
	}
	sort.Slice(summary.Failures, func(i, j int) bool {
		a, b := summary.Failures[i], summary.Failures[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		if a.Test != b.Test {
			return a.Test < b.Test
		}
		return a.Kind < b.Kind
	})
	return summary, nil
}

func validAction(action string) bool {
	switch action {
	case "start", "run", "pause", "cont", "pass", "bench", "fail", "output", "skip":
		return true
	default:
		return false
	}
}

func failureFromOutput(pkg, test, failedBuild string, output []string) Failure {
	joined := strings.Join(output, "")
	failure := Failure{Kind: FailureTest, Package: pkg, Test: test, Message: firstUsefulLine(joined)}
	if failedBuild != "" {
		failure.Kind = FailureBuild
		failure.Package = failedBuild
	} else if strings.Contains(joined, "test timed out") {
		failure.Kind = FailureTimeout
	} else if strings.Contains(joined, "panic:") {
		failure.Kind = FailurePanic
	}
	failure.File, failure.Line = findLocation(joined)
	return failure
}

func packageFailure(pkg string, state *packageState) Failure {
	joined := strings.Join(state.output, "")
	test := ""
	if strings.Contains(joined, "test timed out") && len(state.activeTests) > 0 {
		tests := make([]string, 0, len(state.activeTests))
		for name := range state.activeTests {
			tests = append(tests, name)
		}
		sort.Strings(tests)
		test = tests[0]
	}
	failure := failureFromOutput(pkg, test, "", state.output)
	if failure.Kind == FailureTest {
		failure.Kind = FailurePackage
	}
	return failure
}

func firstUsefulLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "=== RUN") && !strings.HasPrefix(line, "--- FAIL") && !strings.HasPrefix(line, "# ") {
			return line
		}
	}
	return "go test reported a failure without diagnostic output"
}

func findLocation(output string) (string, int) {
	matches := sourceLocation.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return "", 0
	}
	match := matches[0]
	for _, candidate := range matches {
		if strings.HasSuffix(candidate[1], "_test.go") {
			match = candidate
			break
		}
	}
	line, err := strconv.Atoi(match[2])
	if err != nil {
		return "", 0
	}
	return match[1], line
}

func render(summary Summary, options ReportOptions) {
	if options.Annotations != nil {
		for _, failure := range summary.Failures {
			properties := ""
			if failure.File != "" {
				properties = " file=" + escapeProperty(failure.File)
				if failure.Line > 0 {
					properties += ",line=" + strconv.Itoa(failure.Line)
				}
			}
			message := failureTitle(options.Standard, failure) + "; reproduce: " + options.Command
			fmt.Fprintf(options.Annotations, "::error%s::%s\n", properties, escapeMessage(message))
		}
	}
	if options.Markdown == nil {
		return
	}
	fmt.Fprintf(options.Markdown, "## Go test summary: `%s`\n\n", options.Standard)
	if len(summary.Failures) == 0 {
		fmt.Fprintf(options.Markdown, "Passed %d package(s).\n\n", summary.PassedPackages)
	} else {
		fmt.Fprintf(options.Markdown, "Failed with %d actionable failure(s):\n\n", len(summary.Failures))
		for _, failure := range summary.Failures {
			location := failure.Package
			if failure.Test != "" {
				location += " / " + failure.Test
			}
			if failure.File != "" {
				location += fmt.Sprintf(" / %s:%d", failure.File, failure.Line)
			}
			fmt.Fprintf(options.Markdown, "- **%s** `%s`: %s\n", failure.Kind, location, failure.Message)
		}
		fmt.Fprintln(options.Markdown)
	}
	fmt.Fprintf(options.Markdown, "Reproduce with:\n\n`%s`\n", strings.ReplaceAll(options.Command, "`", "\\`"))
}

func failureTitle(standard string, failure Failure) string {
	parts := []string{standard, string(failure.Kind), failure.Package}
	if failure.Test != "" {
		parts = append(parts, failure.Test)
	}
	parts = append(parts, failure.Message)
	return strings.Join(parts, ": ")
}

func escapeProperty(value string) string {
	replacer := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C")
	return replacer.Replace(value)
}

func escapeMessage(value string) string {
	replacer := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return replacer.Replace(value)
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("testsummary", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "-", "go test -json stream, or - for stdin")
	standard := flags.String("standard", "", "quality standard ID")
	command := flags.String("command", "", "exact reproduction command")
	summaryPath := flags.String("summary", "", "GitHub step summary path")
	passthrough := flags.Bool("passthrough", false, "copy the raw JSON stream to stdout as it is parsed")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *standard == "" || *command == "" {
		fmt.Fprintln(stderr, "--standard and --command are required")
		return 2
	}
	input := io.Reader(os.Stdin)
	var inputFile *os.File
	var err error
	if *inputPath != "-" {
		inputFile, err = os.Open(*inputPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		defer inputFile.Close()
		input = inputFile
	}
	var stream io.Writer
	if *passthrough {
		stream = stdout
	}
	summary, err := summarizeStream(input, stream)
	if err != nil {
		fmt.Fprintln(stderr, "testsummary:", err)
		return 2
	}
	var markdown io.Writer
	var summaryFile *os.File
	if *summaryPath != "" {
		summaryFile, err = os.OpenFile(*summaryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		defer summaryFile.Close()
		markdown = summaryFile
	}
	annotations := stdout
	if *passthrough && len(summary.Failures) == 0 {
		annotations = nil
	}
	render(summary, ReportOptions{Standard: *standard, Command: *command, Annotations: annotations, Markdown: markdown})
	if len(summary.Failures) > 0 {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
