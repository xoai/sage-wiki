package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/pack"
)

var packCmd = &cobra.Command{
	Use:   "pack",
	Short: "Manage contribution packs",
	Long:  "Install, apply, and manage contribution packs that bundle ontology, prompts, skills, and config for specific use cases.",
}

var packInstallCmd = &cobra.Command{
	Use:   "install <name|url|path>",
	Short: "Install a pack from a local path or Git URL",
	Args:  cobra.ExactArgs(1),
	RunE:  runPackInstall,
}

var packApplyCmd = &cobra.Command{
	Use:   "apply <name>",
	Short: "Apply an installed pack to the current project",
	Args:  cobra.ExactArgs(1),
	RunE:  runPackApply,
}

var packRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a pack from the current project",
	Args:  cobra.ExactArgs(1),
	RunE:  runPackRemove,
}

var packInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show details about an installed pack",
	Args:  cobra.ExactArgs(1),
	RunE:  runPackInfo,
}

var packListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed packs",
	RunE:  runPackList,
}

var packSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the pack registry",
	Args:  cobra.ExactArgs(1),
	RunE:  runPackSearch,
}

var packUpdateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Update installed packs to latest versions",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runPackUpdate,
}

var packCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Scaffold a new pack directory",
	Args:  cobra.ExactArgs(1),
	RunE:  runPackCreate,
}

var packValidateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate a pack's schema and files",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runPackValidate,
}

var packConflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Short: "Show conflicts between installed packs",
	RunE:  runPackConflicts,
}

func init() {
	packApplyCmd.Flags().String("mode", "merge", "Apply mode: merge (fill-only) or replace")
	packApplyCmd.Flags().Bool("enable-parsers", false, "Install parser files from this pack (opt-in for executable content)")
	packCreateCmd.Flags().Bool("from-project", false, "Pre-fill pack from current project config")

	packCmd.AddCommand(packInstallCmd, packApplyCmd, packRemoveCmd, packInfoCmd, packListCmd,
		packSearchCmd, packUpdateCmd, packCreateCmd, packValidateCmd, packConflictsCmd)
}

func runPackInstall(cmd *cobra.Command, args []string) error {
	nameOrURL := args[0]
	cacheDir := pack.CacheDir()

	// try local path or Git URL first
	installSource := "local"
	manifest, cachePath, err := pack.Install(nameOrURL, cacheDir)
	if err != nil {
		// only fall back to registry if the argument is NOT an existing local path
		// (avoids silently installing a different registry pack when a local one is invalid)
		_, localExists := os.Stat(nameOrURL)
		if localExists != nil && pack.ValidateName(nameOrURL) == nil && !isPathOrURL(nameOrURL) {
			reg := pack.NewRegistry(cacheDir)
			manifest, cachePath, err = reg.InstallFromRegistry(nameOrURL)
			if err != nil {
				return fmt.Errorf("pack %q not found locally or in registry: %w", nameOrURL, err)
			}
			installSource = "registry"
		} else {
			return err
		}
	}

	// record source in cache for apply to use
	os.WriteFile(filepath.Join(cachePath, ".source"), []byte(installSource), 0o644)

	fmt.Printf("Installed %s@%s to %s\n", manifest.Name, manifest.Version, cachePath)
	fmt.Printf("Run 'sage-wiki pack apply %s' to apply it to your project.\n", manifest.Name)
	return nil
}

func isPathOrURL(s string) bool {
	return strings.Contains(s, "/") || strings.Contains(s, "\\") ||
		strings.HasPrefix(s, ".") || strings.Contains(s, "://") ||
		strings.Contains(s, "@")
}

func validatePackNameArg(name string) error {
	if err := pack.ValidateName(name); err != nil {
		return fmt.Errorf("invalid pack name %q: %w", name, err)
	}
	return nil
}

func runPackApply(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validatePackNameArg(name); err != nil {
		return err
	}
	dir, _ := filepath.Abs(projectDir)
	cacheDir := pack.CacheDir()

	if !pack.IsInstalled(name, cacheDir) {
		return fmt.Errorf("pack %q is not installed; run 'sage-wiki pack install' first", name)
	}

	packDir := pack.InstalledPath(name, cacheDir)
	manifest, err := pack.LoadManifest(packDir)
	if err != nil {
		return fmt.Errorf("loading pack manifest: %w", err)
	}

	modeStr, _ := cmd.Flags().GetString("mode")
	mode := pack.ApplyMode(modeStr)
	switch mode {
	case pack.ModeReplace, pack.ModeMerge:
	default:
		return fmt.Errorf("invalid mode %q: use merge or replace", modeStr)
	}

	state, err := pack.LoadState(dir)
	if err != nil {
		return fmt.Errorf("loading pack state: %w", err)
	}

	// read source from cache marker; fall back to existing state source
	applySource := "local"
	if data, err := os.ReadFile(filepath.Join(packDir, ".source")); err == nil {
		applySource = strings.TrimSpace(string(data))
	} else if existing, _ := state.FindInstalled(name); existing != nil && existing.Source != "" {
		applySource = existing.Source
	}

	enableParsers, _ := cmd.Flags().GetBool("enable-parsers")

	result, err := pack.Apply(dir, packDir, manifest, mode, state, pack.ApplyOpts{
		Source:        applySource,
		EnableParsers: enableParsers,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Applied %s@%s (%s mode)\n", manifest.Name, manifest.Version, mode)
	if len(result.ConfigChanges) > 0 {
		fmt.Printf("  Config: %s\n", strings.Join(result.ConfigChanges, ", "))
	}
	if len(result.OntologyAdded) > 0 {
		fmt.Printf("  Ontology: %s\n", strings.Join(result.OntologyAdded, ", "))
	}
	if len(result.PromptsAdded) > 0 {
		fmt.Printf("  Prompts: %s\n", strings.Join(result.PromptsAdded, ", "))
	}
	allConflicts := append(result.PromptsConflict, result.SkillsConflict...)
	allConflicts = append(allConflicts, result.SamplesConflict...)
	allConflicts = append(allConflicts, result.ParsersConflict...)
	if len(allConflicts) > 0 {
		fmt.Printf("  Conflicts (skipped): %s\n", strings.Join(allConflicts, ", "))
	}
	if len(result.SkillsAdded) > 0 {
		fmt.Printf("  Skills: %s\n", strings.Join(result.SkillsAdded, ", "))
	}
	if len(result.SamplesAdded) > 0 {
		fmt.Printf("  Samples: %s\n", strings.Join(result.SamplesAdded, ", "))
	}
	return nil
}

func runPackRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validatePackNameArg(name); err != nil {
		return err
	}
	dir, _ := filepath.Abs(projectDir)

	state, err := pack.LoadState(dir)
	if err != nil {
		return fmt.Errorf("loading pack state: %w", err)
	}

	installed, _ := state.FindInstalled(name)
	if installed == nil {
		return fmt.Errorf("pack %q is not applied to this project", name)
	}

	// validate and resolve state-derived paths to prevent escape
	// resolve symlinks in the project dir itself for accurate containment
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	absDir, _ := filepath.Abs(resolvedDir)
	isPathSafe := func(relPath string) bool {
		if err := pack.ValidateRelPath(relPath); err != nil {
			return false
		}
		fullPath := filepath.Join(dir, relPath)
		// resolve symlinks in the full path to catch symlink components
		resolved, err := filepath.EvalSymlinks(filepath.Dir(fullPath))
		if err != nil {
			// parent doesn't exist yet — check lexically
			full, _ := filepath.Abs(fullPath)
			return strings.HasPrefix(full, absDir+string(filepath.Separator)) || full == absDir
		}
		resolvedFull := filepath.Join(resolved, filepath.Base(fullPath))
		return strings.HasPrefix(resolvedFull, absDir+string(filepath.Separator)) || resolvedFull == absDir
	}

	// restore pre-apply snapshots, but ONLY for paths the pack actually wrote
	var restored, removed, kept []string
	if installed.Snapshots != nil {
		for path, preApplyHash := range installed.Snapshots {
			if !isPathSafe(path) {
				kept = append(kept, path+" (unsafe path, skipped)")
				continue
			}
			fullPath := filepath.Join(dir, path)

			// skip paths the pack never wrote (merge-mode conflicts, skipped parsers)
			_, packWrote := installed.Files[path]
			if !packWrote {
				continue
			}

			if preApplyHash != nil {
				// file existed before pack — restore only if user hasn't
				// edited the file since the pack was applied
				postApplyHash := installed.Files[path]
				if postApplyHash == "" {
					// no post-apply hash recorded — can't verify, keep file
					kept = append(kept, path+" (no post-apply hash, kept)")
					continue
				}
				currentHash, _ := pack.ComputeFileHash(fullPath)
				if currentHash != postApplyHash {
					kept = append(kept, path+" (modified after apply)")
					continue
				}
				snapshotPath := filepath.Join(dir, ".sage", "pack-snapshots", name, path)
				data, err := os.ReadFile(snapshotPath)
				if err != nil {
					kept = append(kept, path+" (snapshot missing, kept)")
					continue
				}
				if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
					kept = append(kept, path+" (restore failed: "+err.Error()+")")
					continue
				}
				if err := os.WriteFile(fullPath, data, 0o644); err != nil {
					kept = append(kept, path+" (restore failed: "+err.Error()+")")
					continue
				}
				restored = append(restored, path)
			} else {
				// file was created by pack — delete if unmodified
				if _, statErr := os.Stat(fullPath); os.IsNotExist(statErr) {
					removed = append(removed, path)
					continue
				}
				if !state.IsModified(dir, name, path) {
					if err := removeFileAndEmptyParents(fullPath, dir); err == nil {
						removed = append(removed, path)
						continue
					}
				}
				kept = append(kept, path+" (modified after apply)")
			}
		}
	}

	// remove pack-added files not covered by snapshots
	for path := range installed.Files {
		if !isPathSafe(path) {
			kept = append(kept, path+" (unsafe path, skipped)")
			continue
		}
		if containsPath(restored, path) || containsPath(removed, path) || containsPathPrefix(kept, path) {
			continue
		}
		if state.IsModified(dir, name, path) {
			kept = append(kept, path)
			continue
		}
		fullPath := filepath.Join(dir, path)
		if err := removeFileAndEmptyParents(fullPath, dir); err != nil {
			kept = append(kept, path+" (error: "+err.Error()+")")
			continue
		}
		removed = append(removed, path)
	}

	// only clean snapshots and remove state if all operations succeeded
	hasErrors := len(kept) > 0
	if !hasErrors {
		state.CleanSnapshot(dir, name)
		state.RemoveInstalled(name)
	} else {
		// partial remove — keep state and snapshots for recovery
		// but update Files and Snapshots to reflect what was actually removed
		if p, _ := state.FindInstalled(name); p != nil {
			for _, path := range removed {
				delete(p.Files, path)
				delete(p.Snapshots, path)
			}
			for _, path := range restored {
				delete(p.Files, path)
				delete(p.Snapshots, path)
			}
		}
	}
	if err := state.Save(dir); err != nil {
		return fmt.Errorf("saving pack state: %w", err)
	}

	if hasErrors {
		fmt.Printf("Partially removed %s (some files could not be restored)\n", name)
	} else {
		fmt.Printf("Removed %s\n", name)
	}
	if len(restored) > 0 {
		fmt.Printf("  Restored: %s\n", strings.Join(restored, ", "))
	}
	if len(removed) > 0 {
		fmt.Printf("  Deleted: %s\n", strings.Join(removed, ", "))
	}
	if len(kept) > 0 {
		fmt.Printf("  Kept (modified): %s\n", strings.Join(kept, ", "))
	}
	return nil
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

func containsPathPrefix(paths []string, target string) bool {
	for _, p := range paths {
		if p == target || strings.HasPrefix(p, target+" ") {
			return true
		}
	}
	return false
}

func runPackInfo(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validatePackNameArg(name); err != nil {
		return err
	}
	cacheDir := pack.CacheDir()

	var manifest *pack.PackManifest
	if pack.IsInstalled(name, cacheDir) {
		packDir := pack.InstalledPath(name, cacheDir)
		var err error
		manifest, err = pack.LoadManifest(packDir)
		if err != nil {
			return err
		}
	} else if pack.IsBundled(name) {
		var err error
		manifest, _, err = pack.GetBundled(name)
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("pack %q is not installed or bundled", name)
	}

	fmt.Printf("Name:        %s\n", manifest.Name)
	fmt.Printf("Version:     %s\n", manifest.Version)
	fmt.Printf("Description: %s\n", manifest.Description)
	fmt.Printf("Author:      %s\n", manifest.Author)
	if manifest.License != "" {
		fmt.Printf("License:     %s\n", manifest.License)
	}
	if len(manifest.Tags) > 0 {
		fmt.Printf("Tags:        %s\n", strings.Join(manifest.Tags, ", "))
	}
	if manifest.MinVersion != "" {
		fmt.Printf("Min Version: %s\n", manifest.MinVersion)
	}
	if len(manifest.Ontology.RelationTypes) > 0 {
		var names []string
		for _, r := range manifest.Ontology.RelationTypes {
			names = append(names, r.Name)
		}
		fmt.Printf("Relations:   %s\n", strings.Join(names, ", "))
	}
	if len(manifest.Ontology.EntityTypes) > 0 {
		var names []string
		for _, e := range manifest.Ontology.EntityTypes {
			names = append(names, e.Name)
		}
		fmt.Printf("Entities:    %s\n", strings.Join(names, ", "))
	}
	if len(manifest.Prompts) > 0 {
		fmt.Printf("Prompts:     %s\n", strings.Join(manifest.Prompts, ", "))
	}
	if len(manifest.Skills) > 0 {
		fmt.Printf("Skills:      %s\n", strings.Join(manifest.Skills, ", "))
	}
	if len(manifest.Samples) > 0 {
		fmt.Printf("Samples:     %s\n", strings.Join(manifest.Samples, ", "))
	}
	return nil
}

func runPackList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	state, err := pack.LoadState(dir)
	if err != nil {
		state = &pack.PackState{}
	}

	appliedNames := make(map[string]bool)
	if len(state.Installed) > 0 {
		fmt.Println("Applied packs:")
		fmt.Printf("  %-20s %-10s %-10s %s\n", "NAME", "VERSION", "SOURCE", "APPLIED")
		for _, p := range state.Installed {
			appliedNames[p.Name] = true
			applied := "never"
			if !p.AppliedAt.IsZero() {
				applied = p.AppliedAt.Format("2006-01-02")
			}
			fmt.Printf("  %-20s %-10s %-10s %s\n", p.Name, p.Version, p.Source, applied)
		}
		fmt.Println()
	}

	// show cached (installed but not yet applied) packs
	cacheDir := pack.CacheDir()
	if entries, err := os.ReadDir(cacheDir); err == nil {
		var cached []string
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if appliedNames[e.Name()] {
				continue
			}
			if pack.IsInstalled(e.Name(), cacheDir) {
				cached = append(cached, e.Name())
			}
		}
		if len(cached) > 0 {
			fmt.Println("Installed (not yet applied):")
			for _, name := range cached {
				manifest, err := pack.LoadManifest(pack.InstalledPath(name, cacheDir))
				if err != nil {
					fmt.Printf("  %-20s (invalid manifest)\n", name)
					continue
				}
				fmt.Printf("  %-20s %-10s  Run 'sage-wiki pack apply %s' to apply\n", manifest.Name, manifest.Version, manifest.Name)
			}
			fmt.Println()
		}
	}

	bundled := pack.ListBundled()
	if len(bundled) > 0 {
		fmt.Println("Bundled packs (available offline):")
		fmt.Printf("  %-20s %-10s %s\n", "NAME", "VERSION", "DESCRIPTION")
		for _, p := range bundled {
			status := ""
			if appliedNames[p.Name] {
				status = " (applied)"
			}
			desc := p.Description
			if len(desc) > 45 {
				desc = desc[:42] + "..."
			}
			fmt.Printf("  %-20s %-10s %s%s\n", p.Name, p.Version, desc, status)
		}
	}

	if len(state.Installed) == 0 {
		fmt.Println("\nUse 'sage-wiki pack install <name>' to install a pack.")
	}
	return nil
}

// removeFileAndEmptyParents removes a file and any empty parent directories
// up to (but not including) the project root.
func removeFileAndEmptyParents(path, root string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	for dir != root && dir != "." && dir != "/" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		os.Remove(dir)
		dir = filepath.Dir(dir)
	}
	return nil
}

func runPackSearch(cmd *cobra.Command, args []string) error {
	query := args[0]
	reg := pack.NewRegistry("")

	results, err := reg.Search(query)
	if err != nil {
		return err
	}

	if len(results) == 0 {
		fmt.Printf("No packs found for %q.\n", query)
		return nil
	}

	fmt.Printf("%-25s %-10s %-10s %s\n", "NAME", "VERSION", "TIER", "DESCRIPTION")
	for _, p := range results {
		tier := p.Tier
		if tier == "" {
			tier = "community"
		}
		desc := p.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		fmt.Printf("%-25s %-10s %-10s %s\n", p.Name, p.Version, tier, desc)
	}
	return nil
}

func runPackUpdate(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	cacheDir := pack.CacheDir()

	state, err := pack.LoadState(dir)
	if err != nil {
		return fmt.Errorf("loading pack state: %w", err)
	}

	reg := pack.NewRegistry(cacheDir)
	packs, err := reg.FetchIndex()
	if err != nil {
		return err
	}

	if len(args) == 1 {
		// update single pack
		name := args[0]
		result, err := pack.UpdatePack(name, dir, state, reg)
		if err != nil {
			return err
		}
		if err := state.Save(dir); err != nil {
			return err
		}
		fmt.Printf("Updated: %s\n", strings.Join(result.Updated, ", "))
		if len(result.Conflicts) > 0 {
			fmt.Printf("Conflicts (skipped): %s\n", strings.Join(result.Conflicts, ", "))
		}
		return nil
	}

	// update all packs with available updates
	updates := pack.CheckUpdates(state, packs)
	if len(updates) == 0 {
		fmt.Println("All packs are up to date.")
		return nil
	}

	for _, u := range updates {
		fmt.Printf("Updating %s: %s → %s\n", u.Name, u.CurrentVersion, u.LatestVersion)
		result, err := pack.UpdatePack(u.Name, dir, state, reg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error updating %s: %v\n", u.Name, err)
			continue
		}
		if err := state.Save(dir); err != nil {
			return fmt.Errorf("saving state after updating %s: %w", u.Name, err)
		}
		if len(result.Updated) > 0 {
			fmt.Printf("  Updated: %s\n", strings.Join(result.Updated, ", "))
		}
		if len(result.Skipped) > 0 {
			fmt.Printf("  Skipped: %s\n", strings.Join(result.Skipped, ", "))
		}
		if len(result.Conflicts) > 0 {
			fmt.Printf("  Conflicts: %s\n", strings.Join(result.Conflicts, ", "))
		}
	}
	return nil
}

func runPackCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	dir, _ := filepath.Abs(projectDir)
	fromProject, _ := cmd.Flags().GetBool("from-project")

	var err error
	if fromProject {
		err = pack.CreateScaffoldFromProject(name, ".", dir)
	} else {
		err = pack.CreateScaffold(name, ".")
	}
	if err != nil {
		return err
	}

	fmt.Printf("Created pack scaffold at %s/\n", name)
	fmt.Println("Edit pack.yaml and add your prompts, skills, and samples.")
	fmt.Println("Run 'sage-wiki pack validate' to check your pack.")
	return nil
}

func runPackValidate(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}

	absDir, _ := filepath.Abs(dir)
	errors, err := pack.Validate(absDir)
	if err != nil {
		return err
	}

	if len(errors) == 0 {
		fmt.Println("Pack is valid.")
		return nil
	}

	errCount, warnCount := 0, 0
	for _, e := range errors {
		if e.Level == "error" {
			errCount++
		} else {
			warnCount++
		}
		fmt.Println(e.String())
	}

	fmt.Printf("\n%d error(s), %d warning(s)\n", errCount, warnCount)
	if errCount > 0 {
		return fmt.Errorf("pack validation failed with %d error(s)", errCount)
	}
	return nil
}

func runPackConflicts(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	state, err := pack.LoadState(dir)
	if err != nil {
		return fmt.Errorf("loading pack state: %w", err)
	}

	if len(state.Installed) < 2 {
		fmt.Println("No multi-pack conflicts (fewer than 2 packs installed).")
		return nil
	}

	// check for file overlaps between packs
	fileOwners := make(map[string][]string)
	for _, p := range state.Installed {
		for path := range p.Files {
			fileOwners[path] = append(fileOwners[path], p.Name)
		}
	}

	conflicts := 0
	for path, owners := range fileOwners {
		if len(owners) > 1 {
			fmt.Printf("  %s: owned by %s\n", path, strings.Join(owners, ", "))
			conflicts++
		}
	}

	if conflicts == 0 {
		fmt.Println("No file conflicts between installed packs.")
	} else {
		fmt.Printf("\n%d file(s) with overlapping ownership.\n", conflicts)
	}
	return nil
}
