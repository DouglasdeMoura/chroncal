package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/config"
)

// plaintextRetry holds the request that the secretless credential store
// refused (issue #792). Exactly one request member is set. A confirmed opt-in
// sends the request again, so the user does not type the secret a second
// time.
type plaintextRetry struct {
	discovery   *CalendarDiscoveryRequestedMsg        // Add Account
	credentials *AccountCredentialsUpdateSubmittedMsg // Update Credentials
	// bearer is true when the secret is a token, not a password.
	bearer bool
}

// secretNoun names the secret of the refused request in the prompt.
func (r plaintextRetry) secretNoun() string {
	if r.bearer {
		return "token"
	}
	return "password"
}

// plaintextOptInSavedMsg reports the config write of a confirmed opt-in.
type plaintextOptInSavedMsg struct {
	retry plaintextRetry
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

// offerPlaintextOptIn opens the opt-in prompt over the refused form. The
// prompt is security-relevant, so the confirm button uses the Danger
// variant, and Cancel has the focus.
func (m Model) offerPlaintextOptIn(retry plaintextRetry) Model {
	return m.armConfirm(
		pendingAction{
			kind:   pendingActionPlaintextOptIn,
			target: pendingTarget{plaintext: &retry},
		},
		NewConfirmDialogModel(plaintextOptInMessage(retry.secretNoun()), "Store in Plaintext", m.theme).
			Destructive(),
	)
}

// confirmPlaintextOptIn writes the opt-in to the config file. The write runs
// as a command, because it reads and writes a file. The sync gate stays
// closed until handlePlaintextOptInSaved answers, so the user cannot send the
// form a second time in the meantime.
func (m Model) confirmPlaintextOptIn(retry *plaintextRetry) (tea.Model, tea.Cmd) {
	if retry == nil {
		return m, nil
	}
	saved := *retry
	m.syncing = true
	m.syncStatus = "Turning on plaintext storage…"
	return m, tea.Batch(m.syncSpinner.Tick, func() tea.Msg {
		_, err := config.EnableAllowPlaintext()
		return plaintextOptInSavedMsg{retry: saved, err: err}
	})
}

// handlePlaintextOptInSaved turns on plaintext storage for this session and
// sends the refused request again. openCredentialStore reads
// app.AllowPlaintext each time it opens a store, so the retry uses the
// plaintext store with no restart. A failed write keeps the setting off and
// leaves the user in the form.
func (m Model) handlePlaintextOptInSaved(msg plaintextOptInSavedMsg) (tea.Model, tea.Cmd) {
	m.syncing = false
	if msg.err != nil {
		m.statusToken++
		m.syncStatus = "Couldn’t turn on plaintext storage: " + msg.err.Error()
		return m, tea.Batch(
			m.toast.Failed(msg.err.Error()),
			m.expireStatusAfter(10*time.Second, m.statusToken),
		)
	}
	m.app.AllowPlaintext = true
	switch {
	case msg.retry.discovery != nil:
		return m.handleCalendarDiscoveryRequested(*msg.retry.discovery)
	case msg.retry.credentials != nil:
		return m.handleAccountCredentialsUpdateSubmitted(*msg.retry.credentials)
	default:
		m.syncStatus = ""
		return m, nil
	}
}
