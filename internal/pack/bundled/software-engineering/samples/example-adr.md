# ADR-001: Use SQLite for Local Storage

## Status

Accepted

## Context

The application needs a local data store for structured data with full-text
search. Options considered: SQLite, LevelDB, flat JSON files, embedded Postgres.

## Decision

Use SQLite with FTS5 extension for local storage.

## Consequences

- SQLite is a single file, simplifying backup and deployment
- FTS5 provides built-in full-text search without external dependencies
- Write concurrency is limited to one writer at a time
- No network protocol needed for local-only usage
