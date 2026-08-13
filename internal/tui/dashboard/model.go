package dashboard

import (
	"context"
	"path/filepath"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/tui"
	"github.com/xoai/sage-wiki/internal/tui/browse"
	"github.com/xoai/sage-wiki/internal/tui/compile"
	"github.com/xoai/sage-wiki/internal/tui/components"
	queryTab "github.com/xoai/sage-wiki/internal/tui/query"
	searchTab "github.com/xoai/sage-wiki/internal/tui/search"
	"github.com/xoai/sage-wiki/internal/vectors"
)

type tab int

const (
	tabBrowse tab = iota
	tabSearch
	tabQuery
	tabCompile
)

var tabNames = []string{"Browse", "Search", "Q&A", "Compile"}

// Model is the unified TUI dashboard.
type Model struct {
	activeTab tab
	browse    browse.Model
	search    searchTab.Model
	query     queryTab.Model
	compile   compile.Model
	statusBar components.StatusBar
	width     int
	height    int

	compileStarted bool // prevent repeated auto-compiles
}

// New creates the dashboard with all tabs.
func New(projectDir string, cfg *config.Config, db store.DBHandle) Model {
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db, vectors.WithANN(cfg.Search.ANNEnabled()), vectors.WithVectorBackend(cfg.VectorBackend()), vectors.WithIndexDir(filepath.Join(projectDir, ".sage")))
	sourcePaths := cfg.ResolveSources(projectDir)

	// Pipeline-aware search seam (M5): unified by default, legacy behind
	// the config switch. The TUI was the fifth entry point the spec's
	// fork table missed (Gate-3 F-041) — it previously ran BM25-only
	// with default weights.
	var searchFn searchTab.SearchFn
	if cfg.Search.PipelineOrDefault() == "unified" {
		mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
		mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		trustMode := cfg.Trust.IncludeOutputsMode()
		var trustStore *trust.Store
		if trustMode == "verified" {
			trustStore = trust.NewStore(db)
		}
		deps := search.Deps{
			Mem:          memStore,
			Chunks:       memory.NewChunkStore(db),
			Vec:          vecStore,
			Embedder:     embed.NewFromConfig(cfg), // without it search.Run runs no vector leg at all
			BM25Weight:   cfg.Search.HybridWeightBM25,
			VectorWeight: cfg.Search.HybridWeightVector,
			Ont: ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes),
				ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault())),
			GraphWeight:          cfg.Search.HybridWeightGraph,
			GraphRelationWeights: cfg.Search.GraphRelationWeights,
			IncludeDoc:           trust.IncludePredicate(trustMode, trustStore),
		}
		searchFn = func(query string, limit int) ([]search.DocResult, error) {
			// The TUI has no per-keystroke context; the stage timeouts inside
			// Run are the bound here.
			resp, err := search.Run(context.Background(), deps, search.Request{Query: query, Limit: limit, Granularity: search.Docs})
			if err != nil {
				return nil, err
			}
			return search.DocResults(resp.Results), nil
		}
	} else {
		searcher := hybrid.NewSearcher(memStore, vecStore)
		// Trust filtering applies here too: the pipeline pin rolls back
		// ranking, not the rule about which documents may appear.
		trustMode := cfg.Trust.IncludeOutputsMode()
		var trustStore *trust.Store
		if trustMode == "verified" {
			trustStore = trust.NewStore(db)
		}
		includeDoc := trust.IncludePredicate(trustMode, trustStore)
		searchFn = func(query string, limit int) ([]search.DocResult, error) {
			legacy, err := searcher.Search(hybrid.SearchOpts{
				Query:        query,
				Limit:        limit,
				BM25Weight:   cfg.Search.HybridWeightBM25,
				VectorWeight: cfg.Search.HybridWeightVector,
			}, nil)
			if err != nil {
				return nil, err
			}
			out := make([]search.DocResult, 0, len(legacy))
			for _, r := range legacy {
				if !includeDoc(r.ID) {
					continue
				}
				out = append(out, search.DocResult{ID: r.ID, Content: r.Content, Tags: r.Tags,
					ArticlePath: r.ArticlePath, BM25Rank: r.BM25Rank, VectorRank: r.VectorRank, RRFScore: r.RRFScore})
			}
			return out, nil
		}
	}

	sb := components.NewStatusBar(80)
	sb.SetHints([]components.KeyHint{
		{Key: "F1-F4", Help: "switch tab"},
		{Key: "esc", Help: "browse"},
		{Key: "ctrl+c", Help: "quit"},
	})

	return Model{
		activeTab: tabBrowse,
		browse:    browse.New(projectDir, cfg.Output),
		search:    searchTab.New(projectDir, cfg.Output, searchFn, ""),
		query:     queryTab.New(projectDir, db),
		compile:   compile.New(projectDir, cfg.Output, sourcePaths, 2),
		statusBar: sb,
	}
}

func (m Model) Init() tea.Cmd {
	// Init search and query for cursor blink. Don't init compile
	// (it auto-compiles — only start when user switches to tab 4).
	return tea.Batch(m.search.Init(), m.query.Init())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.statusBar.SetWidth(m.width)

		// Resize for tab content area (minus tab bar and status bar)
		contentMsg := tea.WindowSizeMsg{
			Width:  m.width,
			Height: m.contentHeight(),
		}

		// Forward to all tabs so they're ready when switched to
		var cmd tea.Cmd
		m.browse, cmd = m.browse.Update(contentMsg)
		cmds = append(cmds, cmd)

		var sModel tea.Model
		sModel, cmd = m.search.Update(contentMsg)
		m.search = sModel.(searchTab.Model)
		cmds = append(cmds, cmd)

		var qModel tea.Model
		qModel, cmd = m.query.Update(contentMsg)
		m.query = qModel.(queryTab.Model)
		cmds = append(cmds, cmd)

		var cModel tea.Model
		cModel, cmd = m.compile.Update(contentMsg)
		m.compile = cModel.(compile.Model)
		cmds = append(cmds, cmd)

		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			// Let query tab clean up streaming context before quitting
			if m.activeTab == tabQuery {
				var model tea.Model
				model, _ = m.query.Update(msg)
				m.query = model.(queryTab.Model)
			}
			return m, tea.Quit
		}

		// F1-F4 switches tabs from ANY tab (no text input conflict)
		// Esc always goes to Browse
		switch msg.String() {
		case "f1":
			m.activeTab = tabBrowse
			m.browse.Refresh()
			return m, nil
		case "f2":
			m.activeTab = tabSearch
			return m, m.search.Init()
		case "f3":
			m.activeTab = tabQuery
			return m, m.query.Init()
		case "f4":
			m.activeTab = tabCompile
			if !m.compileStarted {
				m.compileStarted = true
				return m, m.compile.Init()
			}
			return m, nil
		case "esc":
			if m.activeTab != tabBrowse {
				m.activeTab = tabBrowse
				m.browse.Refresh()
				return m, nil
			}
		}

		// Plain number keys only on Browse/Compile (no text input)
		if m.activeTab == tabBrowse || m.activeTab == tabCompile {
			switch msg.String() {
			case "1":
				m.activeTab = tabBrowse
				m.browse.Refresh()
				return m, nil
			case "2":
				m.activeTab = tabSearch
				return m, m.search.Init()
			case "3":
				m.activeTab = tabQuery
				return m, m.query.Init()
			case "4":
				m.activeTab = tabCompile
				if !m.compileStarted {
					m.compileStarted = true
					return m, m.compile.Init()
				}
				return m, nil
			}
		}
	}

	// Always forward compile-related messages (compile runs in background)
	switch msg.(type) {
	case compile.CompileCompleteMsg, compile.ScanTickMsg, spinner.TickMsg:
		if m.compileStarted {
			var model tea.Model
			var cmd tea.Cmd
			model, cmd = m.compile.Update(msg)
			m.compile = model.(compile.Model)
			cmds = append(cmds, cmd)
		}
		// If this tab is also the active tab, don't double-forward
		if m.activeTab == tabCompile {
			return m, tea.Batch(cmds...)
		}
	}

	// Forward to active tab
	var cmd tea.Cmd
	switch m.activeTab {
	case tabBrowse:
		m.browse, cmd = m.browse.Update(msg)
	case tabSearch:
		var model tea.Model
		model, cmd = m.search.Update(msg)
		m.search = model.(searchTab.Model)
	case tabQuery:
		var model tea.Model
		model, cmd = m.query.Update(msg)
		m.query = model.(queryTab.Model)
	case tabCompile:
		var model tea.Model
		model, cmd = m.compile.Update(msg)
		m.compile = model.(compile.Model)
	}
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Tab bar
	tabBar := m.renderTabBar()

	// Active tab content
	var content string
	switch m.activeTab {
	case tabBrowse:
		content = m.browse.View()
	case tabSearch:
		content = m.search.View()
	case tabQuery:
		content = m.query.View()
	case tabCompile:
		content = m.compile.View()
	}

	status := m.statusBar.View()

	return lipgloss.JoinVertical(lipgloss.Left, tabBar, content, status)
}

func (m Model) contentHeight() int {
	return m.height - 3 // tab bar + status bar
}

func (m Model) renderTabBar() string {
	var tabs []string
	for i, name := range tabNames {
		style := lipgloss.NewStyle().Padding(0, 2)
		if tab(i) == m.activeTab {
			style = style.Bold(true).
				Foreground(tui.Accent).
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(tui.Accent)
		} else {
			style = style.Foreground(tui.DimColor)
		}
		fKey := string(rune('1' + i))
		label := style.Render("[F" + fKey + "] " + name)
		tabs = append(tabs, label)
	}

	bar := lipgloss.JoinHorizontal(lipgloss.Bottom, tabs...)
	return lipgloss.NewStyle().Width(m.width).Render(bar)
}

// Run launches the unified TUI dashboard.
func Run(projectDir string) error {
	a, err := app.Open(projectDir)
	if err != nil {
		return err // text: "load config: ..." / "open db: ..." (prefix-free by design, P1-8)
	}
	defer a.Close()

	m := New(projectDir, a.Config, a.DB)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInputTTY())

	// Set program reference for query streaming
	queryTab.SetActiveProgram(p)
	defer queryTab.SetActiveProgram(nil)

	_, err = p.Run()
	return err
}
