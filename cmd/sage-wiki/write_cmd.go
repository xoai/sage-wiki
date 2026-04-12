package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

var writeCmd = &cobra.Command{
	Use:   "write",
	Short: "Write a summary or article",
}

var writeSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Write a summary for a source file",
	RunE:  runWriteSummary,
}

var writeArticleCmd = &cobra.Command{
	Use:   "article",
	Short: "Write an article for a concept",
	RunE:  runWriteArticle,
}

func init() {
	writeSummaryCmd.Flags().String("source", "", "Source file path relative to project root")
	writeSummaryCmd.Flags().String("content", "", "Summary markdown content")
	writeSummaryCmd.Flags().String("concepts", "", "Comma-separated concept names")
	writeSummaryCmd.MarkFlagRequired("source")
	writeSummaryCmd.MarkFlagRequired("content")

	writeArticleCmd.Flags().String("concept", "", "Concept ID (lowercase-hyphenated)")
	writeArticleCmd.Flags().String("content", "", "Article markdown content")
	writeArticleCmd.MarkFlagRequired("concept")
	writeArticleCmd.MarkFlagRequired("content")

	writeCmd.AddCommand(writeSummaryCmd, writeArticleCmd)
}

func runWriteSummary(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	source, _ := cmd.Flags().GetString("source")
	content, _ := cmd.Flags().GetString("content")
	conceptsStr, _ := cmd.Flags().GetString("concepts")

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	baseName := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	summaryPath := filepath.Join(cfg.Output, "summaries", baseName+".md")
	absPath := filepath.Join(dir, summaryPath)
	os.MkdirAll(filepath.Dir(absPath), 0755)

	fm := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\n---\n\n", source, cfg.Compiler.UserNow())
	if err := os.WriteFile(absPath, []byte(fm+content), 0644); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	// Index in FTS5 + embed
	db, dbErr := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if dbErr == nil {
		defer db.Close()
		memory.NewStore(db).Add(memory.Entry{ID: source, Content: content, ArticlePath: summaryPath})
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			if v, err := embedder.Embed(content); err == nil {
				vectors.NewStore(db).Upsert(source, v)
			}
		}
	}

	// Update manifest
	var concepts []string
	if conceptsStr != "" {
		for _, c := range strings.Split(conceptsStr, ",") {
			if c = strings.TrimSpace(c); c != "" {
				concepts = append(concepts, c)
			}
		}
	}
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if _, exists := mf.Sources[source]; !exists {
		mf.AddSource(source, "", "article", int64(len(content)))
	}
	mf.MarkCompiled(source, summaryPath, concepts)
	mf.Save(filepath.Join(dir, ".manifest.json"))

	msg := fmt.Sprintf("Summary written: %s (%d chars)", summaryPath, len(content))
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": summaryPath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}

func runWriteArticle(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	conceptID, _ := cmd.Flags().GetString("concept")
	content, _ := cmd.Flags().GetString("content")

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	articlePath := filepath.Join(cfg.Output, "concepts", conceptID+".md")
	absPath := filepath.Join(dir, articlePath)
	os.MkdirAll(filepath.Dir(absPath), 0755)

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	// Update ontology + FTS5 + embed
	db, dbErr := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if dbErr == nil {
		defer db.Close()
		merged := ontology.MergedRelations(cfg.Ontology.Relations)
		mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		ont := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))
		ont.AddEntity(ontology.Entity{ID: conceptID, Type: "concept", Name: conceptID, ArticlePath: articlePath})
		memory.NewStore(db).Add(memory.Entry{ID: "concept:" + conceptID, Content: content, ArticlePath: articlePath})
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			if v, err := embedder.Embed(content); err == nil {
				vectors.NewStore(db).Upsert("concept:"+conceptID, v)
			}
		}
	}

	// Update manifest
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	mf.AddConcept(conceptID, articlePath, nil)
	mf.Save(filepath.Join(dir, ".manifest.json"))

	msg := fmt.Sprintf("Article written: %s", articlePath)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": articlePath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
