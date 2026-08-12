package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify schema version
	var version int
	err = db.ReadDB().QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != 15 {
		t.Errorf("expected schema version 15, got %d", version)
	}

	// Verify tables exist
	tables := []string{"entries", "vec_entries", "entities", "relations", "derived_relations", "learnings", "chunks_meta", "chunks_fts", "vec_chunks", "compile_items", "pending_outputs", "confirmation_sources"}
	for _, table := range tables {
		var name string
		err := db.ReadDB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type IN ('table', 'shadow') AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestIdempotentMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open twice — second open should not re-run migration
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	db1.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	defer db2.Close()

	var count int
	err = db2.ReadDB().QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 15 {
		t.Errorf("expected 15 schema_version rows, got %d", count)
	}
}

func TestWriteTxSerialization(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a test table
	db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE test_counter (val INTEGER)")
		return err
	})
	db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO test_counter VALUES (0)")
		return err
	})

	// Run concurrent writes — should serialize via mutex
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.WriteTx(func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE test_counter SET val = val + 1")
				return err
			})
		}()
	}
	wg.Wait()

	var val int
	db.ReadDB().QueryRow("SELECT val FROM test_counter").Scan(&val)
	if val != 10 {
		t.Errorf("expected counter 10, got %d", val)
	}
}

// TestWriteTxSerializesAcrossHandles proves cross-handle writer reservation
// (spec R2): two storage.Open handles on one SQLite path must serialize write
// transactions such that handle B's transaction body does not enter until
// handle A's write transaction commits. A writeMu on one handle cannot
// serialize another handle (see stored learning 0ac16bd512a342fca11b3c2b38ce64e7),
// so SQLite's own writer reservation is the mechanism under test.
//
// Phases are coordinated with channels. The generous timeout below is a
// deadlock bound only: it stays below the 5s busy_timeout so that handle B's
// blocked BEGIN (immediate mode) can still succeed after A commits, and it
// proves nothing about scheduling — immediate mode keeps B out of its body by
// lock, not by timing.
func TestWriteTxSerializesAcrossHandles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	dbA, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open handle A: %v", err)
	}
	defer dbA.Close()
	dbB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open handle B: %v", err)
	}
	defer dbB.Close()

	// Setup runs through WriteTx so the write connection is only ever touched
	// while its lock is held (WriteDB's documented contract), matching the
	// other tests in this package.
	if err := dbA.WriteTx(func(tx *sql.Tx) error {
		for _, stmt := range []string{
			"CREATE TABLE IF NOT EXISTS test_counter (val INTEGER NOT NULL)",
			"DELETE FROM test_counter",
			"INSERT INTO test_counter (val) VALUES (0)",
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%q: %w", stmt, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	order := make(chan string, 8)   // buffered phase log; drained after joins
	aRead := make(chan struct{})    // A's callback established its snapshot
	bAttempt := make(chan struct{}) // B goroutine is about to call WriteTx
	bDone := make(chan struct{})    // B's WriteTx returned
	resumeA := make(chan struct{})  // A may proceed to write and commit
	aDone := make(chan struct{})    // A's WriteTx returned

	var aErr, bErr error
	var bEntryVal int // value B's callback reads at entry; synced via close(bDone)

	go func() {
		aErr = dbA.WriteTx(func(tx *sql.Tx) error {
			var v int
			if err := tx.QueryRow("SELECT val FROM test_counter").Scan(&v); err != nil {
				return err
			}
			order <- "A:read"
			close(aRead)
			<-resumeA
			order <- "A:write"
			if _, err := tx.Exec("UPDATE test_counter SET val = val + 1"); err != nil {
				return fmt.Errorf("A snapshot upgrade: %w", err)
			}
			return nil
		})
		order <- "A:done"
		close(aDone)
	}()

	// B must not attempt its transaction until A has begun and read; otherwise
	// B can win the SQLite write lock first and the choreography flips.
	<-aRead
	go func() {
		close(bAttempt)
		bErr = dbB.WriteTx(func(tx *sql.Tx) error {
			var v int
			if err := tx.QueryRow("SELECT val FROM test_counter").Scan(&v); err != nil {
				return err
			}
			bEntryVal = v
			order <- "B:enter"
			if _, err := tx.Exec("UPDATE test_counter SET val = val + 1"); err != nil {
				return fmt.Errorf("B write: %w", err)
			}
			order <- "B:write"
			return nil
		})
		order <- "B:done"
		close(bDone)
	}()
	<-bAttempt

	// While A pauses before its write, B attempts WriteTx. Immediate mode
	// blocks B inside BEGIN until A commits; deferred mode lets B enter and
	// commit right away. The wait is a deadlock bound only — generous for
	// scheduling, yet below the 5s busy_timeout so B's blocked BEGIN succeeds
	// after A commits.
	select {
	case <-bDone:
		t.Logf("transaction phase: B entered and committed while A was paused (deferred)")
	case <-time.After(2 * time.Second):
		t.Logf("transaction phase: B remained blocked in BEGIN while A was paused (immediate)")
	}

	close(resumeA)
	<-aDone
	<-bDone

	if aErr != nil {
		t.Errorf("handle A WriteTx: %v", aErr)
	}
	if bErr != nil {
		t.Errorf("handle B WriteTx: %v", bErr)
	}

	entries := make([]string, 0, 8)
drain:
	for {
		select {
		case e := <-order:
			entries = append(entries, e)
		default:
			break drain
		}
	}
	t.Logf("observed transaction order: %v", entries)

	// Ordering anchors — both airtight, neither has a false-red window:
	//
	// 1. Data anchor: B's entry read must observe A's committed increment
	//    (bEntryVal == 1). WAL snapshots contain only committed data, and B's
	//    BEGIN (immediate mode) can succeed only after A's commit releases the
	//    writer lock, so B's entry read observes the increment if and only if
	//    A committed before B entered. The observation is the database state
	//    read inside B's own transaction — not a goroutine log line — so there
	//    is no post-commit logging gap to race with.
	//
	// 2. Log anchor: "B:enter" must follow "A:write" in the phase log.
	//    "A:write" is logged inside A's callback immediately after the UPDATE,
	//    while A still holds the writer lock (commit has not happened yet);
	//    "B:enter" is logged only after B's BEGIN succeeds, which requires the
	//    lock to be free. The write lock therefore orders the two log sends
	//    with no scheduler involvement. ("A:done" is deliberately NOT used as
	//    an anchor: it is logged after WriteTx returns, and Commit may release
	//    the writer lock before the goroutine schedules that send, letting B
	//    enter and log first — a false-red window.)
	if bEntryVal != 1 {
		t.Errorf("handle B entered its transaction before handle A's increment committed (B entry read %d): %v", bEntryVal, entries)
	}
	bEnteredAt, aWriteAt := -1, -1
	for i, e := range entries {
		switch e {
		case "B:enter":
			bEnteredAt = i
		case "A:write":
			aWriteAt = i
		}
	}
	if bEnteredAt >= 0 && aWriteAt >= 0 && bEnteredAt < aWriteAt {
		t.Errorf("handle B entered its transaction body before handle A's write (order: %v)", entries)
	}

	var val int
	if err := dbA.ReadDB().QueryRow("SELECT val FROM test_counter").Scan(&val); err != nil {
		t.Fatalf("read final counter: %v", err)
	}
	if val != 2 {
		t.Errorf("expected final counter 2, got %d (order: %v)", val, entries)
	}
}

func TestConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert test data
	db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO entities (id, type, name, created_at, updated_at)
			VALUES ('e1', 'concept', 'test', datetime('now'), datetime('now'))`)
		return err
	})

	// Concurrent reads should not block
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var name string
			db.ReadDB().QueryRow("SELECT name FROM entities WHERE id='e1'").Scan(&name)
			if name != "test" {
				t.Errorf("expected 'test', got %q", name)
			}
		}()
	}
	wg.Wait()
}

// TestOpen_CreatesParentDir verifies that Open creates a missing parent
// directory so callers don't need to MkdirAll first (fixes #84 obs 1).
func TestOpen_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	// .sage/ does not exist yet — mirror the post-`rm -rf .sage/` state
	dbPath := filepath.Join(dir, ".sage", "wiki.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open with missing parent dir: %v", err)
	}
	defer db.Close()

	// Confirm the parent dir was created
	if _, err := db.WriteDB().Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatalf("write to db: %v", err)
	}
}
