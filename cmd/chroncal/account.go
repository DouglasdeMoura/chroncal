package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func accountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage CalDAV accounts",
	}
	cmd.AddCommand(accountAddCmd(), accountListCmd(), accountRemoveCmd(), accountDiscoverCmd())
	return cmd
}

func accountAddCmd() *cobra.Command {
	var (
		serverURL      string
		authType       string
		username       string
		allowPlaintext bool
		allowInsecure  bool
		oauthClientID  string
		oauthSecret    string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a CalDAV account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Validate HTTPS
			if !strings.HasPrefix(serverURL, "https://") {
				if !allowInsecure {
					return fmt.Errorf("server URL must use HTTPS; use --allow-insecure for HTTP (e.g., local development)")
				}
				if !strings.HasPrefix(serverURL, "http://") {
					return fmt.Errorf("server URL must start with http:// or https://")
				}
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			// Create account in DB
			account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
				Name:      name,
				ServerUrl: serverURL,
				AuthType:  authType,
				Username:  username,
			})
			if err != nil {
				return fmt.Errorf("create account: %w", err)
			}

			// Store credentials
			credStore, err := auth.NewCredentialStore(allowPlaintext)
			if err != nil {
				return fmt.Errorf("credential store: %w", err)
			}

			cred := auth.Credential{
				AccountID: account.ID,
				Username:  username,
			}

			if authType == "oauth2" {
				if oauthClientID == "" || oauthSecret == "" {
					return fmt.Errorf("--oauth-client-id and --oauth-client-secret are required for OAuth 2.0")
				}
				result, err := auth.GoogleOAuthFlow(ctx, oauthClientID, oauthSecret)
				if err != nil {
					// Clean up account on auth failure
					_ = a.Queries.DeleteAccount(ctx, account.ID)
					return fmt.Errorf("OAuth flow: %w", err)
				}
				cred.AccessToken = result.AccessToken
				cred.RefreshToken = result.RefreshToken
				cred.TokenExpiry = result.Expiry.Format("2006-01-02T15:04:05Z07:00")
				cred.OAuthClientID = oauthClientID
				cred.OAuthClientSecret = oauthSecret
			} else if authType == "basic" {
				fmt.Print("Password: ")
				passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println() // newline after hidden input
				if err != nil {
					_ = a.Queries.DeleteAccount(ctx, account.ID)
					return fmt.Errorf("read password: %w", err)
				}
				cred.Password = string(passwordBytes)
			}

			if err := credStore.Set(cred); err != nil {
				return fmt.Errorf("store credentials: %w", err)
			}

			fmt.Printf("Account %q added (ID: %d)\n", name, account.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "CalDAV server URL (required)")
	cmd.Flags().StringVar(&authType, "auth", "basic", "Auth type: basic, oauth2, bearer")
	cmd.Flags().StringVar(&username, "username", "", "Username for authentication")
	cmd.Flags().BoolVar(&allowPlaintext, "allow-plaintext", false, "Allow plaintext credential storage")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure", false, "Allow HTTP (non-HTTPS) server URLs")
	cmd.Flags().StringVar(&oauthClientID, "oauth-client-id", "", "OAuth 2.0 client ID")
	cmd.Flags().StringVar(&oauthSecret, "oauth-client-secret", "", "OAuth 2.0 client secret")
	cmd.MarkFlagRequired("server")
	return cmd
}

func accountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			accounts, err := a.Queries.ListAccounts(context.Background())
			if err != nil {
				return err
			}

			if len(accounts) == 0 {
				fmt.Println("No accounts configured.")
				return nil
			}

			for _, acc := range accounts {
				fmt.Printf("  %d  %-20s  %s (%s)\n", acc.ID, acc.Name, acc.ServerUrl, acc.AuthType)
			}
			return nil
		},
	}
}

func accountRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name|id>",
		Short: "Remove an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			account, err := resolveAccount(ctx, a.Queries, args[0])
			if err != nil {
				return err
			}

			if err := a.Queries.DeleteAccount(ctx, account.ID); err != nil {
				return fmt.Errorf("delete account: %w", err)
			}

			// Best-effort credential deletion
			credStore, _ := auth.NewCredentialStore(true)
			if credStore != nil {
				_ = credStore.Delete(account.ID)
			}

			fmt.Printf("Account %q removed\n", account.Name)
			return nil
		},
	}
}

func accountDiscoverCmd() *cobra.Command {
	var allowPlaintext bool
	cmd := &cobra.Command{
		Use:   "discover <name|id>",
		Short: "Discover remote calendars on an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			account, err := resolveAccount(ctx, a.Queries, args[0])
			if err != nil {
				return err
			}

			credStore, err := auth.NewCredentialStore(allowPlaintext)
			if err != nil {
				return err
			}

			cred, err := credStore.Get(account.ID)
			if err != nil {
				return fmt.Errorf("get credentials: %w", err)
			}

			client, err := caldav.NewClientFromCredential(account.ServerUrl, cred)
			if err != nil {
				return fmt.Errorf("connect: %w", err)
			}

			calendars, err := client.DiscoverCalendars(ctx)
			if err != nil {
				return fmt.Errorf("discover: %w", err)
			}

			if len(calendars) == 0 {
				fmt.Println("No calendars found on server.")
				return nil
			}

			fmt.Printf("Found %d calendar(s) on %s:\n\n", len(calendars), account.Name)
			for i, cal := range calendars {
				components := "none"
				if len(cal.SupportedComponentSet) > 0 {
					components = strings.Join(cal.SupportedComponentSet, ", ")
				}
				fmt.Printf("  %d. %s\n     Path: %s\n     Components: %s\n",
					i+1, cal.Name, cal.Path, components)
				if cal.Description != "" {
					fmt.Printf("     Description: %s\n", cal.Description)
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowPlaintext, "allow-plaintext", false, "Allow plaintext credential storage")
	return cmd
}

func resolveAccount(ctx context.Context, q *storage.Queries, ref string) (storage.Account, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return q.GetAccount(ctx, id)
	}
	account, err := q.GetAccountByName(ctx, ref)
	if err != nil {
		return storage.Account{}, fmt.Errorf("account %q not found", ref)
	}
	return account, nil
}

