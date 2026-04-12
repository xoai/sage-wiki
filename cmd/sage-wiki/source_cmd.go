package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/manifest"
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Inspect source files and pre-extracted content",
}

var sourceShowCmd = &cobra.Command{
	Use:   "show <path>",
	Short: "Show pre-extracted content or source metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runSourceShow,
}

var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sources with extraction/compilation status",
	RunE:  runSourceList,
}

func init() {
	sourceShowCmd.Flags().Bool("meta-only", false, "Show only metadata, not content")

	sourceCmd.AddCommand(sourceShowCmd, sourceListCmd)
}

func runSourceShow(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	relPath := args[0]
	metaOnly, _ := cmd.Flags().GetBool("meta-only")

	sc, err := extract.TryPreExtracted(dir, relPath)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if sc == nil {
		// 无预提取内容，显示源文件元信息
		absPath := filepath.Join(dir, relPath)
		info, statErr := os.Stat(absPath)
		if statErr != nil {
			msg := fmt.Sprintf("source not found: %s", relPath)
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, msg))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		result := map[string]interface{}{
			"path":          relPath,
			"pre_extracted": false,
			"size_bytes":    info.Size(),
			"type":          extract.DetectSourceType(absPath),
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, result, ""))
			return nil
		}
		fmt.Printf("Source: %s\n", relPath)
		fmt.Printf("  Pre-extracted: no\n")
		fmt.Printf("  Size: %d bytes\n", info.Size())
		fmt.Printf("  Type: %s\n", extract.DetectSourceType(absPath))
		return nil
	}

	// 有预提取内容
	result := map[string]interface{}{
		"path":          relPath,
		"pre_extracted": true,
		"confidence":    sc.Confidence,
		"engine":        sc.ExtractEngine,
	}
	if !metaOnly {
		result["text_length"] = len(sc.Text)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
		return nil
	}

	fmt.Printf("Source: %s\n", relPath)
	fmt.Printf("  Pre-extracted: yes\n")
	fmt.Printf("  Confidence: %s\n", sc.Confidence)
	fmt.Printf("  Engine: %s\n", sc.ExtractEngine)
	if !metaOnly {
		fmt.Printf("  Text length: %d chars\n", len(sc.Text))
		fmt.Println("---")
		preview := sc.Text
		if len(preview) > 500 {
			preview = preview[:500] + "\n... (truncated)"
		}
		fmt.Println(preview)
	}
	return nil
}

func runSourceList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	// 加载 manifest
	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return fmt.Errorf("load manifest: %w", err)
	}

	// 检查 .pre-extracted/ 目录
	preDir := filepath.Join(dir, ".pre-extracted", "files")
	hasPreExtracted := false
	if info, err := os.Stat(preDir); err == nil && info.IsDir() {
		hasPreExtracted = true
	}

	type sourceRow struct {
		Path      string `json:"path"`
		Extracted string `json:"extracted"` // yes/no/n-a
		Compiled  string `json:"compiled"`  // yes/pending/error
		Type      string `json:"type"`
	}

	var rows []sourceRow
	for path, src := range mf.Sources {
		row := sourceRow{
			Path:     path,
			Compiled: src.Status,
			Type:     src.Type,
		}

		if hasPreExtracted {
			// 检查是否有预提取文件
			relPath := path
			if strings.HasPrefix(relPath, "raw/") {
				relPath = relPath[4:]
			}
			mdPath := filepath.Join(preDir, relPath+".md")
			if _, err := os.Stat(mdPath); err == nil {
				row.Extracted = "yes"
			} else {
				row.Extracted = "no"
			}
		} else {
			row.Extracted = "n/a"
		}

		rows = append(rows, row)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, rows, ""))
		return nil
	}

	if len(rows) == 0 {
		fmt.Println("No sources found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tEXTRACTED\tCOMPILED\tTYPE")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Path, r.Extracted, r.Compiled, r.Type)
	}
	w.Flush()
	return nil
}
