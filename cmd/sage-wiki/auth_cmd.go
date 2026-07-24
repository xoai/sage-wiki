package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/cli"
)

const tosWarning = `Note: Subscription auth uses your existing LLM subscription credentials.
Some providers may restrict third-party use of subscription tokens in
their Terms of Service, which could change at any time. If you encounter
access issues, switch to an API key (api.auth: api_key in config.yaml).
`

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage subscription authentication for LLM providers",
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show stored credentials for all providers",
	RunE:  runAuthStatus,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials for a provider",
	RunE:  runAuthLogout,
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with an LLM provider via OAuth",
	RunE:  runAuthLogin,
}

var authImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import credentials from an existing CLI tool",
	RunE:  runAuthImport,
}

func init() {
	authLogoutCmd.Flags().String("provider", "", "Provider (openai, anthropic, claude, copilot, gemini)")
	authLogoutCmd.MarkFlagRequired("provider")

	authLoginCmd.Flags().String("provider", "", "Provider to login (openai, anthropic)")
	authLoginCmd.MarkFlagRequired("provider")

	authImportCmd.Flags().String("provider", "", "Provider to import from (openai, claude, copilot, gemini)")
	authImportCmd.MarkFlagRequired("provider")

	authCmd.AddCommand(authStatusCmd, authLogoutCmd, authLoginCmd, authImportCmd)
	rootCmd.AddCommand(authCmd)
}

func ensureTOS(store *auth.Store) {
	if store.IsTOSAcknowledged() {
		return
	}
	fmt.Print(tosWarning)
	store.AcknowledgeTOS()
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	store := auth.NewStore(auth.DefaultStorePath())

	creds, err := store.List()
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	if len(creds) == 0 {
		fmt.Println("No stored credentials.")
		fmt.Println("\nRun `sage-wiki auth login --provider <name>` or `sage-wiki auth import --provider <name>` to add credentials.")
		return nil
	}

	// P2-6: backend line (design D5) + per-credential location probe.
	fmt.Printf("backend: %s\n", store.Backend())
	fmt.Println("Stored credentials:")
	fmt.Println()
	for name, cred := range creds {
		fmt.Println(auth.FormatStatusLine(name, cred, store.CredentialLocation(name)))
	}

	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	providerFlag, _ := cmd.Flags().GetString("provider")

	name, err := auth.ResolveProviderName(providerFlag)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	store := auth.NewStore(auth.DefaultStorePath())

	if _, err := store.Get(name); err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("no credentials stored for %q", name))
	}

	if err := store.Delete(name); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	fmt.Printf("Credentials removed for %s.\n", name)
	return nil
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	providerFlag, _ := cmd.Flags().GetString("provider")

	name, err := auth.ResolveProviderName(providerFlag)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	cfg, ok := auth.Providers[name]
	if !ok || cfg.FlowType != auth.FlowPKCE {
		return cli.CLIError(outputFormat, fmt.Errorf("provider %q does not support OAuth login — use `sage-wiki auth import --provider %s` instead", name, name))
	}

	store := auth.NewStore(auth.DefaultStorePath())
	ensureTOS(store)

	fmt.Printf("Logging in to %s...\n", name)

	err = auth.LoginPKCE(name, store, auth.LoginCallbacks{
		OnPrompt: func(authorizeURL string, browserOpened bool) {
			if browserOpened {
				fmt.Println("\nOpening your browser to authorize...")
			}
			fmt.Println("\nIf no browser opened (e.g. on a remote/headless server), open this URL manually:")
			fmt.Println("\n  " + authorizeURL)
			fmt.Println("\nAfter you authorize, the browser is redirected to a localhost URL.")
			fmt.Println("On this machine that completes login automatically. On a headless server")
			fmt.Println("the page won't load — copy the FULL redirect URL from the address bar and")
			fmt.Print("paste it here (or just wait if the browser is on this machine): ")
		},
		OnManualURL: func(authorizeURL string) string {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				return strings.TrimSpace(scanner.Text())
			}
			return ""
		},
		OnSuccess: func(provider string) {
			cred, _ := store.Get(provider)
			if cred != nil {
				fmt.Printf("\nLogged in to %s. Token: %s\n", provider, cred.String())
			}
		},
	})
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	return nil
}

func runAuthImport(cmd *cobra.Command, args []string) error {
	providerFlag, _ := cmd.Flags().GetString("provider")

	name, err := auth.ResolveProviderName(providerFlag)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	store := auth.NewStore(auth.DefaultStorePath())
	ensureTOS(store)

	fmt.Printf("Importing credentials for %s...\n", name)

	if err := auth.ImportFromCLI(name, store); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	cred, _ := store.Get(name)
	if cred != nil {
		fmt.Printf("Imported credentials for %s. Token: %s (source: %s)\n", name, cred.String(), cred.Source)
	}

	return nil
}
