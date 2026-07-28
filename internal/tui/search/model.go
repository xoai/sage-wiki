package search

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/tui"
	"github.com/xoai/sage-wiki/internal/tui/components"
)

// SearchFn is the pipeline-agnostic search seam: the dashboard injects
// unified (search.Run) or legacy per the config pipeline switch (M5).
type SearchFn func(query string, limit int) ([]search.DocResult, error)

// searchResult wraps a hybrid search result for display.
type searchResult struct {
	Name    string
	Path    string
	Score   float64
	Content string
}

// searchResultsMsg carries search results back to the model.
type searchResultsMsg struct {
	results []searchResult
	query   string
}

// articleContentMsg carries article content for preview.
type articleContentMsg struct {
	title   string
	content string
}

// debounceMsg triggers a search after the debounce delay.
type debounceMsg struct {
	query string
	id    int
}

// pane tracks which pane is focused.
type pane int

const (
	paneList pane = iota
	panePreview
)

// Model is the interactive search TUI.
type Model struct {
	input     textinput.Model
	preview   viewport.Model
	statusBar components.StatusBar

	results     []searchResult
	cursor      int
	focused     pane
	width       int
	height      int
	debounceID  int
	lastQuery   string
	previewText string

	// Dependencies
	projectDir string
	outputDir  string
	searchFn   SearchFn // pipeline-agnostic search (unified or legacy per config)
}

// New creates a new interactive search model.
func New(projectDir, outputDir string, searchFn SearchFn, initialQuery string) Model {
	ti := textinput.New()
	ti.Placeholder = "Search wiki..."
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 40
	if initialQuery != "" {
		ti.SetValue(initialQuery)
	}

	vp := viewport.New(40, 20)

	sb := components.NewStatusBar(80)
	sb.SetHints([]components.KeyHint{
		{Key: "↑↓", Help: "navigate"},
		{Key: "enter", Help: "open in $EDITOR"},
		{Key: "tab", Help: "switch pane"},
		{Key: "esc", Help: "back"},
	})

	return Model{
		input:      ti,
		preview:    vp,
		statusBar:  sb,
		projectDir: projectDir,
		outputDir:  outputDir,
		searchFn:   searchFn,
		focused:    paneList,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if m.input.Value() != "" {
		cmds = append(cmds, m.doSearch(m.input.Value()))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = m.listWidth() - 4
		m.preview.Width = m.previewWidth() - 4
		m.preview.Height = m.contentHeight() - 2
		m.statusBar.SetWidth(m.width)
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		// When list pane is focused, forward ALL keys to text input first
		// (except navigation keys that we explicitly handle)
		if m.focused == paneList {
			switch msg.String() {
			case "tab":
				m.focused = panePreview
				m.input.Blur()
				return m, nil
			case "enter":
				if len(m.results) > 0 && m.cursor < len(m.results) {
					return m, m.openInEditor(m.results[m.cursor].Path)
				}
			case "up":
				if m.cursor > 0 {
					m.cursor--
					cmds = append(cmds, m.loadPreview())
					return m, tea.Batch(cmds...)
				}
			case "down":
				if m.cursor < len(m.results)-1 {
					m.cursor++
					cmds = append(cmds, m.loadPreview())
					return m, tea.Batch(cmds...)
				}
			default:
				// Forward everything else (letters, backspace, etc.) to text input
				oldVal := m.input.Value()
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				cmds = append(cmds, cmd)

				if m.input.Value() != oldVal {
					m.debounceID++
					cmds = append(cmds, m.debounce(m.input.Value(), m.debounceID))
				}
				return m, tea.Batch(cmds...)
			}
		} else {
			// Preview pane focused
			switch msg.String() {
			case "tab":
				m.focused = paneList
				m.input.Focus()
				return m, nil
			case "up", "k", "down", "j", "pgup", "pgdown":
				var cmd tea.Cmd
				m.preview, cmd = m.preview.Update(msg)
				return m, cmd
			}
		}

	case debounceMsg:
		if msg.id == m.debounceID && msg.query != m.lastQuery {
			cmds = append(cmds, m.doSearch(msg.query))
		}

	case searchResultsMsg:
		m.results = msg.results
		m.lastQuery = msg.query
		m.cursor = 0
		m.statusBar.SetInfo(fmt.Sprintf("%d results", len(m.results)))
		if len(m.results) > 0 {
			cmds = append(cmds, m.loadPreview())
		} else {
			m.previewText = ""
			m.preview.SetContent("No results found.")
		}

	case articleContentMsg:
		m.previewText = msg.content
		rendered, err := glamour.Render(msg.content, "dark")
		if err != nil {
			rendered = msg.content
		}
		m.preview.SetContent(rendered)
		m.preview.GotoTop()
	}

	// Pass remaining messages to preview viewport
	if m.focused == panePreview {
		var cmd tea.Cmd
		m.preview, cmd = m.preview.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Header
	header := tui.TitleStyle.Render("sage-wiki search")

	// Left pane: input + results list
	leftContent := m.renderList()
	leftBorder := tui.BorderStyle
	if m.focused == paneList {
		leftBorder = tui.ActiveBorderStyle
	}
	leftPane := leftBorder.
		Width(m.listWidth() - 2).
		Height(m.contentHeight() - 2).
		Render(leftContent)

	// Right pane: preview
	rightBorder := tui.BorderStyle
	if m.focused == panePreview {
		rightBorder = tui.ActiveBorderStyle
	}
	rightPane := rightBorder.
		Width(m.previewWidth() - 2).
		Height(m.contentHeight() - 2).
		Render(m.preview.View())

	// Join panes horizontally
	content := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)

	// Status bar
	status := m.statusBar.View()

	return lipgloss.JoinVertical(lipgloss.Left, header, content, status)
}

// --- Layout helpers ---

func (m Model) listWidth() int {
	return m.width * 2 / 5
}

func (m Model) previewWidth() int {
	return m.width - m.listWidth()
}

func (m Model) contentHeight() int {
	return m.height - 3 // header + status bar
}

// --- Rendering ---

func (m Model) renderList() string {
	var b strings.Builder
	b.WriteString(m.input.View() + "\n\n")

	visibleHeight := m.contentHeight() - 5 // input + padding
	start := 0
	if m.cursor >= visibleHeight {
		start = m.cursor - visibleHeight + 1
	}

	for i := start; i < len(m.results) && i < start+visibleHeight; i++ {
		r := m.results[i]
		name := r.Name
		if len(name) > m.listWidth()-14 {
			name = name[:m.listWidth()-17] + "..."
		}

		score := tui.DimStyle.Render(fmt.Sprintf("%.4f", r.Score))

		if i == m.cursor {
			line := tui.SelectedStyle.Render("● " + name)
			b.WriteString(line + " " + score + "\n")
			path := tui.DimStyle.Render("  " + r.Path)
			b.WriteString(path + "\n")
		} else {
			line := "○ " + name
			b.WriteString(line + " " + score + "\n")
		}
	}

	return b.String()
}

// --- Commands ---

func (m Model) debounce(query string, id int) tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg {
		return debounceMsg{query: query, id: id}
	})
}

func (m Model) doSearch(query string) tea.Cmd {
	return func() tea.Msg {
		results, err := m.searchFn(query, 20)
		if err != nil {
			return searchResultsMsg{query: query}
		}

		var items []searchResult
		for _, r := range results {
			name := strings.TrimSuffix(filepath.Base(r.ArticlePath), ".md")
			path := r.ArticlePath
			// Strip output dir prefix
			if strings.HasPrefix(path, m.outputDir+"/") {
				path = strings.TrimPrefix(path, m.outputDir+"/")
			}
			items = append(items, searchResult{
				Name:    name,
				Path:    path,
				Score:   r.RRFScore,
				Content: r.Content,
			})
		}
		return searchResultsMsg{results: items, query: query}
	}
}

func (m Model) loadPreview() tea.Cmd {
	if m.cursor >= len(m.results) {
		return nil
	}
	r := m.results[m.cursor]
	return func() tea.Msg {
		absPath := filepath.Join(m.projectDir, m.outputDir, r.Path)
		data, err := os.ReadFile(absPath)
		if err != nil {
			return articleContentMsg{title: r.Name, content: "Could not load article: " + err.Error()}
		}

		content := string(data)
		// Strip frontmatter for preview
		if strings.HasPrefix(content, "---\n") {
			if end := strings.Index(content[4:], "\n---"); end >= 0 {
				content = strings.TrimSpace(content[4+end+4:])
			}
		}

		return articleContentMsg{title: r.Name, content: content}
	}
}

func (m Model) openInEditor(articlePath string) tea.Cmd {
	absPath := filepath.Join(m.projectDir, m.outputDir, articlePath)
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, absPath)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return nil
	})
}
