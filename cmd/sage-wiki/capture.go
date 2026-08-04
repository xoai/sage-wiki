package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/pkg/engine"
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
	captureCmd.Flags().Bool("upgrade", false, "Adopt a pre-format (v0.2.x) workspace (one-way)")
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

	// Config is loaded by the engine open below (WithConfigFile); the shim
	// needs no config of its own since engine.Capture owns the format.

	// SPEC-01: the capture write is a workspace mutation — take the
	// engine's single-writer lock so a capture during an active compile
	// fails fast (spec §B.1 8e) instead of racing the manifest. The file
	// format below is unchanged (parity); the engine's own
	// Workspace.Capture serves API consumers with its own simpler shape.
	upgrade, _ := cmd.Flags().GetBool("upgrade")
	var openOpts []engine.Option
	if upgrade {
		openOpts = append(openOpts, engine.WithUpgrade())
	}
	openOpts = append(openOpts, engine.WithConfigFile(resolveConfigPath(dir)))
	evOpts, evClose := cliEventPlane(cmd.Context(), dir)
	defer evClose()
	openOpts = append(openOpts, evOpts...)
	w, err := engine.Open(cmd.Context(), dir, openOpts...)
	if err != nil {
		return cli.CLIError(outputFormat, lockSentinel(err))
	}
	defer w.Close()
	if w.RequiresUpgrade() {
		return cli.CLIError(outputFormat, fmt.Errorf("workspace predates format versioning (v0.2.x) — re-run with --upgrade to adopt it (one-way)"))
	}

	// SPEC-01: one capture implementation (pack rule 2) — engine.Capture
	// owns the file format; the shim passes the CLI's metadata through.
	id, err := w.Capture(cmd.Context(), engine.Source{
		Reader:  strings.NewReader(content),
		Context: captureCtx,
		Tags:    tagsStr,
		Origin:  "cli-capture",
	})
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	relPath, _ := filepath.Rel(dir, string(id))

	msg := fmt.Sprintf("Captured to %s. Run `sage-wiki compile` to process.", relPath)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": relPath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
