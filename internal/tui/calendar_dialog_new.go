package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// NewCalendarDialogModel builds a dialog for create (params.ID==0) or edit.
func NewCalendarDialogModel(params CalendarDialogParams, theme Theme) CalendarDialogModel {
	title := "New local calendar"
	if params.ID > 0 {
		title = "Edit calendar"
		// Apple's "Get Info" sheet shows the default badge inline with the
		// title — readable at a glance and impossible to confuse with an
		// editable field. We do the same so users opening a calendar via
		// the sidebar's Return keypress see the state immediately.
		if params.IsDefault {
			title += " · Default"
		}
	}
	if params.Color == "" {
		params.Color = "#a6e3a1"
	}

	styles := DefaultDialogStyles()
	dialog := NewDialog(title, styles)
	dialog.SetWidth(62)

	formStyles := DefaultFormStyles()
	formStyles.LabelLayout = LabelInline
	formStyles.ShowFocusMarker = true
	formStyles.ButtonAlign = ButtonAlignRight
	formStyles.ButtonRule = true

	nameField := NewTextField("e.g. Work")
	nameField.SetValue(params.Name)
	nameField.SetCharLimit(256)

	colorField := NewColorField(theme.CalendarSwatches, params.Color, theme.TextDim)

	descField := NewTextField("Shared family schedule")
	descField.SetValue(params.Description)
	descField.SetCharLimit(500)

	emailField := NewTextField("you@example.com")
	emailField.SetValue(params.OwnerEmail)
	emailField.SetCharLimit(256)

	items := []FormItem{
		{Label: "Name", Field: nameField, Required: true},
		{Label: "Color", Field: colorField, Required: true},
		{Label: "Description", Field: descField},
		{Label: "Owner email", Field: emailField},
		{Label: "", Field: NewStaticField("", nil)},
	}

	// The dialog width isn't known until SetSize. Truncate compact account
	// context at render time so long names and errors stay on one row.
	contentWidth := new(int)

	// staticLine builds a one-row static field: truncate to the content
	// width first, then style — slicing after styling would cut through
	// ANSI escapes.
	staticLine := func(text string, style lipgloss.Style) FormItem {
		return FormItem{Label: "", Field: NewStaticField(text, func(s string) string {
			if *contentWidth > 0 {
				s = truncateTo(s, *contentWidth)
			}
			return style.Render(s)
		})}
	}
	// Edit mode surfaces the calendar's visibility and owner context as
	// actionable rows. That is a Display Calendar checkbox (visibility is
	// immediate and never auto-saved with metadata). And either
	// "Location: Local" or an "Account: <name> ›" opener that drills into
	// the owner account. Create mode has no immutable ID, so neither row
	// applies there.
	var (
		visibilityCb  *CheckboxField
		accountOpener *OpenerField
		openerIdx     = -1
	)
	if params.ID > 0 {
		visibility := NewCheckboxField("", !params.Hidden)
		visibility.SetContent("Display calendar")
		visibilityCb = visibility
		items = append(items, FormItem{Label: "", Field: visibility, AlignToFieldColumn: true})

		// Account and Location sit in the shared label column like every
		// other row (Apple Settings detail-row layout); the opener's value
		// carries the drill-in chevron.
		if params.RemoteLinked {
			accountName := strings.TrimSpace(params.AccountName)
			if accountName == "" {
				accountName = "Connected account"
			}
			openerIdx = len(items)
			opener := NewOpenerField(accountName + " ›")
			accountOpener = opener
			items = append(items, FormItem{Label: "Account", Field: opener})
			for _, line := range syncHealthDialogLines(params, theme) {
				items = append(items, staticLine(line.text, line.style))
			}
			// Account calendars carry no Delete button. A delete here would
			// only remove the local copy, not the account's calendar. This
			// footnote explains why and points at the local alternative.
			note := lipgloss.NewStyle().Foreground(theme.TextDim)
			ownership := "This calendar lives in your " + accountName + " account."
			if strings.TrimSpace(params.AccountName) == "" {
				ownership = "This calendar lives in your connected account."
			}
			items = append(items,
				FormItem{Label: "", Field: NewStaticField("", nil)},
				staticLine(ownership, note),
				staticLine("Turn off Display calendar to hide it on this device.", note),
				staticLine("To remove it, open Account › Manage Calendars.", note),
			)
		} else {
			items = append(items, FormItem{Label: "Location", Field: NewStaticField("Local", nil)})
		}
	}

	form := NewForm("Save", formStyles, items...)

	savedID := params.ID
	saveMakeDefault := new(bool)
	form.OnSubmit(func(f *Form) tea.Cmd {
		nameVal := strings.TrimSpace(f.Field(cdIdxName).(*TextField).Value())
		hexVal := strings.TrimSpace(f.Field(cdIdxColor).(*ColorField).Value())
		descVal := strings.TrimSpace(f.Field(cdIdxDescription).(*TextField).Value())
		emailVal := strings.TrimSpace(f.Field(cdIdxEmail).(*TextField).Value())

		msg := CalendarSavedMsg{
			ID:          savedID,
			Name:        nameVal,
			Color:       hexVal,
			Description: descVal,
			OwnerEmail:  emailVal,
			MakeDefault: *saveMakeDefault,
		}
		*saveMakeDefault = false

		return func() tea.Msg { return msg }
	})

	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	localDraft := params

	// form is intentionally NOT set here: the button/handler wiring below
	// mutates the local form, and m.form captures the final state just
	// before return.
	m := CalendarDialogModel{
		id:                params.ID,
		name:              params.Name,
		linked:            params.RemoteLinked,
		dialog:            dialog,
		body:              viewport.New(),
		help:              newThemedHelp(theme),
		theme:             theme,
		accentColor:       theme.Selected,
		mutedColor:        theme.Muted,
		textDimColor:      theme.TextDim,
		saveMakeDefault:   saveMakeDefault,
		accountConnection: false,
		localDraft:        &localDraft,
		contentWidth:      contentWidth,
		hidden:            params.Hidden,
		visibilityCb:      visibilityCb,
		accountOpener:     accountOpener,
	}
	m.body.MouseWheelEnabled = true

	// Edit mode, not yet default: surface "Set as Default" without forcing a
	// trip through the manage-calendars list.
	if params.ID > 0 && !params.IsDefault {
		id := params.ID
		name := params.Name
		form.SetUtilityActionButton("Set as Default", Button, func() tea.Msg {
			return CalendarSetDefaultRequestedMsg{ID: id, Name: name}
		})
	}

	// Remote calendars drill into their owner account via the inline
	// "Account: <name> ›" opener, not a separate button. The opener's
	// Enter then emits the canonical AccountSettingsRequestedMsg the
	// host already routes. The calendar manager intercepts that to push
	// the account detail. It does not disturb the in-progress calendar
	// draft.
	if openerIdx >= 0 {
		accountID := params.AccountID
		capturedIdx := openerIdx
		form.OnFieldEnter(func(f *Form, field int) tea.Cmd {
			if field != capturedIdx {
				return nil
			}
			return func() tea.Msg { return AccountSettingsRequestedMsg{AccountID: accountID} }
		})
	}

	// Edit mode exposes Delete as a leading action that targets the
	// calendar's immutable ID. It is destructive (ButtonDanger) and only
	// requests removal. The host owns the safe-confirm flow. Account
	// calendars get no Delete. The button could only drop the local copy
	// while the account still owns the calendar. The form explains that
	// in a footnote instead (hide locally via Display calendar; manage
	// membership in Account ▸ Manage Calendars). Export is a manager-only
	// affordance (the legacy app has no export handler yet). It is gated
	// on ManagerEmbedded to avoid a no-op button in legacy-wired dialogs.
	// Apple sheet disposition: Set as Default and Export live on the quiet
	// utility tier above the commit row. Delete — destructive — sits in
	// the commit row's bottom-left corner, as far as possible from Save.
	if params.ID > 0 {
		id := params.ID
		name := params.Name
		if params.ManagerEmbedded {
			form.SetUtilityActionButton("Export Calendar…", Button, func() tea.Msg {
				return CalendarExportRequestedMsg{ID: id, Name: name}
			})
			if !params.RemoteLinked {
				form.SetUtilityActionButton("Move to Account…", Button, func() tea.Msg {
					return CalendarMoveToAccountRequestedMsg{ID: id, Name: name}
				})
			}
		}
		if params.RemoteLinked {
			// The keep-local counterpart to Delete: unlink from the account
			// but keep every downloaded event as a local calendar.
			form.SetUtilityActionButton("Keep as Local Calendar…", Button, func() tea.Msg {
				return CalendarKeepLocalRequestedMsg{ID: id, Name: name}
			})
		} else {
			form.SetLeadingActionButton("Delete Calendar…", ButtonDanger, func() tea.Msg {
				return CalendarDeleteRequestedMsg{ID: id, Name: name}
			})
		}
	}

	// Create mode with at least one calendar already on disk: offer to
	// promote the new row to default in one save. Do not force a
	// follow-up trip through the list dialog. Suppressed for the very
	// first calendar. The service auto-promotes that row in silence.
	if params.ID == 0 && params.OfferDefault {
		form.SetLeadingActionButton("Save and Set as Default", Button, func() tea.Msg {
			return calendarSavePromotePressedMsg{}
		})
	}

	m.form = form
	return m
}

// NewAccountDialogModel opens account sign-in directly. Remote collection
// discovery is an account concern; New Local Calendar remains a local-only flow.
func NewAccountDialogModel(theme Theme) CalendarDialogModel {
	dialog := NewDialog("Add Account", DefaultDialogStyles())
	dialog.SetWidth(62)
	form := newCalDAVConnectionForm(theme, "")
	m := CalendarDialogModel{
		dialog:            dialog,
		form:              form,
		body:              viewport.New(),
		help:              newThemedHelp(theme),
		theme:             theme,
		accentColor:       theme.Selected,
		mutedColor:        theme.Muted,
		textDimColor:      theme.TextDim,
		accountConnection: true,
	}
	m.body.MouseWheelEnabled = true
	return m
}

func newCalDAVConnectionForm(theme Theme, usernamePrefill string) Form {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	insecure := NewCheckboxField("", false)
	insecure.SetContent("allow plain HTTP")
	form := NewForm("Sign In", styles,
		FormItem{Label: "Server URL", Field: newRemoteURLField(""), Required: true},
		FormItem{Label: "Username", Field: newUsernameField(usernamePrefill), Required: true},
		FormItem{Label: "Auth", Field: newAuthField("basic"), Required: true},
		FormItem{Label: "Password", Field: newPasswordField(), Required: true},
		FormItem{Label: "HTTP", Field: insecure},
	)
	form.SetActionButton("Test", Button, func() tea.Msg {
		return testConnectionPressedMsg{}
	})
	form.OnCancel(func(*Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	var snapshot struct {
		secret, clientID, clientSecret string
		allowInsecure                  bool
	}
	oauthLayout := new(bool)
	snapshotTail := func(f *Form) {
		if *oauthLayout {
			snapshot.clientID = f.Field(calDAVIdxOAuthClientID).(*TextField).Value()
			snapshot.clientSecret = f.Field(calDAVIdxOAuthClientSecret).(*TextField).Value()
			snapshot.allowInsecure = f.Field(calDAVIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			return
		}
		snapshot.secret = f.Field(calDAVIdxSecret).(*TextField).Value()
		snapshot.allowInsecure = f.Field(calDAVIdxAllowInsecure).(*CheckboxField).Checked()
	}
	appendTail := func(f *Form, authType string) {
		allow := NewCheckboxField("", snapshot.allowInsecure)
		allow.SetContent("allow plain HTTP")
		if calendarAuthIsOAuth(authType) {
			clientSecret := newOAuthClientSecretField()
			clientSecret.SetValue(snapshot.clientSecret)
			f.AppendItems(
				FormItem{Label: "Client ID", Field: newOAuthClientIDField(snapshot.clientID), Required: true},
				FormItem{Label: "Client secret", Field: clientSecret, Required: true},
				FormItem{Label: "HTTP", Field: allow},
			)
			*oauthLayout = true
			return
		}
		secret := newPasswordField()
		secret.SetValue(snapshot.secret)
		f.AppendItems(
			FormItem{Label: "Password", Field: secret, Required: true},
			FormItem{Label: "HTTP", Field: allow},
		)
		*oauthLayout = false
	}
	form.OnRebuild(func(f *Form) {
		authType := f.Field(calDAVIdxAuth).(*SelectField).Value()
		if calendarAuthIsOAuth(authType) != *oauthLayout {
			snapshotTail(f)
			f.RemoveItems(calDAVIdxSecret)
			f.ClearError()
			appendTail(f, authType)
		}
		if !*oauthLayout {
			secret := f.Field(calDAVIdxSecret).(*TextField)
			if authType == "bearer" {
				f.SetItemLabel(calDAVIdxSecret, "Token")
				secret.SetPlaceholder("paste your API token")
			} else {
				f.SetItemLabel(calDAVIdxSecret, "Password")
				secret.SetPlaceholder("your password")
			}
		}

		insecureIdx := calDAVIdxAllowInsecure
		if *oauthLayout {
			insecureIdx = calDAVIdxOAuthAllowInsecure
		}
		allow := f.Field(insecureIdx).(*CheckboxField)
		wasAuto := allow.AutoChecked()
		if isLocalhostHTTP(f.Field(calDAVIdxServer).(*TextField).Value()) {
			allow.SetChecked(true)
			allow.SetAutoChecked(true)
			allow.SetSuffix("")
			allow.SetDisabledWhen(func() (bool, string) {
				return true, lipgloss.NewStyle().Foreground(theme.Muted).Italic(true).
					Render("auto-enabled for localhost")
			})
		} else {
			if wasAuto {
				allow.SetChecked(false)
				allow.SetAutoChecked(false)
			}
			allow.SetDisabledWhen(nil)
			if allow.Checked() {
				allow.SetSuffix(lipgloss.NewStyle().Foreground(theme.Error).Render("(unencrypted)"))
			} else {
				allow.SetSuffix("")
			}
		}
	})
	form.OnSubmit(func(f *Form) tea.Cmd {
		msg := CalendarDiscoveryRequestedMsg{
			ServerURL: strings.TrimSpace(f.Field(calDAVIdxServer).(*TextField).Value()),
			Username:  strings.TrimSpace(f.Field(calDAVIdxUsername).(*TextField).Value()),
			AuthType:  f.Field(calDAVIdxAuth).(*SelectField).Value(),
		}
		if msg.ServerURL == "" {
			f.SetError(calDAVIdxServer, "Server URL is required")
			return nil
		}
		if msg.Username == "" {
			f.SetError(calDAVIdxUsername, "Username is required")
			return nil
		}
		if calendarAuthIsOAuth(msg.AuthType) {
			msg.OAuthClientID = strings.TrimSpace(f.Field(calDAVIdxOAuthClientID).(*TextField).Value())
			msg.OAuthClientSecret = strings.TrimSpace(f.Field(calDAVIdxOAuthClientSecret).(*TextField).Value())
			msg.AllowInsecure = f.Field(calDAVIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			if msg.OAuthClientID == "" {
				f.SetError(calDAVIdxOAuthClientID, "Client ID is required")
				return nil
			}
			if msg.OAuthClientSecret == "" {
				f.SetError(calDAVIdxOAuthClientSecret, "Client secret is required")
				return nil
			}
		} else {
			msg.Secret = f.Field(calDAVIdxSecret).(*TextField).Value()
			msg.AllowInsecure = f.Field(calDAVIdxAllowInsecure).(*CheckboxField).Checked()
			if strings.TrimSpace(msg.Secret) == "" {
				f.SetError(calDAVIdxSecret, "Credential is required")
				return nil
			}
		}
		return func() tea.Msg { return msg }
	})
	return form
}
