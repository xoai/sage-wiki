package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/storage"
)

var learnCmd = &cobra.Command{
	Use:   "learn [content]",
	Short: "Store a learning entry",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runLearn,
}

func init() {
	learnCmd.Flags().String("type", "gotcha", "Learning type: gotcha, correction, convention, error-fix, api-drift")
	learnCmd.Flags().String("tags", "", "Comma-separated tags")
}

func runLearn(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	content := strings.Join(args, " ")
	learnType, _ := cmd.Flags().GetString("type")
	tagsStr, _ := cmd.Flags().GetString("tags")

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	if err := linter.StoreLearning(db, learnType, content, tagsStr, "cli"); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	display := content
	if len(display) > 80 {
		display = display[:80] + "..."
	}
	msg := fmt.Sprintf("Learning stored: [%s] %s", learnType, display)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"message": msg}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
