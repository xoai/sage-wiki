package compile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/tui"
	"github.com/xoai/sage-wiki/internal/tui/components"
)

// fileStatus tracks a compiled file's state.
type fileStatus struct {
	path    string
	status  string // "done", "error"
	modTime time.Time
}

// CompileCompleteMsg signals a compile finished.
type CompileCompleteMsg struct {
	result    *compiler.CompileResult
	err       error
	costInfo  string // formatted cost summary (single line)
}

// fileChangeMsg signals output files changed (for watch mode).
type fileChangeMsg struct{}

// ScanTickMsg triggers periodic output directory scanning.
type ScanTickMsg struct{}

type pane int

const (
	paneFiles pane = iota
	panePreview
)

// Model is the compile dashboard TUI.
type Model struct {
	spinner   spinner.Model
	preview   viewport.Model
	statusBar components.StatusBar

	files      []fileStatus
	cursor     int
	focused    pane
	width      int
	height     int
	compiling  bool
	watching   bool
	lastResult *compiler.CompileResult
	lastError  error
	costInfo   string // cost report summary line
	snapshot   string // source dir hash for change detection

	projectDir  string
	outputDir   string
	sourcePaths []string // source directories to watch
	debounce    int
}

// New creates a compile dashboard model.
func New(projectDir, outputDir string, sourcePaths []string, debounce int) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(tui.Accent)

	vp := viewport.New(40, 20)

	sb := components.NewStatusBar(80)
	sb.SetHints([]components.KeyHint{
		{Key: "↑↓", Help: "navigate"},
		{Key: "tab", Help: "switch pane"},
		{Key: "ctrl+c", Help: "quit"},
	})

	return Model{
		spinner:     s,
		preview:     vp,
		statusBar:   sb,
		projectDir:  projectDir,
		outputDir:   outputDir,
		sourcePaths: sourcePaths,
		debounce:    debounce,
		focused:     paneFiles,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.runCompile(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.preview.Width = m.previewWidth() - 4
		m.preview.Height = m.contentHeight() - 2
		m.statusBar.SetWidth(m.width)

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if m.focused == paneFiles {
				m.focused = panePreview
			} else {
				m.focused = paneFiles
			}
		case "up", "k":
			if m.focused == paneFiles && m.cursor > 0 {
				m.cursor--
				cmds = append(cmds, m.loadPreview())
			} else if m.focused == panePreview {
				var cmd tea.Cmd
				m.preview, cmd = m.preview.Update(msg)
				cmds = append(cmds, cmd)
			}
		case "down", "j":
			if m.focused == paneFiles && m.cursor < len(m.files)-1 {
				m.cursor++
				cmds = append(cmds, m.loadPreview())
			} else if m.focused == panePreview {
				var cmd tea.Cmd
				m.preview, cmd = m.preview.Update(msg)
				cmds = append(cmds, cmd)
			}
		case "pgup", "pgdown":
			if m.focused == panePreview {
				var cmd tea.Cmd
				m.preview, cmd = m.preview.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case CompileCompleteMsg:
		m.compiling = false
		m.lastResult = msg.result
		m.lastError = msg.err
		m.costInfo = msg.costInfo
		m.watching = true
		m.scanOutputFiles()
		m.snapshot = m.dirSnapshot()
		if len(m.files) > 0 {
			cmds = append(cmds, m.loadPreview())
		}
		m.statusBar.SetInfo(m.statusInfo())
		// Start watching for changes
		cmds = append(cmds, m.scanTick())

	case previewContentMsg:
		m.preview.SetContent(msg.content)
		m.preview.GotoTop()

	case ScanTickMsg:
		if !m.watching {
			break
		}
		current := m.dirSnapshot()
		if current != m.snapshot {
			m.snapshot = current
			// Files changed — recompile
			m.compiling = true
			cmds = append(cmds, m.runCompile(), m.spinner.Tick)
		} else {
			cmds = append(cmds, m.scanTick())
		}
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Header
	header := tui.TitleStyle.Render("sage-wiki compile")

	// Left pane: file list
	leftContent := m.renderFileList()
	leftBorder := tui.BorderStyle
	if m.focused == paneFiles {
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

	content := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	status := m.statusBar.View()

	return lipgloss.JoinVertical(lipgloss.Left, header, content, status)
}

// --- Layout ---

func (m Model) listWidth() int  { return m.width * 2 / 5 }
func (m Model) previewWidth() int { return m.width - m.listWidth() }
func (m Model) contentHeight() int { return m.height - 3 }

// --- Rendering ---

func (m Model) renderFileList() string {
	var b strings.Builder

	if m.compiling {
		b.WriteString(m.spinner.View() + " Compiling...\n\n")
	} else if m.watching {
		b.WriteString(tui.SuccessStyle.Render("✓") + " Watching for changes...\n\n")
	}

	if m.lastError != nil {
		b.WriteString(tui.ErrorStyle.Render("Error: "+m.lastError.Error()) + "\n\n")
	}

	visibleHeight := m.contentHeight() - 6
	start := 0
	if m.cursor >= visibleHeight {
		start = m.cursor - visibleHeight + 1
	}

	for i := start; i < len(m.files) && i < start+visibleHeight; i++ {
		f := m.files[i]
		icon := tui.SuccessStyle.Render("✓")
		if f.status == "error" {
			icon = tui.ErrorStyle.Render("✗")
		}

		name := filepath.Base(f.path)
		if len(name) > m.listWidth()-12 {
			name = name[:m.listWidth()-15] + "..."
		}

		if i == m.cursor {
			line := tui.SelectedStyle.Render(fmt.Sprintf("%s %s", icon, name))
			b.WriteString(line + "\n")
			path := tui.DimStyle.Render("  " + f.path)
			b.WriteString(path + "\n")
		} else {
			b.WriteString(fmt.Sprintf("%s %s\n", icon, name))
		}
	}

	if len(m.files) == 0 && !m.compiling {
		b.WriteString(tui.DimStyle.Render("No compiled files yet."))
	}

	return b.String()
}

func (m Model) statusInfo() string {
	if m.lastResult != nil {
		r := m.lastResult
		info := fmt.Sprintf("%d summarized, %d concepts, %d articles",
			r.Summarized, r.ConceptsExtracted, r.ArticlesWritten)
		if m.costInfo != "" {
			info += " | " + m.costInfo
		}
		return info
	}
	return ""
}

// --- Commands ---

func (m Model) runCompile() tea.Cmd {
	m.compiling = true
	return func() tea.Msg {
		result, err := compiler.Compile(m.projectDir, compiler.CompileOpts{})
		var costLine string
		if result != nil && result.CostReport != nil {
			costLine = fmt.Sprintf("~$%.4f", result.CostReport.EstimatedCost)
			if result.CostReport.CacheSavings > 0 {
				costLine += fmt.Sprintf(" (saved $%.4f)", result.CostReport.CacheSavings)
			}
		}
		return CompileCompleteMsg{result: result, err: err, costInfo: costLine}
	}
}

func (m Model) scanTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return ScanTickMsg{}
	})
}

func (m *Model) scanOutputFiles() {
	absOutput := filepath.Join(m.projectDir, m.outputDir)
	var files []fileStatus

	for _, sub := range []string{"summaries", "concepts", "outputs"} {
		dir := filepath.Join(absOutput, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			info, _ := e.Info()
			modTime := time.Time{}
			if info != nil {
				modTime = info.ModTime()
			}
			files = append(files, fileStatus{
				path:    filepath.Join(sub, e.Name()),
				status:  "done",
				modTime: modTime,
			})
		}
	}

	// Sort by modification time (newest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	m.files = files
}

func (m Model) loadPreview() tea.Cmd {
	if m.cursor >= len(m.files) {
		return nil
	}
	f := m.files[m.cursor]
	return func() tea.Msg {
		absPath := filepath.Join(m.projectDir, m.outputDir, f.path)
		data, err := os.ReadFile(absPath)
		if err != nil {
			return previewContentMsg{content: "Could not load: " + err.Error()}
		}

		content := string(data)
		// Strip frontmatter
		if strings.HasPrefix(content, "---\n") {
			if end := strings.Index(content[4:], "\n---"); end >= 0 {
				content = strings.TrimSpace(content[4+end+4:])
			}
		}

		rendered, err := glamour.Render(content, "dark")
		if err != nil {
			rendered = content
		}
		return previewContentMsg{content: rendered}
	}
}

type previewContentMsg struct{ content string }

func (m Model) dirSnapshot() string {
	var total int64
	for _, dir := range m.sourcePaths {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			total += info.Size() + info.ModTime().UnixNano()
			return nil
		})
	}
	return fmt.Sprintf("%d", total)
}

