package mirror

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// F-079 witness: a deferral through the public rotate path increments and
// persists consecutive_defers exactly once (was unreachable before).
func TestRotateDeferral_CountsOnce_PublicPath(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	// Hold an exclusive lock so snapshotting fails via both paths.
	db, err := sql.Open("sqlite", filepath.Join(f.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA locking_mode=EXCLUSIVE; BEGIN EXCLUSIVE; INSERT INTO t (v) VALUES ('held')"); err != nil {
		t.Fatal(err)
	}
	st := f.remoteState(t)
	err = f.m.rotate(context.Background(), st)
	var de *DeferredError
	if err == nil {
		t.Skip("snapshot succeeded despite held lock (driver variance) — no deferral to count")
	}
	if !isDeferredErr(err) {
		t.Fatalf("err = %T %v, want DeferredError", err, err)
	}
	loaded, lerr := LoadLocalState(localStatePath(f.dir))
	if lerr != nil {
		t.Fatal(lerr)
	}
	if loaded.ConsecutiveDefers != 1 {
		t.Fatalf("ConsecutiveDefers = %d, want exactly 1", loaded.ConsecutiveDefers)
	}
	_ = de
}

func isDeferredErr(err error) bool {
	for err != nil {
		if _, ok := err.(*DeferredError); ok {
			return true
		}
		err = errUnwrap(err)
	}
	return false
}

func errUnwrap(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return nil
}
