package dashboard

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
		"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/memory"
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
	vecStore := vectors.NewStore(db)
	searcher := hybrid.NewSearcher(memStore, vecStore)
	sourcePaths := cfg.ResolveSources(projectDir)

	sb := components.NewStatusBar(80)
	sb.SetHints([]components.KeyHint{
		{Key: "F1-F4", Help: "switch tab"},
		{Key: "esc", Help: "browse"},
		{Key: "ctrl+c", Help: "quit"},
	})

	return Model{
		activeTab: tabBrowse,
		browse:    browse.New(projectDir, cfg.Output),
		search:    searchTab.New(projectDir, cfg.Output, searcher, ""),
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
