package compiler

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ProgressEvent is one observable progress occurrence (P2-3, spec C6).
// Type is one of: phase, item, error, queue, done. Status qualifies item
// events ("start"/"done"); queue events carry the transition in Detail.
type ProgressEvent struct {
	Type   string
	Phase  string
	Item   string
	Status string
	Detail string
	Total  int
	Done   int
	Errors int
	Time   time.Time
}

// Progress tracks and displays real-time compilation progress.
type Progress struct {
	mu        sync.Mutex
	phase     string
	total     int
	done      int
	errors    int
	current   string
	startTime time.Time
	isTTY     bool
	spinner   *spinner

	// Accumulated results for summary
	summarized []string
	concepts   []string
	articles   []string
	failures   []string

	// Event subscribers (P2-3). Sends are non-blocking: a full subscriber
	// buffer drops the event rather than stalling the pipeline.
	subs map[chan ProgressEvent]struct{}
}

// Subscribe registers a buffered event channel and returns an unsubscribe
// func. The channel is closed on unsubscribe.
func (p *Progress) Subscribe(buffer int) (<-chan ProgressEvent, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan ProgressEvent, buffer)
	p.mu.Lock()
	if p.subs == nil {
		p.subs = make(map[chan ProgressEvent]struct{})
	}
	p.subs[ch] = struct{}{}
	p.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subs, ch)
			close(ch)
			p.mu.Unlock()
		})
	}
}

// SubscriberCount reports the live subscription count (tests + ops probes).
func (p *Progress) SubscriberCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subs)
}

// emit fans an event out to subscribers without blocking the pipeline.
// Callers hold no lock.
func (p *Progress) emit(ev ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	for ch := range p.subs {
		select {
		case ch <- ev:
		default: // slow subscriber — drop, never stall the compile
		}
	}
}

// spinner provides a rotating animation for long operations.
type spinner struct {
	stop   chan struct{}
	frames []string
}

func newSpinner() *spinner {
	return &spinner{
		stop:   make(chan struct{}),
		frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	}
}

func (s *spinner) run(getMessage func() string) {
	go func() {
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				// Clear spinner line
				fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))
				return
			case <-ticker.C:
				msg := getMessage()
				frame := s.frames[i%len(s.frames)]
				fmt.Fprintf(os.Stderr, "\r  %s %s", frame, msg)
				i++
			}
		}
	}()
}

func (s *spinner) halt() {
	close(s.stop)
	time.Sleep(100 * time.Millisecond) // let the goroutine clear the line
}

// NewProgress creates a progress tracker.
func NewProgress() *Progress {
	// Detect if stdout is a terminal (for interactive display)
	info, _ := os.Stderr.Stat()
	isTTY := info.Mode()&os.ModeCharDevice != 0

	return &Progress{
		startTime: time.Now(),
		isTTY:     isTTY,
	}
}

// StartPhase begins tracking a new compilation phase.
func (p *Progress) StartPhase(name string, total int) {
	// Stop previous spinner BEFORE acquiring lock (spinner goroutine holds lock too)
	if p.spinner != nil {
		p.spinner.halt()
		p.spinner = nil
	}

	p.mu.Lock()
	p.phase = name
	p.total = total
	p.done = 0
	p.errors = 0
	p.current = ""
	p.mu.Unlock()

	if total > 0 {
		fmt.Fprintf(os.Stderr, "\n⏳ %s (%d items)\n", name, total)
	} else {
		fmt.Fprintf(os.Stderr, "\n⏳ %s\n", name)
	}

	// Start spinner on TTY
	if p.isTTY && total > 0 {
		p.spinner = newSpinner()
		p.spinner.run(func() string {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.current != "" {
				return fmt.Sprintf("%s %s", p.progressBar(), truncatePath(p.current, 50))
			}
			return p.progressBar()
		})
	}

	p.emit(ProgressEvent{Type: "phase", Phase: name, Total: total})
}

// ItemStart marks the beginning of processing an item.
func (p *Progress) ItemStart(name string) {
	p.mu.Lock()
	p.current = name
	phase := p.phase
	p.mu.Unlock()
	p.emit(ProgressEvent{Type: "item", Phase: phase, Item: name, Status: "start"})
}

// ItemDone marks successful completion of an item.
func (p *Progress) ItemDone(name string, detail string) {
	p.mu.Lock()
	p.done++
	done, total, errs, phase := p.done, p.total, p.errors, p.phase
	p.mu.Unlock()

	p.emit(ProgressEvent{Type: "item", Phase: phase, Item: name, Status: "done", Detail: detail, Done: done, Total: total, Errors: errs})

	// Clear spinner line before printing
	if p.isTTY {
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))
		fmt.Fprintf(os.Stderr, "  ✓ %s", truncatePath(name, 60))
		if detail != "" {
			fmt.Fprintf(os.Stderr, " → %s", truncatePath(detail, 30))
		}
		fmt.Fprintln(os.Stderr)
	} else {
		fmt.Fprintf(os.Stderr, "  [%d/%d] ✓ %s", done, total, name)
		if detail != "" {
			fmt.Fprintf(os.Stderr, " → %s", detail)
		}
		fmt.Fprintln(os.Stderr)
	}
}

// ItemError marks a failed item.
func (p *Progress) ItemError(name string, err error) {
	p.mu.Lock()
	p.done++
	p.errors++
	p.failures = append(p.failures, fmt.Sprintf("%s: %s", name, err))
	done, total, errs, phase := p.done, p.total, p.errors, p.phase
	p.mu.Unlock()

	p.emit(ProgressEvent{Type: "error", Phase: phase, Item: name, Detail: err.Error(), Done: done, Total: total, Errors: errs})

	if p.isTTY {
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 80))
		fmt.Fprintf(os.Stderr, "  ✗ %s — %s\n", truncatePath(name, 50), err)
	} else {
		fmt.Fprintf(os.Stderr, "  [%d/%d] ✗ %s — %s\n", done, total, name, err)
	}
}

// ConceptsDiscovered reports concepts found during extraction.
func (p *Progress) ConceptsDiscovered(concepts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.concepts = append(p.concepts, concepts...)

	if len(concepts) > 0 {
		preview := concepts
		if len(preview) > 5 {
			preview = preview[:5]
		}
		fmt.Fprintf(os.Stderr, "  💡 %d concepts: %s", len(concepts), strings.Join(preview, ", "))
		if len(concepts) > 5 {
			fmt.Fprintf(os.Stderr, " (+%d more)", len(concepts)-5)
		}
		fmt.Fprintln(os.Stderr)
	}
}

// EndPhase marks phase completion.
func (p *Progress) EndPhase() {
	// Stop spinner first (outside lock to avoid deadlock)
	if p.spinner != nil {
		p.spinner.halt()
		p.spinner = nil
	}

	p.mu.Lock()
	phase, done, total, errs := p.phase, p.done, p.total, p.errors
	p.mu.Unlock()

	p.emit(ProgressEvent{Type: "done", Phase: phase, Done: done, Total: total, Errors: errs})

	if total > 0 {
		fmt.Fprintf(os.Stderr, "  Done: %d/%d", done, total)
		if errs > 0 {
			fmt.Fprintf(os.Stderr, " (%d errors)", errs)
		}
		fmt.Fprintln(os.Stderr)
	}
}

// QueueEvent reports a durable-queue transition (P2-3, spec C6): kind is
// claimed/requeued/dead-lettered, detail carries context, count the items
// affected. Emitted by the compile worker; stderr stays silent for these.
func (p *Progress) QueueEvent(kind, detail string, count int) {
	p.emit(ProgressEvent{Type: "queue", Status: kind, Detail: detail, Done: count})
}

// Summary prints the final compilation summary.
func (p *Progress) Summary(result *CompileResult) {
	if p.spinner != nil {
		p.spinner.halt()
		p.spinner = nil
	}
	elapsed := time.Since(p.startTime)

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "📊 Compile complete in %s\n", elapsed.Round(time.Second))
	fmt.Fprintf(os.Stderr, "   Sources:  +%d added, ~%d modified, -%d removed\n",
		result.Added, result.Modified, result.Removed)

	// Tier breakdown
	if result.TierIndexed > 0 || result.TierEmbedded > 0 {
		fmt.Fprintf(os.Stderr, "   Indexed:   %d sources", result.TierIndexed)
		if result.TierEmbedded > 0 {
			fmt.Fprintf(os.Stderr, " (%d embedded)", result.TierEmbedded)
		}
		fmt.Fprintln(os.Stderr)
	}

	if result.Summarized > 0 {
		fmt.Fprintf(os.Stderr, "   Summaries: %d written\n", result.Summarized)
	}
	if result.ConceptsExtracted > 0 {
		fmt.Fprintf(os.Stderr, "   Concepts:  %d extracted\n", result.ConceptsExtracted)
	}
	if result.ArticlesWritten > 0 {
		fmt.Fprintf(os.Stderr, "   Articles:  %d written\n", result.ArticlesWritten)
	}
	if result.Errors > 0 {
		fmt.Fprintf(os.Stderr, "   Errors:    %d\n", result.Errors)
	}

	// Hint when sources were indexed but not compiled
	if result.TierIndexed > 0 && result.Summarized == 0 && result.TierCompiled == 0 {
		fmt.Fprintf(os.Stderr, "   ℹ️  %d sources indexed at Tier 0-1 (searchable but not compiled into articles).\n", result.TierIndexed)
		fmt.Fprintf(os.Stderr, "      Set default_tier: 3 in config.yaml to compile all sources,\n")
		fmt.Fprintf(os.Stderr, "      or use wiki_compile_topic via MCP to compile specific topics.\n")
	}

	fmt.Fprintln(os.Stderr)
}

func (p *Progress) progressBar() string {
	if p.total == 0 {
		return ""
	}
	width := 20
	filled := (p.done * width) / p.total
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %d/%d", bar, p.done, p.total)
}

func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	// Show the last maxLen-3 chars with "..." prefix
	return "..." + path[len(path)-maxLen+3:]
}
