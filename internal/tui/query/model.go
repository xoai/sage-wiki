package query

import (
	"context"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	queryPkg "github.com/xoai/sage-wiki/internal/query"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/tui"
	"github.com/xoai/sage-wiki/internal/tui/components"
)

// qaPair holds a question and its answer.
type qaPair struct {
	question string
	answer   string
	sources  []string
	saved    bool
}

// tokenMsg carries a streaming token.
type tokenMsg struct{ text string }

// streamDoneMsg signals streaming finished.
type streamDoneMsg struct {
	sources []string
	err     error
}

// saveResultMsg signals the save result.
type saveResultMsg struct {
	path string
	err  error
}

// Model is the conversational Q&A TUI.
type Model struct {
	input     textinput.Model
	history   viewport.Model
	spinner   spinner.Model
	statusBar components.StatusBar

	pairs      []qaPair
	streaming  bool
	currentAns strings.Builder
	cancelFn   context.CancelFunc
	width      int
	height     int

	projectDir string
	db         store.DBHandle
}

// New creates a conversational query model.
func New(projectDir string, db store.DBHandle) Model {
	ti := textinput.New()
	ti.Placeholder = "Ask a question..."
	ti.Focus()
	ti.CharLimit = 500
	ti.Width = 60

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(tui.Accent)

	vp := viewport.New(80, 20)

	sb := components.NewStatusBar(80)
	sb.SetHints([]components.KeyHint{
		{Key: "enter", Help: "send"},
		{Key: "ctrl+s", Help: "save answer"},
		{Key: "esc", Help: "cancel stream"},
		{Key: "ctrl+c", Help: "quit"},
	})

	return Model{
		input:      ti,
		history:    vp,
		spinner:    s,
		statusBar:  sb,
		projectDir: projectDir,
		db:         db,
	}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = m.width - 6
		m.history.Width = m.width - 2
		m.history.Height = m.height - 5
		m.statusBar.SetWidth(m.width)
		m.renderHistory()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cleanup()
			return m, tea.Quit
		case "esc":
			if m.streaming && m.cancelFn != nil {
				m.cancelFn()
				m.cancelFn = nil
				m.streaming = false
				m.finishCurrentAnswer()
			}
		case "ctrl+s":
			if len(m.pairs) > 0 {
				last := &m.pairs[len(m.pairs)-1]
				if !last.saved && last.answer != "" {
					return m, m.saveAnswer(last)
				}
			}
		case "enter":
			if !m.streaming && m.input.Value() != "" {
				question := m.input.Value()
				m.input.SetValue("")
				m.streaming = true
				m.currentAns.Reset()
				m.pairs = append(m.pairs, qaPair{question: question})
				m.renderHistory()
				return m, tea.Batch(m.spinner.Tick, m.streamQuery(question))
			}
		}

	case spinner.TickMsg:
		if m.streaming {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tokenMsg:
		m.currentAns.WriteString(msg.text)
		if len(m.pairs) > 0 {
			m.pairs[len(m.pairs)-1].answer = m.currentAns.String()
		}
		m.renderHistory()
		m.history.GotoBottom()

	case streamDoneMsg:
		m.streaming = false
		m.cancelFn = nil
		if msg.err != nil && len(m.pairs) > 0 {
			current := m.pairs[len(m.pairs)-1].answer
			if current == "" {
				m.pairs[len(m.pairs)-1].answer = "Error: " + msg.err.Error()
			}
		}
		if len(m.pairs) > 0 {
			m.pairs[len(m.pairs)-1].sources = msg.sources
		}
		m.finishCurrentAnswer()

	case saveResultMsg:
		if msg.err != nil {
			m.statusBar.SetInfo("Save failed: " + msg.err.Error())
		} else {
			if len(m.pairs) > 0 {
				m.pairs[len(m.pairs)-1].saved = true
			}
			m.statusBar.SetInfo("Saved to " + msg.path)
			m.renderHistory()
		}
	}

	// Pass to text input when not streaming
	if !m.streaming {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	header := tui.TitleStyle.Render("sage-wiki Q&A")

	historyBorder := tui.BorderStyle.
		Width(m.width - 2).
		Height(m.height - 5)
	historyView := historyBorder.Render(m.history.View())

	inputLine := m.input.View()
	if m.streaming {
		inputLine = m.spinner.View() + " streaming..."
	}

	status := m.statusBar.View()

	return lipgloss.JoinVertical(lipgloss.Left, header, historyView, inputLine, status)
}

// --- Helpers ---

func (m *Model) cleanup() {
	if m.cancelFn != nil {
		m.cancelFn()
		m.cancelFn = nil
	}
}

func (m *Model) renderHistory() {
	var b strings.Builder

	questionStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.Accent)
	sourceStyle := lipgloss.NewStyle().Foreground(tui.DimColor).Italic(true)
	savedStyle := lipgloss.NewStyle().Foreground(tui.Green)

	for i, pair := range m.pairs {
		b.WriteString(questionStyle.Render("You: "+pair.question) + "\n\n")

		if pair.answer != "" {
			isCurrentlyStreaming := m.streaming && i == len(m.pairs)-1
			if !isCurrentlyStreaming {
				// Completed answer — render via glamour
				rendered, err := glamour.Render(pair.answer, "dark")
				if err != nil {
					b.WriteString(pair.answer)
				} else {
					b.WriteString(rendered)
				}
			} else {
				// Still streaming — show raw text with cursor
				b.WriteString(pair.answer)
				b.WriteString("█")
			}
		}

		if len(pair.sources) > 0 {
			var names []string
			for _, s := range pair.sources {
				parts := strings.Split(s, "/")
				name := strings.TrimSuffix(parts[len(parts)-1], ".md")
				names = append(names, name)
			}
			b.WriteString("\n" + sourceStyle.Render("Sources: "+strings.Join(names, ", ")))
		}

		if pair.saved {
			b.WriteString(" " + savedStyle.Render("✓ saved"))
		}

		b.WriteString("\n\n" + strings.Repeat("─", min(m.width-4, 60)) + "\n\n")
	}

	m.history.SetContent(b.String())
}

func (m *Model) finishCurrentAnswer() {
	m.renderHistory()
	m.history.GotoBottom()
}

// --- Commands ---

func (m *Model) streamQuery(question string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFn = cancel
	projectDir := m.projectDir
	db := m.db

	return func() tea.Msg {
		sources, err := queryPkg.StreamQuery(ctx, projectDir, question, 5,
			func(token string) {
				sendToken(token)
			}, db)

		return streamDoneMsg{sources: sources, err: err}
	}
}

func (m Model) saveAnswer(pair *qaPair) tea.Cmd {
	question := pair.question
	answer := pair.answer
	sources := pair.sources
	projectDir := m.projectDir
	db := m.db
	return func() tea.Msg {
		path, err := queryPkg.SaveAnswer(projectDir, question, answer, sources, db)
		return saveResultMsg{path: path, err: err}
	}
}

// activeProgram holds the running tea.Program for token streaming.
var (
	activeProgram   *tea.Program
	activeProgramMu sync.Mutex
)

// SetActiveProgram sets the program reference for streaming token delivery.
func SetActiveProgram(p *tea.Program) {
	activeProgramMu.Lock()
	activeProgram = p
	activeProgramMu.Unlock()
}

func sendToken(token string) {
	activeProgramMu.Lock()
	p := activeProgram
	activeProgramMu.Unlock()
	if p != nil {
		p.Send(tokenMsg{text: token})
	}
}
