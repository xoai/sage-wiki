package main

import "testing"

// TestServeTransportGate pins the bare-serve HTTP-mode gate (Gate 8 Q-1):
// --transport's default is "stdio", so HTTP mode must be decided by
// Flags().Changed, not by the string value — bare serve (unchanged)
// enters HTTP mode; an explicit --transport (any value) keeps the
// transport path.
func TestServeTransportGate(t *testing.T) {
	if serveCmd.Flags().Changed("transport") {
		t.Fatal("fresh serveCmd must have Changed(transport)==false (bare serve → HTTP mode)")
	}
	if err := serveCmd.Flags().Set("transport", "stdio"); err != nil {
		t.Fatal(err)
	}
	if !serveCmd.Flags().Changed("transport") {
		t.Fatal("explicit --transport must have Changed(transport)==true (transport mode)")
	}
	if err := serveCmd.Flags().Set("transport", "stdio"); err == nil {
		// reset for other tests
		serveCmd.Flags().Set("transport", "stdio")
	}
}
