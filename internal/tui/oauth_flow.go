package tui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/auth"
)

// OAuthFlowState tracks the pending-authorization modal's lifecycle. There
// is no separate "exchanging" state: with the Start/Wait split the code
// exchange happens inside Wait, invisible to the UI, and the waiting copy
// covers it.
type OAuthFlowState int

const (
	// OAuthFlowIdle is the zero value; the modal is not in use.
	OAuthFlowIdle OAuthFlowState = iota
	// OAuthFlowStarting covers listener bind + browser launch.
	OAuthFlowStarting
	// OAuthFlowWaiting is the long phase: browser open (or URL shown),
	// loopback listener waiting for Google's redirect.
	OAuthFlowWaiting
	// OAuthFlowDone holds the successful result until the parent collects it.
	OAuthFlowDone
	// OAuthFlowFailed is any error path: start failure, redirect error,
	// timeout, or exchange failure. The modal stays up showing the error.
	OAuthFlowFailed
	// OAuthFlowCancelled is the user pressing Esc mid-flow.
	OAuthFlowCancelled
)

// oauthFlowStartedMsg reports StartGoogleOAuthFlow's outcome. A start error
// (loopback port bind, bad client config) lands the modal in Failed without
// ever reaching Waiting.
type oauthFlowStartedMsg struct {
	flow *auth.PendingOAuthFlow
	err  error
}

// oauthFlowDoneMsg reports Wait's outcome: tokens, an error, or ctx.Err()
// after a cancel.
type oauthFlowDoneMsg struct {
	result *auth.GoogleOAuthResult
	err    error
}

// oauthFlowClosedMsg asks the parent to dismiss the modal (Esc on a
// terminal state).
type oauthFlowClosedMsg struct{}

// Test seams: the model never calls the auth package directly so the state
// machine is testable without a real listener or browser.
var (
	oauthStartFn = auth.StartGoogleOAuthFlow
	oauthWaitFn  = func(ctx context.Context, p *auth.PendingOAuthFlow) (*auth.GoogleOAuthResult, error) {
		return p.Wait(ctx)
	}
)

// OAuthFlowModel is the pending modal shown while a Google OAuth
// authorization is in flight. It owns the flow lifecycle: starting the
// loopback flow, surfacing the consent URL, cancellation, and the terminal
// done/failed/cancelled states. What to do with the resulting tokens is the
// parent's business (re-auth vs connect-new).
type OAuthFlowModel struct {
	state         OAuthFlowState
	authURL       string
	browserOpened bool
	errText       string

	// cancel aborts the in-flight flow. Stored on the model — not captured
	// only in the start cmd's closure — because three parties need it: the
	// Start cmd, the Wait cmd, and the Esc handler.
	cancel context.CancelFunc
	// flow is kept so Close() can release the listener in the
	// Esc-between-Start-and-Wait window and on program teardown.
	flow *auth.PendingOAuthFlow

	spinner spinner.Model
	dialog  Dialog
	help    help.Model
	theme   Theme
	width   int
	height  int
}

// NewOAuthFlowModel builds an idle modal. Call Start to launch a flow.
func NewOAuthFlowModel(theme Theme) OAuthFlowModel {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	dialog := NewDialog("Google authorization", DefaultDialogStyles())
	dialog.SetWidth(62)
	return OAuthFlowModel{
		spinner: sp,
		dialog:  dialog,
		help:    newThemedHelp(theme),
		theme:   theme,
	}
}

// Start launches the OAuth flow with the given client config and moves the
// modal to Starting. The returned cmd performs the (blocking) listener bind
// and browser launch off the update loop.
func (m OAuthFlowModel) Start(clientID, clientSecret string) (OAuthFlowModel, tea.Cmd) {
	// Defense-in-depth: never strand a prior flow's Wait goroutine or its
	// bound loopback listener. If Start is somehow called while one is live
	// (re-entrancy the app-level guard should already prevent), abort it
	// first — the dropped cancel func + *PendingOAuthFlow would otherwise be
	// unreachable.
	m.Abort()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.state = OAuthFlowStarting
	m.errText = ""
	m.authURL = ""
	m.flow = nil
	startCmd := func() tea.Msg {
		flow, err := oauthStartFn(ctx, clientID, clientSecret)
		return oauthFlowStartedMsg{flow: flow, err: err}
	}
	waitTick := m.spinner.Tick
	return m, tea.Batch(startCmd, waitTick)
}

// Abort cancels any in-flight flow and releases its listener. Safe to call
// in every state; used by the parent on teardown.
func (m OAuthFlowModel) Abort() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.flow != nil {
		m.flow.Close()
	}
}

func (m OAuthFlowModel) State() OAuthFlowState { return m.state }
func (m OAuthFlowModel) AuthURL() string       { return m.authURL }
func (m OAuthFlowModel) Err() string           { return m.errText }

// Active reports whether a flow is in a non-terminal state.
func (m OAuthFlowModel) Active() bool {
	return m.state == OAuthFlowStarting || m.state == OAuthFlowWaiting
}

func (m OAuthFlowModel) SetSize(w, h int) OAuthFlowModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// SetTheme re-themes the modal (used when the app's theme changes mid-run).
func (m OAuthFlowModel) SetTheme(t Theme) OAuthFlowModel {
	m.theme = t
	m.help = newThemedHelp(t)
	return m
}

func (m OAuthFlowModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m OAuthFlowModel) Update(msg tea.Msg) (OAuthFlowModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.SetSize(msg.Width, msg.Height), nil

	case oauthFlowStartedMsg:
		if m.state != OAuthFlowStarting {
			// Stale: the user cancelled before the listener came up.
			if msg.flow != nil {
				msg.flow.Close()
			}
			return m, nil
		}
		if msg.err != nil {
			m.state = OAuthFlowFailed
			m.errText = msg.err.Error()
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		}
		m.flow = msg.flow
		m.authURL = msg.flow.AuthURL
		m.browserOpened = msg.flow.BrowserOpened
		m.state = OAuthFlowWaiting
		flow := msg.flow
		cancelCtx, cancel := context.WithCancel(context.Background())
		// Chain: cancelling the model's ctx must unblock Wait too. Reuse
		// one cancel func for both phases by wrapping.
		prevCancel := m.cancel
		m.cancel = func() {
			if prevCancel != nil {
				prevCancel()
			}
			cancel()
		}
		return m, func() tea.Msg {
			result, err := oauthWaitFn(cancelCtx, flow)
			return oauthFlowDoneMsg{result: result, err: err}
		}

	case oauthFlowDoneMsg:
		if m.state == OAuthFlowCancelled {
			// Late Wait return after Esc — already handled.
			return m, nil
		}
		switch {
		case msg.err == nil:
			m.state = OAuthFlowDone
		case errors.Is(msg.err, context.Canceled):
			m.state = OAuthFlowCancelled
		default:
			m.state = OAuthFlowFailed
			m.errText = msg.err.Error()
		}
		return m, nil

	case spinner.TickMsg:
		if !m.Active() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			if m.Active() {
				m.state = OAuthFlowCancelled
				m.Abort()
				return m, nil
			}
			return m, func() tea.Msg { return oauthFlowClosedMsg{} }
		case key.Matches(msg, key.NewBinding(key.WithKeys("o"))):
			if m.state == OAuthFlowWaiting && m.authURL != "" {
				return m, openURLCmd(m.authURL)
			}
		}

	case tea.MouseClickMsg:
		// Link zones resolve per-dialog (same pattern as the event view):
		// a click on the rendered auth URL opens the browser.
		if msg.Button == tea.MouseLeft {
			bw, bh := m.BoxSize()
			ox := (m.width - bw) / 2
			oy := (m.height - bh) / 2
			target := mouseResolve(msg.X-ox, msg.Y-oy)
			if strings.HasPrefix(target, linkZonePrefix) {
				return m, openURLCmd(strings.TrimPrefix(target, linkZonePrefix))
			}
		}
	}
	return m, nil
}

func (m OAuthFlowModel) View() string {
	dim := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	errStyle := lipgloss.NewStyle().Foreground(m.theme.Error)

	var body string
	switch m.state {
	case OAuthFlowStarting:
		body = m.spinner.View() + " Starting Google authorization…"
	case OAuthFlowWaiting:
		body = m.spinner.View() + " Waiting for Google authorization…\n\n"
		if m.browserOpened {
			body += dim.Render("Browser opened — finish signing in there.")
		} else {
			// Plain selectable text first (SSH / no-mouse terminals copy it
			// straight from the screen), wrapped to the dialog width; the
			// same URL is also a clickable link zone.
			wrapped := lipgloss.NewStyle().Width(m.dialog.ContentWidth()).Render(m.authURL)
			body += dim.Render("Couldn't open a browser. Open this URL to authorize:") +
				"\n\n" + mouseMark(linkZonePrefix+m.authURL, hyperlink(m.authURL, wrapped))
		}
	case OAuthFlowDone:
		body = "✓ Authorized."
	case OAuthFlowFailed:
		body = errStyle.Render("✗ Authorization failed: "+m.errText) + "\n\n" +
			dim.Render("Close and try again.")
	case OAuthFlowCancelled:
		body = dim.Render("Authorization cancelled.")
	default:
		body = ""
	}

	var helpKeys []key.Binding
	if m.Active() {
		helpKeys = append(helpKeys, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")))
		if m.state == OAuthFlowWaiting && !m.browserOpened {
			helpKeys = append(helpKeys, key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open browser")))
		}
	} else {
		helpKeys = append(helpKeys, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")))
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	return mouseSweep(m.dialog.Box(body))
}
