package wiki

import (
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"path/filepath"
)

func TestWikiStatusCompileQueue(t *testing.T) {
	dir := t.TempDir()
	InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Seed queue rows in every state. Claims go in source_path order:
	// done < failed < leased < pending — limit 3 leaves pending.md unclaimed.
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	items := compiler.NewCompileItemStore(db, config.NowUTC)
	items.Upsert(compiler.CompileItem{SourcePath: "pending.md", Hash: "h", Tier: 1})
	items.Upsert(compiler.CompileItem{SourcePath: "leased.md", Hash: "h", Tier: 1})
	items.Upsert(compiler.CompileItem{SourcePath: "done.md", Hash: "h", Tier: 1, PassIndexed: true})
	items.Upsert(compiler.CompileItem{SourcePath: "failed.md", Hash: "h", Tier: 1})
	if _, err := items.Claim(1, "w1", time.Hour, 3); err != nil {
		t.Fatal(err)
	}
	items.MarkPass("done.md", "embedded")
	items.Release("done.md", "w1", store.ReleaseDone)
	items.Release("failed.md", "w1", store.ReleaseFailed)
	db.Close()

	info, err := GetStatus(dir, nil)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if info.QueueByStatus["failed"] != 1 || info.QueueByStatus["done"] != 1 || info.QueueByStatus["pending"] != 1 || info.QueueByStatus["leased"] != 1 {
		t.Errorf("QueueByStatus = %+v, want 1 of each", info.QueueByStatus)
	}
	out := FormatStatus(info)
	if !strings.Contains(out, "Compile queue: 1 pending, 1 leased, 1 done, 1 failed") {
		t.Errorf("FormatStatus missing queue line:\n%s", out)
	}
}
