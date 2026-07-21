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
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	calendarpkg "github.com/douglasdemoura/chroncal/internal/calendar"
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
	cmd.AddCommand(
		accountAddCmd(),
		accountGetCmd(),
		accountListCmd(),
		accountUpdateCmd(),
		accountCalendarsCmd(),
		accountRemoveCmd(),
	)
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
		Short: "Add a CalDAV account and sync all calendars",
		Long: `Authenticate a CalDAV account, discover every calendar collection it
exposes, import every usable collection, and complete their initial sync.`,
		Args: exactOneArg,
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
			rollback := func(cause error) error {
				if cleanupErr := a.Accounts.Delete(ctx, created.ID, store); cleanupErr != nil {
					return errors.Join(cause, fmt.Errorf("remove incomplete account: %w", cleanupErr))
				}
				return cause
			}
			discovery, err := a.Accounts.Discover(ctx, created.ID, store)
			if err != nil {
				return rollback(fmt.Errorf("discover account calendars: %w", err))
			}
			selected := addableCalendarPaths(discovery)
			if len(selected) == 0 {
				return rollback(errInvalidInputf(
					"account %q exposes no usable event, todo, or journal calendars",
					created.DisplayName,
				))
			}
			imported, err := a.Accounts.Import(ctx, discovery, selected)
			if err != nil {
				return rollback(fmt.Errorf("import account calendars: %w", err))
			}
			if err := refreshDiscoveryImportState(ctx, a, &discovery); err != nil {
				return err
			}
			if err := syncNewCalendars(ctx, a, store, imported.CreatedIDs); err != nil {
				return fmt.Errorf(
					"account %q and %d calendar(s) were added, but initial sync failed: %w",
					created.DisplayName, len(imported.CreatedIDs), err,
				)
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONDiscovery(discovery, imported))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Account %q added with %d calendar(s); initial sync complete.\n",
				textsafe.Display(created.DisplayName), len(imported.CreatedIDs))
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

func accountGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Get one CalDAV account",
		Long:  `Show one account's non-secret connection identity and display name.`,
		Example: `  chroncal account get 3
  chroncal account get "Personal Google" --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			configured, err := resolveAccount(context.Background(), a.Accounts, args[0])
			if err != nil {
				return err
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONAccount(configured))
			}
			printAccount(cmd.OutOrStdout(), configured)
			return nil
		},
	}
}

func accountUpdateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "update <name|id>",
		Short: "Update a CalDAV account",
		Long: `Update account-scoped metadata without changing the server, login,
authentication type, or stored credential.`,
		Example: `  chroncal account update 3 --name "Personal Google"
  chroncal account update Google --name Home --output json`,
		Args: exactOneArg,
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
			updated, err := a.Accounts.Rename(ctx, configured.ID, name)
			if err != nil {
				return err
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONAccount(updated))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed account %q to %q.\n",
				textsafe.Display(configured.DisplayName), textsafe.Display(updated.DisplayName))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new account display name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func accountCalendarsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "calendars",
		Short: "List or change an account's selected calendars",
		Long: `Manage the local calendar collections selected from one CalDAV
account. Removing a collection deletes Chroncal's local copy; it never deletes
the remote calendar.`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		accountCalendarsListCmd(),
		accountCalendarsAddCmd(),
		accountCalendarsSetCmd(),
	)
	return cmd
}

func accountCalendarsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <account-name|id>",
		Short: "List every calendar exposed by an account",
		Long:  `Discover the complete remote inventory and show which calendars are already selected.`,
		Example: `  chroncal account calendars list "Personal Google"
  chroncal account calendars list 3 --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			discovery, _, err := loadAccountDiscovery(context.Background(), a, args[0])
			if err != nil {
				return err
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONDiscovery(discovery, account.ImportResult{}))
			}
			printAccountDiscovery(cmd.OutOrStdout(), discovery)
			return nil
		},
	}
}

func accountCalendarsAddCmd() *cobra.Command {
	var (
		calendarRefs []string
		addAll       bool
	)
	cmd := &cobra.Command{
		Use:   "add <account-name|id>",
		Short: "Add calendars to an account's selection",
		Long: `Add one or more discovered calendars without changing calendars already
selected for the account. Use --calendar repeatedly, or use --all. Newly added
calendars are synced before the command returns.`,
		Example: `  chroncal account calendars add "Personal Google" --calendar Family
  chroncal account calendars add 3 --calendar Family --calendar "Holidays in Brazil"
  chroncal account calendars add 3 --all --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(calendarRefs) == 0) == !addAll {
				return errInvalidInputf("choose exactly one selection mode: --calendar or --all")
			}
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()
			discovery, store, err := loadAccountDiscovery(ctx, a, args[0])
			if err != nil {
				return err
			}
			selected := addableCalendarPaths(discovery)
			if !addAll {
				selected, err = resolveDiscoveredSelections(discovery, calendarRefs)
				if err != nil {
					return err
				}
			}
			imported, err := a.Accounts.Import(ctx, discovery, selected)
			if err != nil {
				return err
			}
			if err := refreshDiscoveryImportState(ctx, a, &discovery); err != nil {
				return err
			}
			if err := syncNewCalendars(ctx, a, store, imported.CreatedIDs); err != nil {
				return err
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), toJSONDiscovery(discovery, imported))
			}
			printAccountDiscovery(cmd.OutOrStdout(), discovery)
			fmt.Fprintf(cmd.OutOrStdout(), "Added and synced %d calendars; %d were already selected.\n",
				len(imported.CreatedIDs), len(imported.ExistingIDs))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&calendarRefs, "calendar", nil, "calendar name or remote path to add (repeatable)")
	cmd.Flags().BoolVar(&addAll, "all", false, "add every usable discovered calendar")
	return cmd
}

func accountCalendarsSetCmd() *cobra.Command {
	var (
		calendarRefs []string
		selectAll    bool
		selectNone   bool
		defaultRef   string
	)
	cmd := &cobra.Command{
		Use:   "set <account-name|id>",
		Short: "Replace an account's selected calendar set",
		Long: `Make the account's Chroncal calendars exactly match the requested set.
Deselected calendars and their downloaded data are deleted locally. Remote
calendars are never deleted. If the current default is removed, --default must
identify a retained local calendar or a newly selected remote calendar. Newly
selected calendars are synced before the command returns.`,
		Example: `  chroncal account calendars set "Personal Google" --calendar Family --yes
  chroncal account calendars set 3 --all
  chroncal account calendars set 3 --none --default Local --yes
  chroncal account calendars set 3 --calendar Family --default Family --yes --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			modeCount := 0
			if len(calendarRefs) > 0 {
				modeCount++
			}
			if selectAll {
				modeCount++
			}
			if selectNone {
				modeCount++
			}
			if modeCount != 1 {
				return errInvalidInputf("choose exactly one selection mode: --calendar, --all, or --none")
			}
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()
			discovery, store, err := loadAccountDiscovery(ctx, a, args[0])
			if err != nil {
				return err
			}
			selected := make([]string, 0)
			switch {
			case selectAll:
				selected = selectableCalendarPaths(discovery)
			case len(calendarRefs) > 0:
				selected, err = resolveDiscoveredSelections(discovery, calendarRefs)
				if err != nil {
					return err
				}
			}
			params := account.SelectionParams{SelectedPaths: selected}
			if strings.TrimSpace(defaultRef) != "" {
				params.NewDefaultID, params.NewDefaultPath, err =
					resolveAccountCalendarDefault(ctx, a, discovery, selected, defaultRef)
				if err != nil {
					return err
				}
			}
			removedNames := deselectedCalendarNames(discovery, selected)
			if len(removedNames) > 0 {
				question := fmt.Sprintf(
					"Remove %d downloaded calendar(s) from Chroncal (%s)? Remote calendars will not be deleted.",
					len(removedNames), textsafe.Display(strings.Join(removedNames, ", ")),
				)
				if len(selected) == 0 {
					question += " The empty account and its stored credential will also be removed."
				}
				if err := confirmDestructive(cmd, question); err != nil {
					return err
				}
			}
			result, err := a.Accounts.ReconcileSelection(ctx, discovery, params, store)
			if err != nil {
				switch {
				case errors.Is(err, calendarpkg.ErrDefaultCalendarRequiresPromotion):
					return errInvalidInputf("%v; choose a replacement with --default", err)
				case errors.Is(err, calendarpkg.ErrInvalidPromotionTarget),
					errors.Is(err, calendarpkg.ErrLastCalendar):
					return errInvalidInputf("%v", err)
				default:
					return err
				}
			}
			if err := syncNewCalendars(ctx, a, store, result.CreatedIDs); err != nil {
				return err
			}
			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), jsonAccountCalendarSelection{
					AccountID: discovery.Account.ID, SelectedPaths: selected,
					CreatedIDs:     append([]int64{}, result.CreatedIDs...),
					RemovedIDs:     append([]int64{}, result.RemovedIDs...),
					SyncedIDs:      append([]int64{}, result.CreatedIDs...),
					AccountRemoved: result.AccountRemoved,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated calendars for %q: added %d, removed %d.\n",
				textsafe.Display(discovery.Account.DisplayName),
				len(result.CreatedIDs), len(result.RemovedIDs))
			if len(result.CreatedIDs) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Initial sync completed for %d new calendar(s).\n", len(result.CreatedIDs))
			}
			if result.AccountRemoved {
				fmt.Fprintln(cmd.OutOrStdout(), "No calendars remain; the account and stored credential were removed.")
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&calendarRefs, "calendar", nil, "calendar name or remote path to keep (repeatable)")
	cmd.Flags().BoolVar(&selectAll, "all", false, "keep every usable discovered calendar")
	cmd.Flags().BoolVar(&selectNone, "none", false, "remove every selected calendar")
	cmd.Flags().StringVar(&defaultRef, "default", "", "replacement default calendar ID, name, or selected remote path")
	addConfirmFlag(cmd)
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

func loadAccountDiscovery(
	ctx context.Context,
	a *app.App,
	accountRef string,
) (account.Discovery, auth.CredentialStore, error) {
	configured, err := resolveAccount(ctx, a.Accounts, accountRef)
	if err != nil {
		return account.Discovery{}, nil, err
	}
	store, err := newCalendarCredentialStore(
		a.CredentialNamespace,
		a.PreviousCredentialNamespaces,
		a.MigrateLegacyCredentials,
		a.AllowPlaintext,
	)
	if err != nil {
		return account.Discovery{}, nil, fmt.Errorf("credential store: %w", err)
	}
	discovery, err := a.Accounts.Discover(ctx, configured.ID, store)
	if err != nil {
		return account.Discovery{}, nil, err
	}
	return discovery, store, nil
}

func refreshDiscoveryImportState(
	ctx context.Context,
	a *app.App,
	discovery *account.Discovery,
) error {
	calendars, err := a.Calendars.List(ctx)
	if err != nil {
		return fmt.Errorf("refresh imported calendars: %w", err)
	}
	importedByPath := make(map[string]int64, len(calendars))
	for _, item := range calendars {
		if item.AccountID == discovery.Account.ID {
			importedByPath[item.RemoteURL] = item.ID
		}
	}
	for i := range discovery.Calendars {
		if id, imported := importedByPath[discovery.Calendars[i].Path]; imported {
			discovery.Calendars[i].Imported = true
			discovery.Calendars[i].CalendarID = id
		}
	}
	return nil
}

func addableCalendarPaths(discovery account.Discovery) []string {
	selected := make([]string, 0, len(discovery.Calendars))
	for _, remote := range discovery.Calendars {
		if remote.Importable && !remote.Missing {
			selected = append(selected, remote.Path)
		}
	}
	return selected
}

func selectableCalendarPaths(discovery account.Discovery) []string {
	selected := make([]string, 0, len(discovery.Calendars))
	for _, remote := range discovery.Calendars {
		if remote.Imported || (remote.Importable && !remote.Missing) {
			selected = append(selected, remote.Path)
		}
	}
	return selected
}

func deselectedCalendarNames(discovery account.Discovery, selectedPaths []string) []string {
	selected := make(map[string]struct{}, len(selectedPaths))
	for _, path := range selectedPaths {
		selected[path] = struct{}{}
	}
	var names []string
	for _, remote := range discovery.Calendars {
		if !remote.Imported {
			continue
		}
		if _, keep := selected[remote.Path]; !keep {
			names = append(names, remote.Name)
		}
	}
	return names
}

func resolveAccountCalendarDefault(
	ctx context.Context,
	a *app.App,
	discovery account.Discovery,
	selectedPaths []string,
	ref string,
) (int64, string, error) {
	if _, err := strconv.ParseInt(ref, 10, 64); err == nil {
		id, err := resolveCalendarID(ctx, a, ref)
		return id, "", err
	}
	selected := make(map[string]struct{}, len(selectedPaths))
	for _, path := range selectedPaths {
		selected[path] = struct{}{}
	}
	var matches []account.DiscoveredCalendar
	for _, remote := range discovery.Calendars {
		if _, ok := selected[remote.Path]; !ok {
			continue
		}
		if remote.Path == ref || strings.EqualFold(remote.Name, ref) {
			matches = append(matches, remote)
		}
	}
	switch len(matches) {
	case 0:
		id, err := resolveCalendarID(ctx, a, ref)
		return id, "", err
	case 1:
		return 0, matches[0].Path, nil
	default:
		return 0, "", errInvalidInputf(
			"selected calendar name %q is ambiguous; use its remote path instead", ref,
		)
	}
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
	seen := make(map[string]struct{}, len(refs))
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
			if !matches[0].Imported && (!matches[0].Importable || matches[0].Missing) {
				return nil, errInvalidInputf("calendar %q cannot be added", matches[0].Name)
			}
			if _, duplicate := seen[matches[0].Path]; duplicate {
				continue
			}
			seen[matches[0].Path] = struct{}{}
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

func printAccount(w interface{ Write([]byte) (int, error) }, item account.Account) {
	_, _ = fmt.Fprintf(w, "ID: %d\n", item.ID)
	_, _ = fmt.Fprintf(w, "Name: %s\n", textsafe.Display(item.DisplayName))
	_, _ = fmt.Fprintf(w, "Server: %s\n", textsafe.Display(item.ServerURL))
	_, _ = fmt.Fprintf(w, "Username: %s\n", textsafe.Display(item.Username))
	_, _ = fmt.Fprintf(w, "Authentication: %s\n", item.AuthType)
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
	Missing     bool     `json:"missing"`
}

type jsonDiscovery struct {
	Account     jsonAccount              `json:"account"`
	Calendars   []jsonDiscoveredCalendar `json:"calendars"`
	CreatedIDs  []int64                  `json:"created_ids,omitempty"`
	ExistingIDs []int64                  `json:"existing_ids,omitempty"`
	SyncedIDs   []int64                  `json:"synced_ids,omitempty"`
}

type jsonAccountCalendarSelection struct {
	AccountID      int64    `json:"account_id"`
	SelectedPaths  []string `json:"selected_paths"`
	CreatedIDs     []int64  `json:"created_ids"`
	RemovedIDs     []int64  `json:"removed_ids"`
	SyncedIDs      []int64  `json:"synced_ids"`
	AccountRemoved bool     `json:"account_removed"`
}

func toJSONDiscovery(discovery account.Discovery, imported account.ImportResult) jsonDiscovery {
	rows := make([]jsonDiscoveredCalendar, len(discovery.Calendars))
	for i, remote := range discovery.Calendars {
		rows[i] = jsonDiscoveredCalendar{
			Path: remote.Path, Name: remote.Name, Description: remote.Description,
			Color: remote.Color, Access: string(remote.Access), Components: remote.SupportedComponentSet,
			Imported: remote.Imported, Importable: remote.Importable, CalendarID: remote.CalendarID,
			Missing: remote.Missing,
		}
	}
	return jsonDiscovery{Account: toJSONAccount(discovery.Account), Calendars: rows,
		CreatedIDs: imported.CreatedIDs, ExistingIDs: imported.ExistingIDs,
		SyncedIDs: imported.CreatedIDs}
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
