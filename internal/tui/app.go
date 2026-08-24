package tui

import (
	"os"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
	syncpkg "github.com/douglasdemoura/chroncal/internal/sync"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

type appFocus int

const (
	focusSidebar appFocus = iota
	focusCalendar
)

type viewMode int

const (
	viewMonth viewMode = iota
	viewWeek
	viewDay
	viewAgenda
)

type eventsLoadedMsg struct {
	// from and to identify the query range. The handler can then drop stale
	// responses when the active view's range has moved on. Rapid month
	// navigation in the agenda fires multiple loads. The last to arrive
	// must not overwrite the current window's rows with empty data.
	from time.Time
	to   time.Time
	// merge=true means this is an incremental slice to append to the
	// current m.events (agenda infinite-scroll path). merge=false means
	// replace m.events entirely. That is a full refresh: initial load,
	// cursor jump, view change, or post-mutation reload.
	merge  bool
	events []event.Event
	err    error
}

type calendarsLoadedMsg struct {
	calendars map[int64]CalendarInfo
	accounts  map[int64]account.Account
	err       error
}

type eventRSVPUpdatedMsg struct {
	event event.Event
	err   error
}

type eventCreatedMsg struct {
	calendarID int64
	err        error
}

// calendarMutationDoneMsg reports a calendar mutation's outcome. keepEditor
// marks mutations that happen beside an open edit form (Set as Default).
// The manager then keeps the form — and any unsaved draft — mounted on
// success.
type calendarMutationDoneMsg struct {
	err        error
	keepEditor bool
}

// calendarOrderSavedMsg reports the result of a persist of a sidebar reorder.
// Success is silent (the list already shows the new order). Failure surfaces
// as a toast. ids carries the order that was written. The handler can then
// clear the pendingOrder entries that match without a discard of a newer
// reorder.
type calendarOrderSavedMsg struct {
	ids []int64
	err error
}
type accountOrderSavedMsg struct {
	ids []int64
	err error
}

type calendarDeleteCountMsg struct {
	id         int64
	name       string
	eventCount int64
	// promoteID and promoteName carry the replacement default that the
	// promote picker selected. Zero ID and empty name mean no picker ran.
	promoteID   int64
	promoteName string
}

type eventEditLoadedMsg struct {
	event        event.Event
	instanceTime time.Time
	err          error
}

// pendingActionKind tags the intent behind an open confirm or choice
// dialog. The value decides which service call a confirmed result fires.
// The zero value means no dialog action is armed.
type pendingActionKind int

const (
	pendingActionNone pendingActionKind = iota
	// Confirm dialogs.
	pendingActionQuit             // 'q'/ctrl+c quit confirm
	pendingActionEventDelete      // single-event delete
	pendingActionPurgeEntries     // trash purge
	pendingActionAccountRemove    // account removal
	pendingActionAccountSelection // account calendar reconcile
	pendingActionCalendarKeepLocal
	pendingActionCalendarDelete
	// Choice dialogs.
	pendingActionEventDeleteScope        // recurring delete scope
	pendingActionEditScope               // recurring edit scope
	pendingActionCalendarPromote         // new-default picker before a calendar delete
	pendingActionAccountSelectionPromote // new-default picker before a reconcile
	pendingActionCalendarMoveAccount     // calendar move: pick destination account
	pendingActionCalendarMoveCollection  // calendar move: pick destination collection
)

// pendingAction is the single armed intent behind an open confirm or
// choice dialog. kind selects which pendingTarget member holds the
// payload. The zero value means nothing is armed.
type pendingAction struct {
	kind   pendingActionKind
	target pendingTarget
	label  string // pre-sanitized display name or result title
}

// pendingTarget holds the payload of one pendingAction. Exactly one
// member is live at a time; the owning kind selects it.
type pendingTarget struct {
	ev           event.Event      // eventDelete, eventDeleteScope
	save         EventFormSaveMsg // editScope
	calendarID   int64            // calendarDelete, calendarKeepLocal, calendarPromote
	promoteID    int64            // calendarDelete: replacement default
	promoteCands []int64          // calendarPromote: candidate IDs by button index
	accountID    int64            // accountRemove
	selection    *accountCalendarSelection
	defaultCands []accountDefaultCandidate
	entries      []trash.Entry // purgeEntries
	move         *calendarMoveState
}

// isCalendarMove reports whether the armed action drives the calendar
// move flow. The move request guard uses it to block a second move while
// one of the move choice dialogs is open.
func (p pendingAction) isCalendarMove() bool {
	return p.kind == pendingActionCalendarMoveAccount ||
		p.kind == pendingActionCalendarMoveCollection
}

type eventViewLoadedMsg struct {
	event event.Event
	err   error
}

type eventUpdatedMsg struct {
	calendarID int64
	err        error
}

// eventUpdateAfterScopeMsg is fired by dispatchEditScope after a scope-routed
// edit (UpdateInstance / UpdateFromInstance / Update) completes. It exists so
// the agenda reloads without a reuse of eventUpdatedMsg. That message currently
// drives the "return to view dialog" behaviour. That behaviour does not apply
// when the row under the edit may have been replaced. One example is "this
// and following", which splits to a new UID.
type eventUpdateAfterScopeMsg struct {
	calendarID int64
	err        error
}

type eventDeletedMsg struct {
	calendarID int64
	meta       event.UndoMeta
	title      string
	err        error
}

// eventRestoredMsg is emitted after an Undo attempt. On success err is nil.
// On failure err carries the reason. meta identifies which undo entry the
// restore acted on. The success handler can then remove that specific entry
// rather than a blind pop of the top. A concurrent delete may have pushed a
// new entry while the restore was in flight.
type eventRestoredMsg struct {
	meta  event.UndoMeta
	title string
	err   error
}

// deferredPushMsg fires after the undo window elapses. It signals that any
// deferred opportunistic delete push for a given (calendar, token) should
// now run. The token is compared against m.pushDeferralToken. A mismatch
// means a restore has since cancelled the push.
type deferredPushMsg struct {
	calendarID int64
	token      int
}

// SyncAllRequestedMsg asks the app to sync every connected calendar.
type SyncAllRequestedMsg struct{}

// SyncCalendarRequestedMsg asks the app to sync a single calendar.
type SyncCalendarRequestedMsg struct {
	ID   int64
	Name string
}

// syncFinishedMsg is emitted when a manual sync run completes.
type syncFinishedMsg struct {
	summary string
	err     error
	reload  bool
}

// syncTarget identifies one calendar in a multi-calendar sync run.
type syncTarget struct {
	ID   int64
	Name string
}

// syncTotals accumulates per-calendar SyncResult counts across a SyncAll run.
type syncTotals struct {
	pushed           int
	pulled           int
	deleted          int
	conflicts        int
	autoResolved     int
	skippedConflicts int
	warnings         int
	errCount         int
	firstErr         error
}

// syncAllPlannedMsg is emitted by runSyncAllPlan after a list of the connected
// calendars. The Update handler uses it to seed the per-calendar progress
// loop. The footer can then show "Syncing X (i/N)…" instead of one static line.
type syncAllPlannedMsg struct {
	targets []syncTarget
}

// syncCalendarFinishedMsg is emitted after each per-calendar SyncCalendar
// call inside a SyncAll run. The Update handler accumulates totals and
// either kicks off the next target or finalizes the summary.
type syncCalendarFinishedMsg struct {
	index  int
	total  int
	name   string
	result *syncpkg.SyncResult
	err    error
}

// opportunisticPushFinishedMsg is emitted after a save-time per-calendar push.
// It does not drive the manual-sync state machine (m.syncing). A push that
// completes while a manual sync is mid-flight then leaves the manual-sync
// status line intact.
type opportunisticPushFinishedMsg struct {
	summary string
	err     error
}

// oauthFlowPurpose records why an OAuth flow is in progress so the
// oauthFlowDoneMsg handler knows what to do with the tokens.
type oauthFlowPurpose struct {
	// Re-authentication target and stored account credential.
	accountID   int64
	accountName string
	cred        auth.Credential

	// Account sign-in: connection form values survive while the OAuth modal
	// obtains tokens, then drive account creation and collection discovery.
	calendarDiscovery    bool
	calendarDiscoveryMsg CalendarDiscoveryRequestedMsg
}

type accountReauthReadyMsg struct {
	accountID int64
	name      string
	cred      auth.Credential
	err       error
}

// oauthCredentialStoredMsg reports the post-flow credential write for a
// re-auth. A Set failure after a successful consent is a distinct state.
// The consent was spent but nothing is corrupted. A second re-auth is
// safe.
type oauthCredentialStoredMsg struct {
	accountID int64
	name      string
	err       error
}

// accountCredentialStoredMsg reports the in-place basic/bearer credential
// rotation. A failure leaves the previous credential untouched.
// StoreCredential only replaces it on success. A retry is then safe and
// nothing is torn down.
type accountCredentialStoredMsg struct {
	accountID int64
	name      string
	err       error
}

type accountDiscoveryReadyMsg struct {
	discovery      account.Discovery
	err            error
	createdAccount bool
}

type accountManagementDiscoveryReadyMsg struct {
	discovery  account.Discovery
	accountID  int64
	generation uint64
	err        error
}

type accountImportFinishedMsg struct {
	created  int
	existing int
	synced   int
	// warnings counts the import warnings collected across the first syncs
	// (values the importer had to fabricate). The status line shows them
	// like every other TUI sync path. Full text is in the state-dir log.
	warnings int
	err      error
	syncErr  error
}

type accountSelectionFinishedMsg struct {
	created    int
	removed    int
	removedIDs []int64
	synced     int
	// warnings counts the import warnings collected across the first syncs
	// of newly added calendars; see accountImportFinishedMsg.warnings.
	warnings          int
	accountRemoved    bool
	removedCurrent    bool
	err               error
	syncErr           error
	accountManagement bool
}

type accountDefaultCandidate struct {
	id   int64
	path string
	name string
}

type accountCalendarSelection struct {
	discovery         account.Discovery
	params            account.SelectionParams
	removed           []account.DiscoveredCalendar
	removedCurrent    bool
	accountManagement bool
}

type accountRemovalFinishedMsg struct {
	accountID int64
	name      string
	err       error
}
type calendarDiscoveryDiscardedMsg struct{ err error }

// syncStatusExpiredMsg clears the footer status line after a delay. The token
// is compared against the current statusToken so a newer status isn't wiped
// by an old tick.
type syncStatusExpiredMsg struct {
	token int
}

type clockTickMsg struct{ token int }

type Model struct {
	app       *app.App
	theme     Theme
	themeName string
	keys      appKeyMap
	// fullSyncStrategy is the configured conflict strategy for manual and
	// background full sync passes. The save-time opportunistic push never
	// uses it: that path always records a conflict and keeps the edit.
	fullSyncStrategy syncpkg.ConflictStrategy
	width            int
	height           int
	viewMode         viewMode
	calendar         CalendarModel
	week             WeekModel
	day              DayModel
	agenda           AgendaModel
	events           []event.Event
	// loadedFrom/loadedTo track the [from, to) UTC range currently covered
	// by m.events. Agenda expansion can then query only the new slice.
	// It does not re-query the whole window each time. Zero values
	// mean "no prior load" and force a full refresh.
	loadedFrom time.Time
	loadedTo   time.Time
	calendars  map[int64]CalendarInfo
	accounts   map[int64]account.Account
	// pendingOrder holds an optimistic sidebar order (calendar ID → position)
	// for a reorder whose async SetOrder has not yet confirmed. It is overlaid
	// onto reloads so an interleaved loadCalendars (e.g. a sync finishing
	// mid-save) can't snap calendars back to the stale DB order, and cleared
	// once calendarOrderSavedMsg confirms the write.
	pendingOrder map[int64]int64
	// Account order saves are serialized and coalesced to the newest complete
	// ID list so rapid moves cannot commit out of order.
	pendingAccountOrder      map[int64]int64
	pendingAccountOrderIDs   []int64
	accountOrderSaveInFlight bool
	dialog                   EventDialogModel
	dialogOpen               bool
	viewDialog               EventViewDialogModel
	viewDialogOpen           bool
	// pendingOpenEvent is set by WithOpenEvent so the first events load
	// can select that occurrence and open the view dialog.
	pendingOpenEvent event.Event
	// viewReturnEvent is set when the event form opens from the
	// view dialog. After the form closes (save or cancel) the app
	// reopens the view with this event. The user then lands back where
	// they started. Zero-valued ID means "do not return to view."
	viewReturnEvent event.Event
	confirmDialog   ConfirmDialogModel
	confirmOpen     bool
	choiceDialog    ChoiceDialogModel
	choiceOpen      bool
	form            EventFormModel
	formOpen        bool
	palette         PaletteModel
	paletteOpen     bool
	// pending is the single armed intent behind an open confirm or choice
	// dialog (see pendingAction). armConfirm and armChoice set it;
	// clearPending resets it.
	pending         pendingAction
	err             error
	ready           bool
	showSidebar     bool
	showWeekNumbers bool
	weekStart       time.Weekday
	focus           appFocus
	hiddenCalendars map[int64]bool
	clickedEventID  int64

	sidebar                     SidebarModel
	calendarManager             CalendarManagerModel
	calendarManagerOpen         bool
	calendarTransferGeneration  uint64
	accountRename               AccountRenameDialogModel
	accountRenameOpen           bool
	accountRenameFromSettings   bool
	accountOAuthConfig          AccountOAuthConfigDialogModel
	accountOAuthConfigOpen      bool
	accountCredentials          AccountCredentialsDialogModel
	accountCredentialsOpen      bool
	accountManagementGeneration uint64
	pendingAccountManagementID  int64
	pendingDiscoveryAccountID   int64
	pendingDiscoveryCreated     bool

	// OAuth modal plus the operation that consumes its tokens: account
	// discovery or re-authentication of an existing connection.
	oauthFlow     OAuthFlowModel
	oauthFlowOpen bool
	oauthPurpose  oauthFlowPurpose
	// oauthPending guards the window between a Re-authenticate request and
	// the modal open. The request dispatches an async credential load, so
	// oauthFlowOpen is still false when a second fast press arrives. This
	// flips synchronously so the second press is rejected. Cleared on every
	// terminal path (flow opened, load failed, or fallback dialog shown).
	oauthPending bool
	// pendingSyncCalendar holds a calendar whose sync was requested while
	// another sync was running (e.g. a re-auth completing mid-sync).
	// syncFinishedMsg drains it so the post-reauth sync isn't lost and the
	// sidebar ⚠ always clears. Zero ID means nothing queued.
	pendingSyncCalendar syncTarget

	// pendingAccountManagementID and pendingDiscovery* above drain through
	// their own lifecycles. They are not dialog wait state; see
	// pendingAction for that.

	helpDialog     HelpDialogModel
	helpDialogOpen bool

	// syncStatus is a transient footer line shown during/after a sync run.
	// statusToken is bumped whenever the status changes so stale Tick
	// expirations can tell whether they still own the current line.
	syncStatus  string
	statusToken int
	syncing     bool
	syncSpinner spinner.Model
	// syncTargets and syncTotals drive a per-calendar SyncAll run. The
	// footer can then show progress as each calendar finishes. It does
	// not use one opaque "Syncing all calendars…" line.
	syncTargets []syncTarget
	syncTotals  syncTotals

	// undoStack remembers event deletes so 'u' can reverse them.
	undoStack *UndoStack
	// toast is a single-slot affordance that surfaces the most recent undo
	// opportunity, or a restoring/failed status after 'u' is pressed.
	toast ToastModel
	// footer composes the contextual help line below the main content.
	footer             FooterModel
	clockTickToken     int
	clockTickScheduled bool
	// pushDeferrals counts opportunistic delete pushes currently deferred
	// waiting for the undo window to expire. The counter exists purely so
	// the deferred closure can detect that a later restore invalidated it
	// (pushDeferralToken bumped) and skip pushing.
	pushDeferralToken int

	// undoRestoreInFlight is true between dispatch of an undo restore and the
	// eventRestoredMsg landing. The restore runs in an async tea.Cmd while
	// Peek leaves the entry on the stack. Without this guard a second press
	// of the undo key would Peek the same entry and dispatch a second
	// RestoreUndo (issue #309). That is a spurious "Undo failed" toast plus
	// two overlapping restore transactions. Cleared when the message arrives.
	undoRestoreInFlight bool

	// trash is the "Recently deleted" overlay. While trashOpen is true
	// the main content renders trash.View() instead of the active
	// viewMode's model, and key input routes through m.trash.Update.
	trash     TrashModel
	trashOpen bool
}

// NewModel builds the root TUI model. themeName selects a built-in theme
// (see internal/tui/themes/*.toml); empty or unknown names fall back to
// DefaultThemeName.
func NewModel(a *app.App, themeName string) Model {
	return newModel(a, themeName, time.Sunday)
}

// newModel builds the root TUI model. configWeekStart is the value from
// config.toml / CHRONCAL_UI_WEEK_START. A stored UI-state choice overrides it.
func newModel(a *app.App, themeName string, configWeekStart time.Weekday) Model {
	ui := config.LoadUIState()
	hidden := make(map[int64]bool, len(ui.HiddenCalendars))
	for _, id := range ui.HiddenCalendars {
		hidden[id] = true
	}
	now := time.Now()
	vm := viewMonth
	switch ui.ViewMode {
	case "week":
		vm = viewWeek
	case "day":
		vm = viewDay
	case "agenda":
		vm = viewAgenda
	}
	weekStart := configWeekStart
	if w, ok := config.ParseWeekStart(ui.WeekStart); ok {
		weekStart = w
	}
	sb := NewSidebarModel(NewMiniMonthModel(now).SetWeekStart(weekStart), NewCalendarListModel(nil, hidden))
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	theme := LoadTheme(themeName, true)
	SetActiveTheme(theme)
	return Model{
		app:                a,
		themeName:          themeName,
		keys:               defaultAppKeys(),
		viewMode:           vm,
		calendar:           NewCalendarModel(now).SetShowWeekNumbers(ui.ShowWeekNumbers).SetWeekStart(weekStart),
		week:               NewWeekModel(now).SetShowWeekNumbers(ui.ShowWeekNumbers).SetWeekStart(weekStart),
		day:                NewDayModel(now),
		agenda:             newAgendaForStartup(now, vm, ui.AgendaShowEmptyDays),
		showSidebar:        ui.ShowSidebar,
		showWeekNumbers:    ui.ShowWeekNumbers,
		weekStart:          weekStart,
		hiddenCalendars:    hidden,
		focus:              focusCalendar,
		sidebar:            sb,
		syncSpinner:        sp,
		undoStack:          NewUndoStack(),
		toast:              NewToastModel(theme),
		footer:             NewFooterModel(theme),
		clockTickScheduled: true,
		oauthFlow:          NewOAuthFlowModel(theme),
		calendarManager:    NewCalendarManagerModel(nil, hidden, newThemedHelp(theme)),
	}
}

// newAgendaForStartup builds the agenda model used by NewModel. When the
// saved view mode is agenda, the cursor starts on today. Prime the
// next SetEvents to land on the current or next event. That mirrors the
// switch-to-agenda behavior. Cold start then matches a mid-session switch.
func newAgendaForStartup(now time.Time, vm viewMode, showEmptyDays bool) AgendaModel {
	a := NewAgendaModel(now).SetShowEmptyDays(showEmptyDays)
	if vm == viewAgenda {
		a = a.SelectCurrentOrNext(now)
	}
	return a
}

// trashLoadedMsg carries trash entries across events, todos, and journals
// for the visible calendar(s), plus an error if any domain query failed.
type trashLoadedMsg struct {
	entries []trash.Entry
	err     error
}

// trashActionDoneMsg reports the result of a restore or purge. The title is
// carried so the toast line can reference the event after the row is gone.
type trashActionDoneMsg struct {
	action string // "restored" or "purged"
	title  string
	err    error
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.loadEvents(), m.loadCalendars()}
	if m.clockTickScheduled {
		cmds = append(cmds, nextClockTick(m.clockTickToken))
	}
	return tea.Batch(cmds...)
}

// RunOptions configures a TUI session. A non-zero Event jumps to that
// occurrence and opens its view dialog after the first load.
// SyncConflictStrategy is the configured full-pass strategy name (from
// cfg.Sync.ConflictStrategy). An empty or invalid name falls back to
// server-wins.
type RunOptions struct {
	Event                event.Event
	WeekStart            time.Weekday
	SyncConflictStrategy string
}

// resolveFullSyncStrategy maps the configured conflict-strategy name to the
// value the TUI's full sync passes use. An empty name parses to prompt. An
// invalid name falls back to prompt as well, so a hand-edited config file can
// not silently enable server-wins. The warning goes to the state-dir log
// file. A write to stderr would print over the display.
func resolveFullSyncStrategy(name string) syncpkg.ConflictStrategy {
	strategy, err := syncpkg.ParseConflictStrategy(name)
	if err != nil {
		config.SharedStateDirLogger().Warn("invalid sync.conflict_strategy; full syncs fall back to prompt", "value", name, "error", err.Error())
		return syncpkg.ConflictPrompt
	}
	return strategy
}

func Run(a *app.App, themeName string, opts RunOptions) error {
	// Query the terminal before Bubble Tea takes over stdin. The helper
	// sends OSC 11 (bg) + OSC 10 (fg) + OSC 4 (palette 0..15) + DA1 in
	// one raw-mode session:
	//
	//   - bg: if reported, used as-is. Otherwise inferred from fg
	//     luminance with a neutral stand-in. Nil when nothing answers.
	//   - palette: the terminal's actual 16-color ANSI rendering. Used
	//     by the theme loader to resolve ANSI index references to real
	//     hex values. OKLCh contrast computations are then exact.
	//
	// Install the palette globally before NewModel. The initial theme
	// load (and every later reload) can then consult it. The bg
	// arrives as a synthetic BackgroundColorMsg. The current handler
	// turns that into a re-theme.
	bg, palette := detectTerminalState(os.Stdin, os.Stdout)
	SetActivePalette(palette)

	model := newModel(a, themeName, opts.WeekStart)
	model.fullSyncStrategy = resolveFullSyncStrategy(opts.SyncConflictStrategy)
	if opts.Event.ID != 0 {
		model = model.WithOpenEvent(opts.Event)
	}
	// Bubbletea renders on a frame-rate ticker (default 60 FPS). At 60
	// FPS only ~16 ms is spent per visible frame. When the user holds
	// a navigation key the highlight steps once per ~16 ms even though
	// our Update+View takes <1 ms. A bump to the max (120 FPS) halves
	// the perceived step latency under key repeat. It does not change
	// app code paths.
	p := tea.NewProgram(model, tea.WithFPS(120))

	if bg != nil {
		go func() { p.Send(tea.BackgroundColorMsg{Color: bg}) }()
	}

	_, err := p.Run()
	return err
}
