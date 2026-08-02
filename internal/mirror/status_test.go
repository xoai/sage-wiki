package mirror

import (
	"context"
	"testing"
	"time"
)

type fakeChangeSource struct {
	changes []Change
}

func (f *fakeChangeSource) Changes(ctx context.Context, since ChangeToken) ([]Change, ChangeToken, error) {
	return f.changes, ChangeToken{}, nil
}

func openStatusMirror(t *testing.T, fake *fakeS3, dir string, state *State) *Mirror {
	t.Helper()
	_, cfg := setupFakeMirror(t, fake)
	sb, _ := MarshalState(state)
	fake.objects[StateKey("ws/")] = sb
	m, err := Open(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m
}

func TestStatus_Fields(t *testing.T) {
	fake := newFakeS3()
	dir := t.TempDir()
	commit := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	st := fixtureState()
	st.UpdatedAt = commit
	m := openStatusMirror(t, fake, dir, st)

	// Injected clock + pending changes via fake ChangeSource.
	m.now = func() time.Time { return commit.Add(90 * time.Second) }
	m.src = &fakeChangeSource{changes: []Change{{Path: "a"}, {Path: "b"}, {Path: "c"}}}
	m.local.PendingRotation = true
	m.local.ConsecutiveDefers = 11

	s, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !s.Enabled {
		t.Fatal("Enabled should be true (remote state exists)")
	}
	if s.RemoteGeneration != 3 {
		t.Fatalf("RemoteGeneration = %d", s.RemoteGeneration)
	}
	if !s.LastCommit.Equal(commit) {
		t.Fatalf("LastCommit = %v", s.LastCommit)
	}
	if s.PendingChanges != 3 {
		t.Fatalf("PendingChanges = %d (stub must be replaced in Task 14)", s.PendingChanges)
	}
	if s.LagSeconds != 90 {
		t.Fatalf("LagSeconds = %d, want 90 (injected clock)", s.LagSeconds)
	}
	if !s.PendingRotation {
		t.Fatal("PendingRotation should surface")
	}
	if s.RotationDeferred != 11 {
		t.Fatalf("RotationDeferred = %d, want 11 (above max 10)", s.RotationDeferred)
	}
}

func TestStatus_ZeroLagWhenNoPending(t *testing.T) {
	fake := newFakeS3()
	dir := t.TempDir()
	commit := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	st := fixtureState()
	st.UpdatedAt = commit
	m := openStatusMirror(t, fake, dir, st)
	m.now = func() time.Time { return commit.Add(time.Hour) }
	m.src = &fakeChangeSource{} // no pending

	s, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.LagSeconds != 0 {
		t.Fatalf("LagSeconds = %d, want 0 when no pending changes", s.LagSeconds)
	}
}

func TestStatus_RotationDeferredOnlyAboveThreshold(t *testing.T) {
	fake := newFakeS3()
	dir := t.TempDir()
	m := openStatusMirror(t, fake, dir, fixtureState())
	m.local.ConsecutiveDefers = 10 // at, not above, max (default 10)
	s, _ := m.Status(context.Background())
	if s.RotationDeferred != 0 {
		t.Fatalf("RotationDeferred = %d, want 0 at threshold", s.RotationDeferred)
	}
}

func TestStatus_NoRemoteState(t *testing.T) {
	fake := newFakeS3()
	dir := t.TempDir()
	_, cfg := setupFakeMirror(t, fake)
	m, _ := Open(dir, cfg, nil)
	s, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled || s.RemoteGeneration != 0 {
		t.Fatalf("status = %+v, want disabled", s)
	}
}

func TestStatus_ServeRestartNote(t *testing.T) {
	fake := newFakeS3()
	dir := makeWorkspaceWithDB(t)
	m := openStatusMirror(t, fake, dir, fixtureState())
	// Simulate a held engine.lock (serve running) via flock.
	_, release := holdEngineLock(t, dir)
	defer release()
	s, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.ServeRestartNote {
		t.Fatal("ServeRestartNote should be true while serve holds engine.lock")
	}
}
