package postgres

import "testing"

// PB-1: the F-055 recurrence mode (append a migration, forget the constant)
// must be caught without a database — this pin runs everywhere.
func TestCurrentSchemaVersionTracksMigrations(t *testing.T) {
	if currentSchemaVersion != len(schemaMigrations) {
		t.Fatalf("currentSchemaVersion = %d but len(schemaMigrations) = %d — bump the constant in the same commit that appends a migration",
			currentSchemaVersion, len(schemaMigrations))
	}
}
