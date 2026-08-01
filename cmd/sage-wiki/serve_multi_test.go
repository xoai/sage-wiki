package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// serveTestCmd builds a command carrying the serve flags the exclusion
// logic reads (same names/defaults as the real registration).
func serveTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("transport", "stdio", "")
	cmd.Flags().String("addr", "", "")
	cmd.Flags().String("workspace", "", "")
	cmd.Flags().String("workspace-root", "", "")
	cmd.Flags().Bool("ui", false, "")
	cmd.Flags().String("token", "", "")
	cmd.Flags().String("token-file", "", "")
	cmd.Flags().Int("max-concurrent-compiles", 2, "")
	cmd.Flags().Duration("drain-timeout", 0, "")
	cmd.Flags().Bool("insecure-no-auth", false, "")
	return cmd
}

func TestServeWorkspaceRoot_FlagExclusions(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		mutate  func(cmd *cobra.Command)
		wantErr string
	}{
		{"transport", func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("transport", "stdio")
		}, "--transport"},
		{"ui", func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("ui", "true")
		}, "--ui"},
		{"workspace", func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("workspace", t.TempDir())
		}, "--workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := serveTestCmd()
			_ = cmd.Flags().Set("workspace-root", root)
			tc.mutate(cmd)
			err := runServe(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want exclusion naming %s", err, tc.wantErr)
			}
		})
	}
}
