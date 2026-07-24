package compiler

import (
	"errors"
	"testing"
	"time"
)

func TestProgress_SubscribeReceivesEventsInOrder(t *testing.T) {
	p := NewProgress()
	ch, unsub := p.Subscribe(16)
	defer unsub()

	p.StartPhase("summarize", 2)
	p.ItemStart("a.md")
	p.ItemDone("a.md", "a.sum.md")
	p.ItemError("b.md", errors.New("boom"))
	p.EndPhase()

	wantTypes := []string{"phase", "item", "item", "error", "done"}
	for i, wt := range wantTypes {
		select {
		case ev := <-ch:
			if ev.Type != wt {
				t.Fatalf("event %d type = %q, want %q (ev: %+v)", i, ev.Type, wt, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("event %d (%q) not delivered", i, wt)
		}
	}

	// Item events carry name + phase context.
	ch2, unsub2 := p.Subscribe(4)
	defer unsub2()
	p.StartPhase("write", 1)
	p.ItemDone("concept-x", "concept-x.md")
	ev := <-ch2 // phase
	if ev.Phase != "write" || ev.Total != 1 {
		t.Errorf("phase event: %+v", ev)
	}
	ev = <-ch2 // item done
	if ev.Item != "concept-x" || ev.Status != "done" || ev.Detail != "concept-x.md" {
		t.Errorf("item event: %+v", ev)
	}
}

func TestProgress_UnsubscribeStopsDelivery(t *testing.T) {
	p := NewProgress()
	ch, unsub := p.Subscribe(4)
	unsub()

	p.StartPhase("summarize", 1)
	ev, ok := <-ch
	if ok {
		t.Fatalf("event delivered after unsubscribe: %+v", ev)
	}
	// A second read confirms the channel is closed (not just empty).
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after unsubscribe")
	}
}

func TestProgress_SlowSubscriberDoesNotBlock(t *testing.T) {
	p := NewProgress()
	_, unsub := p.Subscribe(1) // buffer 1, never drained
	defer unsub()

	done := make(chan struct{})
	go func() {
		p.StartPhase("summarize", 100)
		for i := 0; i < 100; i++ {
			p.ItemDone("x.md", "")
		}
		p.EndPhase()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline blocked on a full subscriber buffer")
	}
}

func TestProgress_NoSubscribersNoPanic(t *testing.T) {
	p := NewProgress()
	p.StartPhase("s", 1)
	p.ItemStart("a")
	p.ItemDone("a", "")
	p.ItemError("b", errors.New("x"))
	p.EndPhase()
}

func TestCompileUsesInjectedProgress(t *testing.T) {
	h := newWorkerHarness(t, 1, 200)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	progress := NewProgress()
	events, unsub := progress.Subscribe(64)
	defer unsub()

	if _, err := Compile(h.dir, CompileOpts{Progress: progress}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	sawPhase := false
	for {
		select {
		case ev := <-events:
			if ev.Type == "phase" {
				sawPhase = true
			}
		default:
			if !sawPhase {
				t.Error("no phase events on the injected Progress hub")
			}
			return
		}
	}
}
