package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
)

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Capture knowledge from text",
	RunE:  runCapture,
}

func init() {
	captureCmd.Flags().String("text", "", "Text content to capture")
	captureCmd.Flags().String("file", "", "File to read (use - for stdin)")
	captureCmd.Flags().String("context", "", "Context description")
	captureCmd.Flags().String("tags", "", "Comma-separated tags")
}

func runCapture(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	text, _ := cmd.Flags().GetString("text")
	filePath, _ := cmd.Flags().GetString("file")
	captureCtx, _ := cmd.Flags().GetString("context")
	tagsStr, _ := cmd.Flags().GetString("tags")

	var content string
	if text != "" {
		content = text
	} else if filePath == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
		content = string(data)
	} else if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
		content = string(data)
	} else {
		return cli.CLIError(outputFormat, fmt.Errorf("provide --text or --file (use --file - for stdin)"))
	}

	if len(content) > 100*1024 {
		return cli.CLIError(outputFormat, fmt.Errorf("content too large (%d bytes, max 100KB)", len(content)))
	}

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	// Write as raw capture file (same as MCP fallback)
	capturesDir := filepath.Join(dir, "raw", "captures")
	os.MkdirAll(capturesDir, 0755)

	slug := fmt.Sprintf("capture-%s", cfg.Compiler.UserNow()[:10])
	relPath := filepath.Join("raw", "captures", slug+".md")
	absPath := filepath.Join(dir, relPath)
	for i := 1; ; i++ {
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			break
		}
		relPath = filepath.Join("raw", "captures", fmt.Sprintf("%s-%d.md", slug, i))
		absPath = filepath.Join(dir, relPath)
	}

	fm := fmt.Sprintf("---\nsource: cli-capture\ncaptured_at: %s\n", cfg.Compiler.UserNow())
	if tagsStr != "" {
		fm += fmt.Sprintf("tags: [%s]\n", tagsStr)
	}
	if captureCtx != "" {
		fm += fmt.Sprintf("context: %q\n", captureCtx)
	}
	fm += "---\n\n"

	if err := os.WriteFile(absPath, []byte(fm+content+"\n"), 0644); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	msg := fmt.Sprintf("Captured to %s. Run `sage-wiki compile` to process.", relPath)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": relPath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
