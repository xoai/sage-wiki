package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/hub"
	"github.com/xoai/sage-wiki/internal/wiki"
)

var hubCmd = &cobra.Command{
	Use:   "hub",
	Short: "Multi-project hub commands",
}

var hubInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize hub config at ~/.sage-hub.yaml",
	RunE:  runHubInit,
}

var hubAddCmd = &cobra.Command{
	Use:   "add [path]",
	Short: "Register a project in the hub",
	Args:  cobra.ExactArgs(1),
	RunE:  runHubAdd,
}

var hubRemoveCmd = &cobra.Command{
	Use:   "remove [name]",
	Short: "Remove a project from the hub",
	Args:  cobra.ExactArgs(1),
	RunE:  runHubRemove,
}

var hubListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered projects",
	RunE:  runHubList,
}

var hubSearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Federated search across all searchable projects",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runHubSearch,
}

var hubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all registered projects",
	RunE:  runHubStatus,
}

var hubCompileCmd = &cobra.Command{
	Use:   "compile [name|--all]",
	Short: "Compile a project or all projects",
	RunE:  runHubCompile,
}

func init() {
	hubSearchCmd.Flags().StringP("project", "p", "", "Search only this project")
	hubSearchCmd.Flags().Int("limit", 10, "Maximum results")

	hubCompileCmd.Flags().Bool("all", false, "Compile all projects")

	hubCmd.AddCommand(hubInitCmd, hubAddCmd, hubRemoveCmd, hubListCmd, hubSearchCmd, hubStatusCmd, hubCompileCmd)
}

func loadHub() (*hub.HubConfig, error) {
	return hub.Load(hub.DefaultPath())
}

func runHubInit(cmd *cobra.Command, args []string) error {
	path := hub.DefaultPath()
	cfg := hub.New()
	if err := cfg.Save(path); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	msg := fmt.Sprintf("Hub initialized: %s", path)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": path}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}

func runHubAdd(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(args[0])

	// Check project has config.yaml
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		errMsg := fmt.Sprintf("not a sage-wiki project (no config.yaml): %s", dir)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, errMsg))
			return nil
		}
		return fmt.Errorf("%s", errMsg)
	}

	hubCfg, err := loadHub()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	name := cfg.Project
	overwritten := hubCfg.AddProject(name, hub.Project{
		Path:        dir,
		Description: cfg.Description,
		Searchable:  true,
	})
	if overwritten {
		fmt.Fprintf(cmd.ErrOrStderr(), "info: project %q already existed, overwriting\n", name)
	}
	if err := hubCfg.Save(hub.DefaultPath()); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	msg := fmt.Sprintf("Added project %q (%s)", name, dir)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"name": name, "path": dir}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}

func runHubRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	hubCfg, err := loadHub()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if _, exists := hubCfg.Projects[name]; !exists {
		errMsg := fmt.Sprintf("project %q not found", name)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, errMsg))
			return nil
		}
		return fmt.Errorf("%s", errMsg)
	}

	hubCfg.RemoveProject(name)
	if err := hubCfg.Save(hub.DefaultPath()); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	msg := fmt.Sprintf("Removed project %q", name)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"name": name}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}

func runHubList(cmd *cobra.Command, args []string) error {
	hubCfg, err := loadHub()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, hubCfg, ""))
		return nil
	}

	if len(hubCfg.Projects) == 0 {
		fmt.Println("No projects registered. Use `sage-wiki hub add <path>` to add one.")
		return nil
	}
	for name, p := range hubCfg.Projects {
		search := "searchable"
		if !p.Searchable {
			search = "private"
		}
		fmt.Printf("  %s: %s (%s) [%s]\n", name, p.Path, p.Description, search)
	}
	return nil
}

func runHubSearch(cmd *cobra.Command, args []string) error {
	query := args[0]
	limit, _ := cmd.Flags().GetInt("limit")
	projectFilter, _ := cmd.Flags().GetString("project")

	hubCfg, err := loadHub()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	var projects map[string]hub.Project
	if projectFilter != "" {
		p, exists := hubCfg.Projects[projectFilter]
		if !exists {
			errMsg := fmt.Sprintf("project %q not found", projectFilter)
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, errMsg))
				return nil
			}
			return fmt.Errorf("%s", errMsg)
		}
		projects = map[string]hub.Project{projectFilter: p}
	} else {
		projects = hubCfg.SearchableProjects()
	}

	results, err := hub.FederatedSearch(projects, query, limit)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}
	for i, r := range results {
		fmt.Printf("%d. [%s] [%.4f] %s\n", i+1, r.Project, r.RRFScore, r.ArticlePath)
		content := r.Content
		if len(content) > 120 {
			content = content[:120] + "..."
		}
		fmt.Printf("   %s\n\n", content)
	}
	return nil
}

func runHubStatus(cmd *cobra.Command, args []string) error {
	hubCfg, err := loadHub()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	type projectStatus struct {
		Name   string           `json:"name"`
		Path   string           `json:"path"`
		Status *wiki.StatusInfo `json:"status,omitempty"`
		Error  string           `json:"error,omitempty"`
	}

	var statuses []projectStatus
	for name, p := range hubCfg.Projects {
		info, err := wiki.GetStatus(p.Path, nil)
		if err != nil {
			statuses = append(statuses, projectStatus{Name: name, Path: p.Path, Error: err.Error()})
			continue
		}
		statuses = append(statuses, projectStatus{Name: name, Path: p.Path, Status: info})
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, statuses, ""))
		return nil
	}

	for _, s := range statuses {
		if s.Error != "" {
			fmt.Printf("  %s: ERROR — %s\n", s.Name, s.Error)
			continue
		}
		fmt.Printf("  %s: %d sources, %d concepts, %d pending\n",
			s.Name, s.Status.SourceCount, s.Status.ConceptCount, s.Status.PendingCount)
	}
	return nil
}

func runHubCompile(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")

	hubCfg, err := loadHub()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	var targets map[string]hub.Project
	if all {
		targets = hubCfg.Projects
	} else if len(args) > 0 {
		name := args[0]
		p, exists := hubCfg.Projects[name]
		if !exists {
			errMsg := fmt.Sprintf("project %q not found", name)
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, errMsg))
				return nil
			}
			return fmt.Errorf("%s", errMsg)
		}
		targets = map[string]hub.Project{name: p}
	} else {
		errMsg := "specify project name or --all"
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, errMsg))
			return nil
		}
		return fmt.Errorf("%s", errMsg)
	}

	type compileResult struct {
		Name   string `json:"name"`
		Error  string `json:"error,omitempty"`
		Result any    `json:"result,omitempty"`
	}

	var results []compileResult
	for name, p := range targets {
		fmt.Fprintf(cmd.ErrOrStderr(), "Compiling %s...\n", name)
		result, err := compiler.Compile(p.Path, compiler.CompileOpts{})
		if err != nil {
			results = append(results, compileResult{Name: name, Error: err.Error()})
			continue
		}
		results = append(results, compileResult{Name: name, Result: result})
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}

	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("  %s: ERROR — %s\n", r.Name, r.Error)
		} else {
			fmt.Printf("  %s: OK\n", r.Name)
		}
	}
	return nil
}
