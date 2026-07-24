package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/auth"
)

var authMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Move credentials from the file store into the OS keychain",
	RunE:  runAuthMigrate,
}

func init() {
	authCmd.AddCommand(authMigrateCmd)
}

// runAuthMigrate implements the spec §4 explicit migration (P2-6): the
// only path that copies file credentials into the OS keychain (no
// automatic migration — the spec's "offer to move").
func runAuthMigrate(cmd *cobra.Command, args []string) error {
	// Don't print "run auth migrate" while the user is running it.
	store := auth.NewStoreNoNotice(auth.DefaultStorePath())
	if store.Backend() != "keychain" {
		// Return the message as the error (cobra prints it once — no
		// duplicate stderr+error output).
		return fmt.Errorf("no OS keychain available on this system — credentials stay in the file")
	}
	result, err := auth.MigrateToKeychain(store)
	if err != nil {
		return err
	}
	for _, p := range result.Moved {
		fmt.Printf("moved %s\n", p)
	}
	for _, p := range result.Failed {
		fmt.Fprintf(os.Stderr, "failed to migrate %s: %v\n", p.Name, p.Err)
	}
	if len(result.Moved) == 0 && len(result.Failed) == 0 && len(result.Skipped) == 0 {
		fmt.Println("nothing to migrate (no file credentials)")
	} else {
		fmt.Println("file backup kept intact (point-in-time)")
	}
	for _, p := range result.Skipped {
		fmt.Printf("skipped %s (already in keychain)\n", p)
	}
	return nil
}
