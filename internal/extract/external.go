package extract

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ExternalParser defines a parser for a specific set of file extensions.
type ExternalParser struct {
	Extensions []string      `yaml:"extensions"`
	Command    string        `yaml:"command"`
	Args       []string      `yaml:"args,omitempty"`
	Timeout    time.Duration `yaml:"timeout,omitempty"`
}

// ExternalRegistry holds loaded external parsers.
type ExternalRegistry struct {
	parsers map[string]*ExternalParser
	Trusted bool // user explicitly set trust_external: true
}

// parserYAML is the on-disk format for parser.yaml.
type parserYAML struct {
	Parsers []ExternalParser `yaml:"parsers"`
}

// LoadExternalParsers reads parser definitions from a pack's parsers/ directory.
func LoadExternalParsers(projectDir string) (*ExternalRegistry, error) {
	path := filepath.Join(projectDir, "parsers", "parser.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ExternalRegistry{parsers: make(map[string]*ExternalParser)}, nil
		}
		return nil, fmt.Errorf("reading parser.yaml: %w", err)
	}

	var py parserYAML
	if err := yaml.Unmarshal(data, &py); err != nil {
		return nil, fmt.Errorf("parsing parser.yaml: %w", err)
	}

	parsersDir := filepath.Join(projectDir, "parsers")
	reg := &ExternalRegistry{parsers: make(map[string]*ExternalParser)}
	for i := range py.Parsers {
		p := &py.Parsers[i]
		if p.Command == "" {
			continue
		}
		if p.Timeout == 0 {
			p.Timeout = 30 * time.Second
		}
		if p.Timeout > 120*time.Second {
			p.Timeout = 120 * time.Second
		}

		// resolve relative commands against parsers/ directory
		if !filepath.IsAbs(p.Command) {
			candidate := filepath.Join(parsersDir, p.Command)
			if _, err := os.Stat(candidate); err == nil {
				p.Command = candidate
			}
		}

		// resolve relative script args against parsers/ directory
		for j, arg := range p.Args {
			if filepath.IsAbs(arg) {
				continue
			}
			candidate := filepath.Join(parsersDir, arg)
			if _, err := os.Stat(candidate); err == nil {
				p.Args[j] = candidate
			}
		}

		// verify command exists
		if _, err := exec.LookPath(p.Command); err != nil {
			fmt.Fprintf(os.Stderr, "warning: external parser command %q not found, skipping\n", p.Command)
			continue
		}

		for _, ext := range p.Extensions {
			ext = strings.TrimPrefix(ext, ".")
			reg.parsers["."+ext] = p
		}
	}

	return reg, nil
}

// Supports returns true if an external parser handles this extension.
func (r *ExternalRegistry) Supports(ext string) bool {
	if r == nil {
		return false
	}
	_, ok := r.parsers[ext]
	return ok
}

// Parse runs the external parser for the given extension.
// Fails closed: refuses to run unless a real sandbox is available or
// the user has explicitly set trust_external: true.
func (r *ExternalRegistry) Parse(content []byte, ext string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("no external registry")
	}
	if !canSandbox() && !r.Trusted {
		return "", fmt.Errorf("external parsers blocked: no sandbox available and trust_external not set")
	}
	p, ok := r.parsers[ext]
	if !ok {
		return "", fmt.Errorf("no external parser for %s", ext)
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.Command, p.Args...)
	cmd.Stdin = bytes.NewReader(content)

	tmpDir, err := os.MkdirTemp("", "sage-parser-*")
	if err != nil {
		return "", fmt.Errorf("creating sandbox dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	cmd.Dir = tmpDir
	cmd.Env = sandboxEnv(tmpDir)

	setSandboxAttrs(cmd)

	// Override default kill to use process group kill (kills child processes too)
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("external parser %q timed out after %s", p.Command, p.Timeout)
		}
		return "", fmt.Errorf("external parser %q failed: %s (stderr: %s)", p.Command, err, stderr.String())
	}

	return stdout.String(), nil
}

// HasParsers returns true if any external parsers are registered.
func (r *ExternalRegistry) HasParsers() bool {
	return r != nil && len(r.parsers) > 0
}

// sandboxEnv returns a minimal environment for sandboxed parser execution.
// HOME is set to the sandbox temp dir (passed separately) to prevent
// reading ~/.ssh, ~/.aws, ~/.netrc, etc.
func sandboxEnv(sandboxDir string) []string {
	env := []string{
		"HOME=" + sandboxDir,
	}
	if v := os.Getenv("PATH"); v != "" {
		env = append(env, "PATH="+v)
	}
	if v := os.Getenv("LANG"); v != "" {
		env = append(env, "LANG="+v)
	}
	if runtime.GOOS == "windows" {
		if v := os.Getenv("SYSTEMROOT"); v != "" {
			env = append(env, "SYSTEMROOT="+v)
		}
	}
	return env
}
