// Package engine is the supported embedding surface for sage-wiki (SPEC-01).
// It is a facade plus types: all logic lives in internal/ packages; this
// package only adapts them into a stable, workspace-scoped API.
//
// # Quick start
//
//	w, err := engine.Init(ctx, dir)               // or engine.Open(ctx, dir)
//	if err != nil { ... }
//	defer w.Close()
//	id, _ := w.Capture(ctx, engine.Source{Path: "doc.md"})
//	res, _ := w.Compile(ctx, engine.CompileRequest{Selector: "pending", Tier: engine.TierUseConfig})
//	hits, _ := w.Search(ctx, engine.SearchRequest{Query: "attention", Limit: 5})
//
// See examples/embed for a complete offline program.
//
// # Workspaces and locking
//
// One Workspace per directory. Open takes an exclusive advisory lock
// (flock with a lockfile fallback) so two processes — or two Workspaces in
// one process — cannot write the same workspace concurrently; a second
// read-write Open fails fast with ErrLocked. WithReadOnly opens without
// the lock: reads never contend with a writer (sqlite WAL keeps them
// consistent) and mutators return ErrReadOnly.
//
// # Workspace format and adoption
//
// Workspaces carry a format_version in .manifest.json. A workspace
// predating format versioning (v0.2.x) opens READ-ONLY: mutating calls
// return ErrIncompatibleVersion until it is adopted by opening with
// WithUpgrade (one-way, explicit consent).
//
// # Options
//
// WithConfigFile (alternate config path), WithProvider (inject an
// embedding provider for vector search), WithCompileProvider (opt into
// caller-owned synchronous Workspace.Compile completions without config
// credentials), WithEventSink (usage events today; the typed event union
// arrives with SPEC-07), WithLogger, WithUpgrade, WithReadOnly, WithLimits
// (per-caller limits override — SPEC-08). The two provider options are
// independent; callers may pass the same provider to both.
//
// # Process-global state (documented adaptation)
//
// Per SPEC-01's interleaving guarantee, per-workspace BEHAVIOR is isolated
// on the Workspace (prompt overrides, storage, provider, price registry —
// the registry is cached per CLIENT, never per process). Two pieces of
// process-global TELEMETRY remain by design: the in-process metrics
// registry and the slog logger — they aggregate across Workspaces in one
// process, which is what an embedder running many workspaces wants. The
// usage ledger encodes decimal costs with a type-scoped marshaler, NOT
// the shopspring library global, so importing the engine does not change
// JSON encoding anywhere else in the host process.
package engine
