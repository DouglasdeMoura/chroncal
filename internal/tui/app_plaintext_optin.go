package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/config"
)

// plaintextOptInSavedMsg reports the config write of a confirmed opt-in
// (issue #792). retry is the request that the secretless credential store
// refused. A successful write dispatches it again through Update, so the
// user does not type the secret a second time.
type plaintextOptInSavedMsg struct {
	retry tea.Msg
	err   error
}

// plaintextRefusalFieldError is the short refusal that the Update
// Credentials form shows under the secret row. The status line shows the
// full error with every remedy.
const plaintextRefusalFieldError = "No secure credential store is available"

// plaintextOptInMessage explains the trade-off before chroncal writes the
// opt-in. For a password, the text names the safer choice first: a password
// command keeps the secret out of the file and needs no opt-in. A token has
// no command form, so the text leaves that choice out.
func plaintextOptInMessage(noun string) string {
	text := "No OS keyring is available, so chroncal cannot store this " + noun + " securely.\n\n"
	if noun == "password" {
		text += "A password command needs no opt-in. It keeps the password out of the file.\n\n"
	}
	return text + "Plaintext storage writes the " + noun + " to a file with mode 0600. " +
		"That mode blocks a casual cat. " +
		"It does not block backups, filesystem snapshots, or sync tools that ignore Unix permissions.\n\n" +
		"This sets security.allow_plaintext = true in the config file."
}

// offerPlaintextOptIn opens the opt-in prompt over the refused form. retry
// is the refused request. bearer is true when the secret is a token, not a
// password. The prompt is security-relevant, so the confirm button uses the
// Danger variant, and Cancel has the focus.
//
// An environment variable can override the opt-in (see
// config.CheckAllowPlaintextEnv). A confirm then cannot take effect, so the
// prompt does not open, and the status line gives the remedy. The caller
// owns the status token and the expiry of the status line.
func (m Model) offerPlaintextOptIn(retry tea.Msg, bearer bool) Model {
	if err := config.CheckAllowPlaintextEnv(); err != nil {
		m.syncStatus = "Plaintext storage cannot turn on: " + err.Error()
		return m
	}
	noun := "password"
	if bearer {
		noun = "token"
	}
	return m.armConfirm(
		pendingAction{
			kind:   pendingActionPlaintextOptIn,
			target: pendingTarget{retry: retry},
		},
		NewConfirmDialogModel(plaintextOptInMessage(noun), "Store in Plaintext", m.theme).
			Destructive(),
	)
}

// confirmPlaintextOptIn writes the opt-in to the config file. The write runs
// as a command, because it reads and writes a file. plaintextSaving stays
// true until handlePlaintextOptInSaved answers, so the user cannot send the
// form a second time in the meantime.
func (m Model) confirmPlaintextOptIn(retry tea.Msg) (tea.Model, tea.Cmd) {
	if retry == nil {
		return m, nil
	}
	m.plaintextSaving = true
	m.statusToken++
	m.syncStatus = "Turning on plaintext storage…"
	return m, func() tea.Msg {
		_, err := config.EnableAllowPlaintext()
		return plaintextOptInSavedMsg{retry: retry, err: err}
	}
}

// plaintextRetryDroppedStatus returns the status line for a refused request
// that can no longer run. It returns "" when the request can run. The user
// can close the form while the config write runs. The opt-in is then on, but
// the secret is not saved, and the status line must say so.
func (m Model) plaintextRetryDroppedStatus(retry tea.Msg) string {
	switch r := retry.(type) {
	case AccountCredentialsUpdateSubmittedMsg:
		if m.credentialsUpdateBlocked(r) {
			return "Plaintext storage is on, but the credentials were not saved. Save the credentials again."
		}
	case CalendarDiscoveryRequestedMsg:
		if m.calendarDiscoveryBlocked() || !m.calendarManagerOpen || !m.calendarManager.AddAccountFormOpen() {
			return "Plaintext storage is on, but the account was not added. Add the account again."
		}
	}
	return ""
}

// handlePlaintextOptInSaved turns on plaintext storage for this session and
// dispatches the refused request again. openCredentialStore reads
// allowPlaintext each time it opens a store, so the retry uses the
// plaintext store with no restart. The flag lives on the model, not on the
// shared *app.App, because commands read app.App from other goroutines. A
// failed write keeps the setting off and leaves the user in the form. A
// request that can no longer run gets a status line, because a silent drop
// leaves the secret unsaved.
func (m Model) handlePlaintextOptInSaved(msg plaintextOptInSavedMsg) (tea.Model, tea.Cmd) {
	m.plaintextSaving = false
	if msg.err != nil {
		m.statusToken++
		m.syncStatus = "Couldn’t turn on plaintext storage: " + msg.err.Error()
		return m, tea.Batch(
			m.toast.Failed("Save", msg.err.Error()),
			m.expireStatusAfter(10*time.Second, m.statusToken),
		)
	}
	m.plaintextOptedIn = true
	if status := m.plaintextRetryDroppedStatus(msg.retry); status != "" {
		m.statusToken++
		m.syncStatus = status
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}
	m.syncStatus = ""
	return m.Update(msg.retry)
}
