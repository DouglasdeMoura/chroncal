package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

func accountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage CalDAV accounts and calendars",
		Long: `Add a CalDAV account once, discover every calendar collection it
exposes, and select the calendars to keep in chroncal. All calendars under an
account share one stored credential.`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(accountAddCmd(), accountListCmd(), accountDiscoverCmd(), accountRemoveCmd())
	return cmd
}

func accountAddCmd() *cobra.Command {
	var (
		serverURL     string
		username      string
		authType      string
		oauthClientID string
		allowInsecure bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add and authenticate a CalDAV account",
		Args:  exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			store, err := newCalendarCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
			if err != nil {
				return fmt.Errorf("credential store: %w", err)
			}
			cred, err := buildCalendarCredential(ctx, calendarRemoteFlags{
				Username: username, AuthType: authType, OAuthClientID: oauthClientID,
			})
			if err != nil {
				return err
			}
			created, err := a.Accounts.Create(ctx, account.CreateParams{
				Name: args[0], ServerURL: serverURL, Username: username,
				AuthType: authType, AllowInsecure: allowInsecure,
			}, cred, store)
			if err != nil {
				return err
			}

			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONAccount(created))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Account %q added (ID: %d). Run `chroncal account discover %d` to choose calendars.\n",
				textsafe.Display(created.DisplayName), created.ID, created.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "CalDAV discovery endpoint (required)")
	cmd.Flags().StringVar(&username, "username", "", "username or account email (required)")
	cmd.Flags().StringVar(&authType, "auth", "basic", "authentication type: basic, bearer, oauth2")
	cmd.Flags().StringVar(&oauthClientID, "oauth-client-id", "", "Google OAuth desktop client ID")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure", false, "allow an HTTP endpoint for local development")
	_ = cmd.MarkFlagRequired("server")
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

func accountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured CalDAV accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			accounts, err := a.Accounts.List(context.Background())
			if err != nil {
				return fmt.Errorf("list accounts: %w", err)
			}
			rows := make([]jsonAccount, len(accounts))
			for i, item := range accounts {
				rows[i] = toJSONAccount(item)
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), rows)
			}
			if len(accounts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No CalDAV accounts configured.")
				return nil
			}
			for _, item := range accounts {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%s\n", item.ID,
					textsafe.Display(item.DisplayName), textsafe.Display(item.Username), item.AuthType)
			}
			return nil
		},
	}
}

func accountDiscoverCmd() *cobra.Command {
	var (
		selectCalendars []string
		importAll       bool
	)
	cmd := &cobra.Command{
		Use:   "discover <name|id>",
		Short: "Find and optionally import account calendars",
		Args:  exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if importAll && len(selectCalendars) > 0 {
				return errInvalidInputf("--all and --select are mutually exclusive")
			}
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()
			configured, err := resolveAccount(ctx, a.Accounts, args[0])
			if err != nil {
				return err
			}
			store, err := newCalendarCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
			if err != nil {
				return fmt.Errorf("credential store: %w", err)
			}
			discovery, err := a.Accounts.Discover(ctx, configured.ID, store)
			if err != nil {
				return err
			}

			selected := []string(nil)
			switch {
			case importAll:
				for _, remote := range discovery.Calendars {
					if remote.Importable {
						selected = append(selected, remote.Path)
					}
				}
			case len(selectCalendars) > 0:
				selected, err = resolveDiscoveredSelections(discovery, selectCalendars)
				if err != nil {
					return err
				}
			}

			var imported account.ImportResult
			if selected != nil {
				imported, err = a.Accounts.Import(ctx, discovery, selected)
				if err != nil {
					return err
				}
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONDiscovery(discovery, imported))
			}
			printAccountDiscovery(cmd.OutOrStdout(), discovery)
			if selected != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Imported %d calendars; %d were already linked.\n",
					len(imported.CreatedIDs), len(imported.ExistingIDs))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&selectCalendars, "select", nil, "calendar name or remote path to import (repeatable)")
	cmd.Flags().BoolVar(&importAll, "all", false, "import every usable discovered calendar")
	return cmd
}

func accountRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name|id>",
		Short: "Remove an account while keeping downloaded calendars local",
		Args:  exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()
			configured, err := resolveAccount(ctx, a.Accounts, args[0])
			if err != nil {
				return err
			}
			question := fmt.Sprintf(
				"Remove CalDAV account %q? Downloaded calendars will be kept locally, but remote links and stored credentials will be removed.",
				textsafe.Display(configured.DisplayName),
			)
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}
			store, err := newCalendarCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
			if err != nil {
				return fmt.Errorf("credential store: %w", err)
			}
			if err := a.Accounts.Delete(ctx, configured.ID, store); err != nil {
				return err
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), map[string]any{
					"account_id": configured.ID,
					"removed":    true,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed account %q; downloaded calendars are now local.\n",
				textsafe.Display(configured.DisplayName))
			return nil
		},
	}
	addConfirmFlag(cmd)
	return cmd
}

func resolveAccount(ctx context.Context, service *account.Service, ref string) (account.Account, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		item, err := service.Get(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return account.Account{}, notFoundErr(err, "account", ref)
			}
			return account.Account{}, err
		}
		return item, nil
	}
	accounts, err := service.List(ctx)
	if err != nil {
		return account.Account{}, err
	}
	var matches []account.Account
	for _, item := range accounts {
		if strings.EqualFold(item.Name, ref) || strings.EqualFold(item.DisplayName, ref) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return account.Account{}, &cliError{Code: "not_found", Msg: fmt.Sprintf("account %q not found", ref)}
	case 1:
		return matches[0], nil
	default:
		return account.Account{}, &cliError{Code: "not_found", Msg: fmt.Sprintf("account name %q is ambiguous; use its numeric ID instead", ref)}
	}
}

func resolveDiscoveredSelections(discovery account.Discovery, refs []string) ([]string, error) {
	selected := make([]string, 0, len(refs))
	for _, ref := range refs {
		var matches []account.DiscoveredCalendar
		for _, remote := range discovery.Calendars {
			if remote.Path == ref || strings.EqualFold(remote.Name, ref) {
				matches = append(matches, remote)
			}
		}
		switch len(matches) {
		case 0:
			return nil, errInvalidInputf("discovered calendar %q not found", ref)
		case 1:
			if !matches[0].Importable {
				return nil, errInvalidInputf("calendar %q has no event, todo, or journal components", matches[0].Name)
			}
			selected = append(selected, matches[0].Path)
		default:
			return nil, errInvalidInputf("calendar name %q is ambiguous; select its remote path instead", ref)
		}
	}
	return selected, nil
}

type jsonAccount struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	ServerURL   string `json:"server_url"`
	AuthType    string `json:"auth_type"`
	Username    string `json:"username"`
}

func toJSONAccount(item account.Account) jsonAccount {
	return jsonAccount{
		ID: item.ID, Name: item.Name, DisplayName: item.DisplayName,
		ServerURL: item.ServerURL, AuthType: item.AuthType, Username: item.Username,
	}
}

type jsonDiscoveredCalendar struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Color       string   `json:"color,omitempty"`
	Access      string   `json:"access"`
	Components  []string `json:"components"`
	Imported    bool     `json:"imported"`
	Importable  bool     `json:"importable"`
	CalendarID  int64    `json:"calendar_id,omitempty"`
}

type jsonDiscovery struct {
	Account     jsonAccount              `json:"account"`
	Calendars   []jsonDiscoveredCalendar `json:"calendars"`
	CreatedIDs  []int64                  `json:"created_ids,omitempty"`
	ExistingIDs []int64                  `json:"existing_ids,omitempty"`
}

func toJSONDiscovery(discovery account.Discovery, imported account.ImportResult) jsonDiscovery {
	rows := make([]jsonDiscoveredCalendar, len(discovery.Calendars))
	for i, remote := range discovery.Calendars {
		rows[i] = jsonDiscoveredCalendar{
			Path: remote.Path, Name: remote.Name, Description: remote.Description,
			Color: remote.Color, Access: string(remote.Access), Components: remote.SupportedComponentSet,
			Imported: remote.Imported, Importable: remote.Importable, CalendarID: remote.CalendarID,
		}
	}
	return jsonDiscovery{Account: toJSONAccount(discovery.Account), Calendars: rows,
		CreatedIDs: imported.CreatedIDs, ExistingIDs: imported.ExistingIDs}
}

func printAccountDiscovery(w interface{ Write([]byte) (int, error) }, discovery account.Discovery) {
	_, _ = fmt.Fprintf(w, "Calendars on %s:\n", textsafe.Display(discovery.Account.DisplayName))
	for _, remote := range discovery.Calendars {
		status := "available"
		switch {
		case remote.Imported:
			status = "linked"
		case !remote.Importable:
			status = "unsupported"
		}
		access := string(remote.Access)
		if access == "" {
			access = "unknown"
		}
		_, _ = fmt.Fprintf(w, "  [%s] %s\t%s\t%s\n", status,
			textsafe.Display(remote.Name), access, textsafe.Display(remote.Path))
	}
}
