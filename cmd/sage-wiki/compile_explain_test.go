package main

import (
	"testing"
)

// TestCompileCmd_ForceFlagRegistered pins the --force flag's existence and
// default (off) — the R1 bypass must be a deliberate ask.
func TestCompileCmd_ForceFlagRegistered(t *testing.T) {
	f := compileCmd.Flags().Lookup("force")
	if f == nil {
		t.Fatal("--force flag not registered on compileCmd")
	}
	if f.DefValue != "false" {
		t.Errorf("--force default = %q, want false", f.DefValue)
	}
}

// TestCompileCmd_ExplainFlagRegistered pins --explain.
func TestCompileCmd_ExplainFlagRegistered(t *testing.T) {
	f := compileCmd.Flags().Lookup("explain")
	if f == nil {
		t.Fatal("--explain flag not registered on compileCmd")
	}
}
