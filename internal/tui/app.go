package tui

import (
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
	syncpkg "github.com/douglasdemoura/chroncal/internal/sync"
	"github.com/douglasdemoura/chroncal/internal/trash"
	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
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

type appKeyMap struct {
	Quit           key.Binding
	MonthView      key.Binding
	WeekView       key.Binding
	DayView        key.Binding
	AgendaView     key.Binding
	Sidebar        key.Binding
	WeekNumbers    key.Binding
	Create         key.Binding
	SwitchFocus    key.Binding
	Help           key.Binding
	Palette        key.Binding
	CalendarCreate key.Binding
	CalendarList   key.Binding
	Sync           key.Binding
	Undo           key.Binding
	TrashView      key.Binding
}

func defaultAppKeys() appKeyMap {
	return appKeyMap{
		Quit:           key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		MonthView:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "month")),
		WeekView:       key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "week")),
		DayView:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "day")),
		AgendaView:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agenda")),
		Sidebar:        key.NewBinding(key.WithKeys("\\"), key.WithHelp("\\", "sidebar")),
		WeekNumbers:    key.NewBinding(key.WithKeys("#"), key.WithHelp("#", "week numbers")),
		Create:         key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new")),
		SwitchFocus:    key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "switch focus")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Palette:        key.NewBinding(key.WithKeys("/", "ctrl+k"), key.WithHelp("/", "commands")),
		CalendarCreate: key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "new calendar")),
		CalendarList:   key.NewBinding(key.WithKeys("C", "shift+c"), key.WithHelp("C", "calendars")),
		Sync:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
		Undo:           key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo")),
		TrashView:      key.NewBinding(key.WithKeys("D", "shift+d"), key.WithHelp("D", "recently deleted")),
	}
}

type eventsLoadedMsg struct {
	// from and to identify the query range so the handler can drop stale
	// responses when the active view's range has moved on (e.g., rapid
	// month navigation in the agenda fires multiple loads and the last to
	// arrive must not overwrite the current window's rows with empty data).
	from time.Time
	to   time.Time
	// merge=true means this is an incremental slice to append to the
	// existing m.events (agenda infinite-scroll path); merge=false means
	// replace m.events entirely (full refresh — initial load, cursor
	// jump, view change, post-mutation reload).
	merge  bool
	events []event.Event
	err    error
}

// miniMonthEventsLoadedMsg carries the events for the sidebar mini-month's
// displayed month. It's separate from eventsLoadedMsg so the mini-month's
// event-density dots can be computed independently from the main view's
// query range (which may be a single day/week).
type miniMonthEventsLoadedMsg struct {
	month  time.Time
	events []event.Event
	err    error
}

type calendarsLoadedMsg struct {
	calendars map[int64]CalendarInfo
	err       error
}

type eventRSVPUpdatedMsg struct {
	err error
}

type eventCreatedMsg struct {
	calendarID int64
	err        error
}

type calendarMutationDoneMsg struct{ err error }

// calendarOrderSavedMsg reports the result of persisting a sidebar reorder.
// Success is silent (the list already shows the new order); failure surfaces
// as a toast. ids carries the order that was written so the handler can clear
// the matching pendingOrder entries without discarding a newer reorder.
type calendarOrderSavedMsg struct {
	ids []int64
	err error
}

type calendarDeleteCountMsg struct {
	id         int64
	name       string
	eventCount int64
}

type eventEditLoadedMsg struct {
	event        event.Event
	instanceTime time.Time
	err          error
}

// pendingScopeKind tags which kind of recurring-scope action a ChoiceDialog
// is currently driving. Zero means no scope dialog is open.
type pendingScopeKind int

const (
	pendingScopeNone pendingScopeKind = iota
	pendingScopeDelete
	pendingScopeEdit
	pendingScopeCalendarPromote
)

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
// the agenda reloads without us re-using eventUpdatedMsg, which currently
// drives the "return to view dialog" behaviour that doesn't apply when the
// underlying row may have been replaced (e.g. "this and following" splits to
// a new UID).
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
// On failure err carries the reason.
type eventRestoredMsg struct {
	title string
	err   error
}

// deferredPushMsg fires after the undo window elapses, signalling that any
// deferred opportunistic delete push for a given (calendar, token) should
// now run. The token is compared against m.pushDeferralToken; a mismatch
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
	pushed    int
	pulled    int
	deleted   int
	conflicts int
	errCount  int
	firstErr  error
}

// syncAllPlannedMsg is emitted by runSyncAllPlan after listing the connected
// calendars. The Update handler uses it to seed the per-calendar progress
// loop so the footer can show "Syncing X (i/N)…" instead of one static line.
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
// It doesn't drive the manual-sync state machine (m.syncing), so a push that
// completes while a manual sync is mid-flight leaves the manual-sync status
// line intact.
type opportunisticPushFinishedMsg struct {
	summary string
	err     error
}

// oauthFlowPurpose records why an OAuth flow is running so the
// oauthFlowDoneMsg handler knows what to do with the tokens.
type oauthFlowPurpose struct {
	// isConnect distinguishes linking a new account (true) from
	// re-authenticating an existing one (false).
	isConnect    bool
	calendarID   int64
	calendarName string

	// Re-auth: the stored credential (client config + username source).
	accountID int64
	cred      auth.Credential

	// Connect: the dialog's save parameters and the already-created row.
	saved CalendarSavedMsg
	cal   calendar.Calendar
}

// calendarReauthReadyMsg carries the loaded credential for a re-auth whose
// client config is complete; the handler launches the flow with it.
type calendarReauthReadyMsg struct {
	calendarID int64
	name       string
	accountID  int64
	cred       auth.Credential
	err        error
}

// calendarReauthNeedsConfigMsg reopens the calendar dialog in the
// missing-config fallback mode: the stored credential predates client-secret
// storage, so the user must enter the OAuth client config once.
type calendarReauthNeedsConfigMsg struct {
	calendarID      int64
	clientIDPrefill string
}

// calendarOAuthConnectPendingMsg is emitted after an oauth2 save has written
// the calendar row (and owner email / default) but before the link exists.
// The handler closes the dialog and starts the browser flow; Connect runs
// only after the flow succeeds.
type calendarOAuthConnectPendingMsg struct {
	cal   calendar.Calendar
	saved CalendarSavedMsg
}

// oauthCredentialStoredMsg reports the post-flow credential write for a
// re-auth. A Set failure after a successful consent is a distinct state:
// the consent was spent but nothing is corrupted, and re-authenticating
// again is safe.
type oauthCredentialStoredMsg struct {
	calendarID int64
	name       string
	err        error
}

// oauthConnectFinishedMsg reports the post-flow Connect for a new link.
type oauthConnectFinishedMsg struct {
	calendarID int64
	name       string
	err        error
}

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
	width     int
	height    int
	viewMode  viewMode
	calendar  CalendarModel
	week      WeekModel
	day       DayModel
	agenda    AgendaModel
	events    []event.Event
	// loadedFrom/loadedTo track the [from, to) UTC range currently covered
	// by m.events so agenda expansion can query only the newly-added
	// slice instead of re-querying the whole window each time. Zero values
	// mean "no prior load" and force a full refresh.
	loadedFrom time.Time
	loadedTo   time.Time
	calendars  map[int64]CalendarInfo
	// pendingOrder holds an optimistic sidebar order (calendar ID → position)
	// for a reorder whose async SetOrder has not yet confirmed. It is overlaid
	// onto reloads so an interleaved loadCalendars (e.g. a sync finishing
	// mid-save) can't snap calendars back to the stale DB order, and cleared
	// once calendarOrderSavedMsg confirms the write.
	pendingOrder   map[int64]int64
	dialog         EventDialogModel
	dialogOpen     bool
	viewDialog     EventViewDialogModel
	viewDialogOpen bool
	// viewReturnEvent is set when the event form is opened from the
	// view dialog; after the form closes (save or cancel) the app
	// reopens the view with this event so the user lands back where
	// they started. Zero-valued ID means "don't return to view."
	viewReturnEvent event.Event
	confirmDialog   ConfirmDialogModel
	confirmOpen     bool
	choiceDialog    ChoiceDialogModel
	choiceOpen      bool
	form            EventFormModel
	formOpen        bool
	palette         PaletteModel
	paletteOpen     bool
	pendingDelete   event.Event
	// pendingScopeKind disambiguates which flow the open choice dialog is
	// driving — recurring delete vs recurring edit. Both reuse the same
	// ChoiceDialogModel and ChoiceDialogResultMsg.
	pendingScopeKind pendingScopeKind
	pendingEditSave  EventFormSaveMsg
	err              error
	ready            bool
	showSidebar      bool
	showWeekNumbers  bool
	focus            appFocus
	hiddenCalendars  map[int64]bool
	clickedEventID   int64

	sidebar            SidebarModel
	calendarDialog     CalendarDialogModel
	calendarDialogOpen bool

	// OAuth pending modal plus what to do with the tokens when it finishes
	// (re-authenticate an existing account vs link a new one).
	oauthFlow     OAuthFlowModel
	oauthFlowOpen bool
	oauthPurpose  oauthFlowPurpose
	// oauthPending guards the window between a Re-authenticate / connect
	// request and the modal actually opening: the request dispatches an
	// async credential load, so oauthFlowOpen is still false when a second
	// fast press arrives. This flips synchronously so the second press is
	// rejected. Cleared on every terminal path (flow opened, load failed,
	// fallback dialog shown).
	oauthPending bool
	// pendingSyncCalendar holds a calendar whose sync was requested while
	// another sync was running (e.g. a re-auth completing mid-sync).
	// syncFinishedMsg drains it so the post-reauth sync isn't lost and the
	// sidebar ⚠ always clears. Zero ID means nothing queued.
	pendingSyncCalendar         syncTarget
	pendingCalendarDelete       int64
	pendingCalendarDeleteName   string
	pendingCalendarPromote      int64   // new default to promote when deleting the current default
	pendingCalendarPromoteName  string  // human-readable name of the promotion target
	pendingCalendarPromoteCands []int64 // candidate calendar IDs by ChoiceDialog button index

	calendarListDialog     CalendarListDialogModel
	calendarListDialogOpen bool

	// pendingQuit is true while the confirm dialog is asking the user to
	// confirm a 'q' quit. Distinguishes the quit flow from event/calendar
	// delete flows that share ConfirmDialogModel.
	pendingQuit bool

	helpDialog     HelpDialogModel
	helpDialogOpen bool

	// miniMonthEvents caches the raw events for the sidebar mini-month's
	// displayed month so visibility toggles can re-filter without a DB hit.
	miniMonthEvents []event.Event

	// syncStatus is a transient footer line shown during/after a sync run.
	// statusToken is bumped whenever the status changes so stale Tick
	// expirations can tell whether they still own the current line.
	syncStatus  string
	statusToken int
	syncing     bool
	syncSpinner spinner.Model
	// syncTargets and syncTotals drive a per-calendar SyncAll run so the
	// footer can show progress as each calendar finishes instead of one
	// opaque "Syncing all calendars…" line.
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

	// trash is the "Recently deleted" overlay. While trashOpen is true
	// the main content renders trash.View() instead of the active
	// viewMode's model, and key input routes through m.trash.Update.
	trash               TrashModel
	trashOpen           bool
	pendingPurgeEntries []trash.Entry
	pendingPurgeTitle   string
}

// NewModel builds the root TUI model. themeName selects a built-in theme
// (see internal/tui/themes/*.toml); empty or unknown names fall back to
// DefaultThemeName.
func NewModel(a *app.App, themeName string) Model {
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
	sb := NewSidebarModel(NewMiniMonthModel(now), NewCalendarListModel(nil, hidden))
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	theme := LoadTheme(themeName, true)
	SetActiveTheme(theme)
	return Model{
		app:                a,
		themeName:          themeName,
		keys:               defaultAppKeys(),
		viewMode:           vm,
		calendar:           NewCalendarModel(now).SetShowWeekNumbers(ui.ShowWeekNumbers),
		week:               NewWeekModel(now).SetShowWeekNumbers(ui.ShowWeekNumbers),
		day:                NewDayModel(now),
		agenda:             newAgendaForStartup(now, vm, ui.AgendaShowEmptyDays),
		showSidebar:        ui.ShowSidebar,
		showWeekNumbers:    ui.ShowWeekNumbers,
		hiddenCalendars:    hidden,
		focus:              focusCalendar,
		sidebar:            sb,
		syncSpinner:        sp,
		undoStack:          NewUndoStack(),
		toast:              NewToastModel(theme),
		footer:             NewFooterModel(theme),
		clockTickScheduled: clockTickEnabledFor(vm),
		oauthFlow:          NewOAuthFlowModel(theme),
	}
}

// newAgendaForStartup builds the agenda model used by NewModel. When the
// saved view mode is agenda, the cursor starts on today, so prime the
// next SetEvents to land on the current/upcoming event — mirroring the
// switch-to-agenda behavior so cold start matches mid-session switching.
func newAgendaForStartup(now time.Time, vm viewMode, showEmptyDays bool) AgendaModel {
	a := NewAgendaModel(now).SetShowEmptyDays(showEmptyDays)
	if vm == viewAgenda {
		a = a.SelectCurrentOrNext(now)
	}
	return a
}

// expectedEventRange returns the [from, to) UTC range the active view
// currently expects from loadEvents. It's used to seed each query and
// to validate incoming eventsLoadedMsg against stale async responses.
func (m Model) expectedEventRange() (time.Time, time.Time) {
	switch m.viewMode {
	case viewDay:
		d := m.day.Cursor()
		from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		return from, from.AddDate(0, 0, 1)
	case viewWeek:
		start := m.week.WeekStartDate()
		from := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		return from, from.AddDate(0, 0, 7)
	case viewAgenda:
		ws := m.agenda.WindowStart()
		we := m.agenda.WindowEnd()
		return time.Date(ws.Year(), ws.Month(), ws.Day(), 0, 0, 0, 0, time.UTC),
			time.Date(we.Year(), we.Month(), we.Day(), 0, 0, 0, 0, time.UTC)
	default:
		anchor := calendarGridAnchor(m.calendar.Month(), m.calendar.WeekStart())
		from := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
		return from, from.AddDate(0, 0, 42)
	}
}

// dispatchEditScope routes a deferred EventFormSaveMsg to the right service
// method based on the user's choice in the scope dialog. choice indices match
// the dialog order: 0=this event only, 1=this and following, 2=all events.
// Returns the message the agenda loop should consume next.
func (m Model) dispatchEditScope(editID int64, choice int, save EventFormSaveMsg) tea.Msg {
	ctx := context.Background()
	master, err := m.app.Events.Get(ctx, editID)
	if err != nil {
		return eventUpdateAfterScopeMsg{calendarID: save.CalendarID, err: err}
	}

	params := event.UpdateParams{
		CalendarID:     save.CalendarID,
		Title:          save.Title,
		Description:    save.Description,
		Location:       save.Location,
		ConferenceURI:  save.ConferenceURI,
		StartTime:      save.StartTime,
		EndTime:        save.EndTime,
		AllDay:         save.AllDay,
		RecurrenceRule: save.RecurrenceRule,
		Timezone:       save.Timezone,
		Transp:         save.Transp,
		Class:          save.Class,
		Categories:     save.Categories,
	}

	// Each branch writes the event row together with its attendees and alarms
	// in a single transaction, so a failed child write rolls the whole edit
	// back instead of leaving a half-updated row (issue #87).
	switch choice {
	case 0: // This event only
		_, err = m.app.Events.UpdateInstanceWithRelations(ctx, master.UID, save.InstanceTime, params, save.Attendees, save.Alarms)
	case 1: // This and following
		_, err = m.app.Events.UpdateFromInstanceWithRelations(ctx, master.UID, save.InstanceTime, params, save.Attendees, save.Alarms)
	default: // All events
		// The form opened showing the clicked instance's time, so save.StartTime
		// is relative to that occurrence — not the master's DTSTART. Apply the
		// delta to the master to shift the whole series, and preserve whatever
		// duration the user left on the form.
		delta := save.StartTime.Sub(save.InstanceTime)
		newDuration := save.EndTime.Sub(save.StartTime)
		params.StartTime = master.StartTime.Add(delta)
		params.EndTime = params.StartTime.Add(newDuration)
		_, err = m.app.Events.UpdateWithRelations(ctx, master.ID, params, save.Attendees, save.Alarms)
	}
	if err != nil {
		return eventUpdateAfterScopeMsg{calendarID: save.CalendarID, err: err}
	}
	return eventUpdateAfterScopeMsg{calendarID: save.CalendarID}
}

func (m Model) loadEvents() tea.Cmd {
	from, to := m.expectedEventRange()
	return m.queryEventsRange(from, to, false)
}

// loadEventsIncremental queries only the newly-added slice of an agenda
// expansion when the loaded range shares an edge with the new expected
// range — infinite-scroll stays O(1 step) in query cost even after the
// user has scrolled years back. Falls back to a full refresh when the
// ranges don't share an edge (e.g. after a cursor jump).
func (m Model) loadEventsIncremental() tea.Cmd {
	wantFrom, wantTo := m.expectedEventRange()
	if m.loadedFrom.IsZero() || m.loadedTo.IsZero() {
		return m.queryEventsRange(wantFrom, wantTo, false)
	}
	// Forward extension: near edge unchanged, far edge pushed later.
	if m.loadedFrom.Equal(wantFrom) && m.loadedTo.Before(wantTo) {
		return m.queryEventsRange(m.loadedTo, wantTo, true)
	}
	// Backward extension: far edge unchanged, near edge pushed earlier.
	if m.loadedTo.Equal(wantTo) && wantFrom.Before(m.loadedFrom) {
		return m.queryEventsRange(wantFrom, m.loadedFrom, true)
	}
	// No shared edge — full refresh.
	return m.queryEventsRange(wantFrom, wantTo, false)
}

// queryEventsRange runs the recurrence-expanded query for [from, to) and
// returns an eventsLoadedMsg tagged with the queried range and whether
// the result is a merge (incremental) or a replacement (full refresh).
func (m Model) queryEventsRange(from, to time.Time, merge bool) tea.Cmd {
	mainCmd := func() tea.Msg {
		expanded, err := m.app.Recurrences.ListExpandedEvents(context.Background(), from, to)
		events := make([]event.Event, len(expanded))
		for i, e := range expanded {
			evt := e.Event
			if !evt.EndTime.IsZero() {
				evt.EndTime = e.InstanceTime.Add(evt.EndTime.Sub(evt.StartTime))
			}
			evt.StartTime = e.InstanceTime
			events[i] = evt
		}
		return eventsLoadedMsg{from: from, to: to, merge: merge, events: events, err: err}
	}
	// The mini-month shows a full month regardless of the main view's range,
	// so refresh its per-day event counts alongside every main reload.
	return tea.Batch(mainCmd, m.loadMiniMonthEvents())
}

// mergeEvents dedup-appends new events into existing. The dedup key is
// (ID, StartTime.UTC()) — unique for both non-recurring events and
// recurrence instances. Needed when a multi-day event straddles the
// incremental slice boundary and gets returned by both queries.
func mergeEvents(existing, incoming []event.Event) []event.Event {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]event.Event, 0, len(existing)+len(incoming))
	add := func(e event.Event) {
		key := eventDedupKey(e)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	for _, e := range existing {
		add(e)
	}
	for _, e := range incoming {
		add(e)
	}
	return out
}

func eventDedupKey(e event.Event) string {
	return e.StartTime.UTC().Format(time.RFC3339) + "|" + fmt.Sprint(e.ID)
}

// loadMiniMonthEvents queries the single-month range displayed by the sidebar
// mini-month so we can paint event-density dots under day numbers.
func (m Model) loadMiniMonthEvents() tea.Cmd {
	mm := m.sidebar.MiniMonth().DisplayMonth()
	from := time.Date(mm.Year(), mm.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	return func() tea.Msg {
		expanded, err := m.app.Recurrences.ListExpandedEvents(context.Background(), from, to)
		events := make([]event.Event, len(expanded))
		for i, e := range expanded {
			evt := e.Event
			if !evt.EndTime.IsZero() {
				evt.EndTime = e.InstanceTime.Add(evt.EndTime.Sub(evt.StartTime))
			}
			evt.StartTime = e.InstanceTime
			events[i] = evt
		}
		return miniMonthEventsLoadedMsg{month: from, events: events, err: err}
	}
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

// loadTrash queries the trash aggregator across all visible calendars
// and hands the result to the trash model via trashLoadedMsg.
func (m Model) loadTrash() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var out []trash.Entry
		for id := range m.calendars {
			if m.hiddenCalendars[id] {
				continue
			}
			entries, err := m.app.Trash.List(ctx, id)
			if err != nil {
				return trashLoadedMsg{err: err}
			}
			out = append(out, entries...)
		}
		return trashLoadedMsg{entries: out}
	}
}

// refreshMiniMonthDays recomputes the per-day event-density set from the
// cached mini-month events (honoring the current hiddenCalendars filter) and
// pushes it into the sidebar.
func (m Model) refreshMiniMonthDays() Model {
	days := make(map[string]bool, len(m.miniMonthEvents))
	for _, e := range m.miniMonthEvents {
		if m.hiddenCalendars[e.CalendarID] {
			continue
		}
		days[eventDay(e).Format("2006-01-02")] = true
	}
	mm := m.sidebar.MiniMonth().SetEventDays(days)
	m.sidebar = m.sidebar.SetMiniMonth(mm)
	return m
}

func eventsOn(events []event.Event, day time.Time) []event.Event {
	dayKey := day.Local().Format("2006-01-02")
	var out []event.Event
	for _, e := range events {
		// All-day events are stored as midnight UTC; compare in UTC
		// so negative-offset timezones don't shift the date.
		eKey := e.StartTime.Local().Format("2006-01-02")
		if e.AllDay {
			eKey = e.StartTime.UTC().Format("2006-01-02")
		}
		if eKey == dayKey {
			out = append(out, e)
		}
	}
	return out
}

// eventDay returns the display date for an event. All-day events use their
// UTC date (a datestamp, not a point in time) so they appear on the correct
// day regardless of the local timezone offset.
func eventDay(e event.Event) time.Time {
	if e.AllDay {
		return e.StartTime.UTC()
	}
	return e.StartTime.Local()
}

func eventsToCalendar(events []event.Event, calendars map[int64]CalendarInfo, hidden map[int64]bool) []CalendarEvent {
	out := make([]CalendarEvent, 0, len(events))
	for _, e := range events {
		if hidden[e.CalendarID] {
			continue
		}
		color := calendars[e.CalendarID].Color
		for _, day := range eventCalendarDays(e) {
			start, end := clipEventToDay(e, day)
			out = append(out, CalendarEvent{
				ID:        e.ID,
				Title:     e.Title,
				AllDay:    e.AllDay,
				Day:       day,
				Color:     color,
				StartTime: start,
				EndTime:   end,
			})
		}
	}
	return out
}

// eventCalendarDays returns one entry for each local calendar day an event
// touches. All-day events use UTC (their StartTime is a datestamp at 00:00
// UTC, not a point in time). Timed events use local time.
func eventCalendarDays(e event.Event) []time.Time {
	if e.AllDay {
		s := e.StartTime.UTC()
		startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC)
		end := e.EndTime.UTC()
		var days []time.Time
		for d := startDay; d.Before(end); d = d.AddDate(0, 0, 1) {
			days = append(days, d)
		}
		if len(days) == 0 {
			days = []time.Time{startDay}
		}
		return days
	}
	s := e.StartTime.Local()
	end := e.EndTime.Local()
	startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, s.Location())
	if !end.After(s) {
		return []time.Time{startDay}
	}
	var days []time.Time
	for d := startDay; d.Before(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	if len(days) == 0 {
		days = []time.Time{startDay}
	}
	return days
}

// clipEventToDay returns the event's start and end times clipped to the
// given calendar day. For all-day events the times are the event's original
// values (views ignore them). For timed events spanning midnight, the end
// of day 1 is pushed one second before midnight so the time-grid renderer
// sees an in-day hour/minute (placeEvents reads only hour/minute).
func clipEventToDay(e event.Event, day time.Time) (time.Time, time.Time) {
	if e.AllDay {
		return e.StartTime, e.EndTime
	}
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	dayEnd := dayStart.AddDate(0, 0, 1)
	start := e.StartTime.Local()
	if start.Before(dayStart) {
		start = dayStart
	}
	end := e.EndTime.Local()
	if !end.Before(dayEnd) {
		end = dayEnd.Add(-time.Second)
	}
	return start, end
}

func (m Model) loadCalendars() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		cals, err := m.app.Calendars.List(ctx)
		if err != nil {
			return calendarsLoadedMsg{err: err}
		}
		// Pre-fetch accounts so we can stamp each linked calendar with its
		// server URL — the event view uses this to detect Google-hosted
		// calendars and inject an authuser hint into meeting links.
		accountServerURLs := map[int64]string{}
		if accounts, aerr := m.app.Queries.ListAccounts(ctx); aerr == nil {
			for _, a := range accounts {
				accountServerURLs[a.ID] = a.ServerUrl
			}
		}
		info := make(map[int64]CalendarInfo, len(cals))
		for _, c := range cals {
			count, _ := m.app.Events.CountByCalendar(ctx, c.ID)
			info[c.ID] = CalendarInfo{
				Name:                c.Name,
				Color:               c.Color,
				OwnerEmail:          c.OwnerEmail,
				Description:         c.Description,
				EventCount:          count,
				DisplayOrder:        c.DisplayOrder,
				Synced:              c.AccountID != 0,
				AccountServerURL:    accountServerURLs[c.AccountID],
				LastSyncAt:          c.LastSyncAt,
				LastSyncAttemptedAt: c.LastSyncAttemptedAt,
				LastSyncError:       c.LastSyncError,
				CreatedAt:           c.CreatedAt,
				UpdatedAt:           c.UpdatedAt,
				IsDefault:           c.IsDefault,
			}
		}
		return calendarsLoadedMsg{calendars: info}
	}
}

// sortedCalendarListItems builds the sidebar's calendar rows from the calendar
// map and sorts them by the user's persisted display order (name as a tiebreak,
// e.g. rows that share a backfilled default). Shared by the reload handler and
// the reorder handler so both produce identical row order.
func sortedCalendarListItems(calendars map[int64]CalendarInfo) []CalendarListItem {
	items := make([]CalendarListItem, 0, len(calendars))
	for id, c := range calendars {
		items = append(items, CalendarListItem{ID: id, Name: c.Name, Color: c.Color, Health: syncHealthFor(c), Order: c.DisplayOrder})
	}
	slices.SortFunc(items, func(a, b CalendarListItem) int {
		return compareCalendarOrder(a.Order, a.Name, b.Order, b.Name)
	})
	return items
}

// syncHealthFor derives the sidebar health marker state from a calendar's
// persisted sync fields. Local-only calendars (not Synced) get no marker;
// a recorded last_sync_error is the only loud (SyncHealthError) state. Because
// these fields are written by every sync attempt — manual, and the background
// `chroncal tick` cron — the marker surfaces failures the user never triggered.
func syncHealthFor(info CalendarInfo) SyncHealth {
	switch {
	case !info.Synced:
		return SyncHealthNone
	case info.LastSyncError != "":
		return SyncHealthError
	case info.LastSyncAt != "":
		return SyncHealthOK
	default:
		return SyncHealthPending
	}
}

// newSyncService builds a sync.Service using the app's shared SQLite handle.
// Logs and credential-store warnings are discarded so sync work (including
// token-refresh persistence mid-run) doesn't clobber the rendered TUI; users
// run `chroncal sync run` from a shell if they need verbose output.
func (m Model) newSyncService() (*syncpkg.Service, error) {
	credStore, err := auth.NewCredentialStoreWithWarnings(true, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return syncpkg.NewService(m.app.DB, m.app.Queries, credStore, m.app.Calendars, m.app.Events, m.app.Todos, m.app.Journals, logger), nil
}

// runSyncAllPlan lists the connected calendars and emits a syncAllPlannedMsg
// so the Update loop can step through them one at a time. The actual sync work
// happens in runSyncOne so the footer can refresh between calendars.
func (m Model) runSyncAllPlan() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		cals, err := m.app.Queries.ListCalendars(ctx)
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		var targets []syncTarget
		for _, c := range cals {
			if c.AccountID == nil || *c.AccountID == 0 {
				continue
			}
			targets = append(targets, syncTarget{ID: c.ID, Name: c.Name})
		}
		return syncAllPlannedMsg{targets: targets}
	}
}

// runSyncOne syncs a single calendar inside a SyncAll run and emits
// syncCalendarFinishedMsg so the Update loop can advance to the next target
// (or finalize) and refresh the footer.
func (m Model) runSyncOne(target syncTarget, index, total int) tea.Cmd {
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return syncCalendarFinishedMsg{index: index, total: total, name: target.Name, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := svc.SyncCalendar(ctx, target.ID, syncpkg.ConflictServerWins)
		return syncCalendarFinishedMsg{index: index, total: total, name: target.Name, result: result, err: err}
	}
}

func (m Model) runSyncCalendar(id int64, name string) tea.Cmd {
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := svc.SyncCalendar(ctx, id, syncpkg.ConflictServerWins)
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		label := name
		if label == "" {
			label = "calendar"
		}
		summary := syncSummary(label, result.Pushed, result.Pulled, result.Deleted, result.Conflicts)
		var firstErr error
		if len(result.Errors) > 0 {
			firstErr = result.Errors[0]
		}
		return syncFinishedMsg{summary: summary, err: firstErr, reload: true}
	}
}

// startOAuthFlow closes the calendar dialog, opens the pending modal, and
// launches the browser authorization with the given client config. The
// caller has already recorded m.oauthPurpose.
func (m Model) startOAuthFlow(clientID, clientSecret string) (Model, tea.Cmd) {
	m.oauthPending = false // the flow is opening now; release the request guard
	m.calendarDialogOpen = false
	m.oauthFlowOpen = true
	m.oauthFlow = m.oauthFlow.SetSize(m.width, m.height)
	var cmd tea.Cmd
	m.oauthFlow, cmd = m.oauthFlow.Start(clientID, clientSecret)
	return m, cmd
}

// finishOAuthReauth persists the fresh tokens for a re-authenticated
// account. A full re-consent returns a new refresh token (unlike
// RefreshGoogleToken, which preserves the old one); the stored credential's
// username and client config are kept, the token triple is replaced.
func (m Model) finishOAuthReauth(result *auth.GoogleOAuthResult) tea.Cmd {
	p := m.oauthPurpose
	return func() tea.Msg {
		credStore, err := auth.NewCredentialStoreWithWarnings(true, io.Discard)
		if err != nil {
			return oauthCredentialStoredMsg{calendarID: p.calendarID, name: p.calendarName, err: err}
		}
		cred := p.cred
		cred.AccountID = p.accountID
		cred.AccessToken = result.AccessToken
		// Preserve the existing refresh token if Google didn't return a new
		// one. access_type=offline&prompt=consent normally forces a fresh
		// refresh token, but if it ever comes back empty, overwriting would
		// strip the account's ability to refresh and brick sync once the
		// access token expires (~1h). Keep the old one in that case.
		if result.RefreshToken != "" {
			cred.RefreshToken = result.RefreshToken
		}
		cred.TokenExpiry = result.Expiry.Format(time.RFC3339)
		if err := credStore.Set(cred); err != nil {
			return oauthCredentialStoredMsg{calendarID: p.calendarID, name: p.calendarName, err: err}
		}
		return oauthCredentialStoredMsg{calendarID: p.calendarID, name: p.calendarName}
	}
}

// finishOAuthConnect links the already-created calendar row to the remote
// using the fresh tokens, mirroring the basic/bearer connect path (metadata
// fetch is best-effort, authed with the access token like the CLI does).
func (m Model) finishOAuthConnect(result *auth.GoogleOAuthResult) tea.Cmd {
	p := m.oauthPurpose
	return func() tea.Msg {
		ctx := context.Background()
		credStore, err := auth.NewCredentialStoreWithWarnings(true, io.Discard)
		if err != nil {
			return oauthConnectFinishedMsg{calendarID: p.cal.ID, name: p.cal.Name, err: err}
		}
		cred := auth.Credential{
			Username:          p.saved.Username,
			AccessToken:       result.AccessToken,
			RefreshToken:      result.RefreshToken,
			TokenExpiry:       result.Expiry.Format(time.RFC3339),
			OAuthClientID:     p.saved.OAuthClientID,
			OAuthClientSecret: p.saved.OAuthClientSecret,
		}

		metaCtx, metaCancel := context.WithTimeout(ctx, 10*time.Second)
		meta, _ := caldav.FetchCalendarMetadata(metaCtx, p.saved.RemoteURL, p.saved.Username, result.AccessToken, p.saved.AuthType, p.saved.AllowInsecure)
		metaCancel()

		if cerr := m.app.Calendars.Connect(ctx, p.cal, calendar.RemoteLink{
			RemoteURL:     p.saved.RemoteURL,
			Username:      p.saved.Username,
			AuthType:      p.saved.AuthType,
			AllowInsecure: p.saved.AllowInsecure,
			RemoteColor:   meta.Color,
		}, cred, credStore); cerr != nil {
			return oauthConnectFinishedMsg{calendarID: p.cal.ID, name: p.cal.Name, err: cerr}
		}
		return oauthConnectFinishedMsg{calendarID: p.cal.ID, name: p.cal.Name}
	}
}

// syncProgressLabel produces a short calendar label for the per-calendar
// progress footer. Calendar names can be long (Apple/Google often exceed 40
// chars) and would push the spinner off the visible status line.
func syncProgressLabel(name string) string {
	if name == "" {
		return "calendar"
	}
	const maxLen = 32
	runes := []rune(name)
	if len(runes) <= maxLen {
		return name
	}
	return string(runes[:maxLen-1]) + "…"
}

// runOpportunisticPush pushes pending changes for a single calendar without
// pulling. Best-effort: failures don't surface as errors — the dirty flag
// survives and the background tick will retry. Returns nil for local-only
// calendars (Synced=false) so callers can unconditionally batch it into the
// post-save command without polluting the UI for offline calendars.
func (m Model) runOpportunisticPush(calendarID int64) tea.Cmd {
	info, ok := m.calendars[calendarID]
	if !ok || !info.Synced {
		return nil
	}
	name := info.Name
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return opportunisticPushFinishedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := svc.PushCalendar(ctx, calendarID, syncpkg.ConflictServerWins)
		if err != nil {
			return opportunisticPushFinishedMsg{err: err}
		}
		if result.Pushed == 0 && result.Deleted == 0 && len(result.Errors) == 0 {
			return opportunisticPushFinishedMsg{}
		}
		label := name
		if label == "" {
			label = "calendar"
		}
		summary := syncSummary(label, result.Pushed, 0, result.Deleted, 0)
		var firstErr error
		if len(result.Errors) > 0 {
			firstErr = result.Errors[0]
		}
		return opportunisticPushFinishedMsg{summary: summary, err: firstErr}
	}
}

// syncSummary builds the footer confirmation for a completed sync, listing
// only the non-zero counters. A no-op sync reads "Synced Work · up to date"
// instead of dragging four "· 0" segments behind it.
func syncSummary(label string, pushed, pulled, deleted, conflicts int) string {
	var parts []string
	if pushed > 0 {
		parts = append(parts, fmt.Sprintf("pushed %d", pushed))
	}
	if pulled > 0 {
		parts = append(parts, fmt.Sprintf("pulled %d", pulled))
	}
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("deleted %d", deleted))
	}
	if conflicts > 0 {
		noun := "conflict"
		if conflicts > 1 {
			noun = "conflicts"
		}
		parts = append(parts, fmt.Sprintf("%d %s", conflicts, noun))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Synced %s · up to date", label)
	}
	return fmt.Sprintf("Synced %s · %s", label, strings.Join(parts, " · "))
}

func (m Model) expireStatusAfter(d time.Duration, token int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return syncStatusExpiredMsg{token: token}
	})
}

func nextClockTickDelay(now time.Time) time.Duration {
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	d := nextMinute.Sub(now)
	if d <= 0 {
		return time.Minute
	}
	return d
}

func nextClockTick(token int) tea.Cmd {
	return tea.Tick(nextClockTickDelay(time.Now()), func(time.Time) tea.Msg {
		return clockTickMsg{token: token}
	})
}

func clockTickEnabledFor(mode viewMode) bool {
	return mode == viewDay || mode == viewWeek
}

func (m Model) clockTickEnabled() bool {
	return clockTickEnabledFor(m.viewMode)
}

func (m Model) scheduleClockTick() (Model, tea.Cmd) {
	if !m.clockTickEnabled() || m.clockTickScheduled {
		return m, nil
	}
	m.clockTickToken++
	m.clockTickScheduled = true
	return m, nextClockTick(m.clockTickToken)
}

func (m Model) disableClockTick() Model {
	if m.clockTickScheduled {
		m.clockTickToken++
		m.clockTickScheduled = false
	}
	return m
}

// navigateMainTo sets the active main view's cursor (and the month-view's
// displayed month) to the given date. Callers typically follow this with
// m.loadEvents() to refresh the query range.
func (m Model) navigateMainTo(t time.Time) Model {
	switch m.viewMode {
	case viewDay:
		m.day.cursor = t
	case viewWeek:
		m.week.cursor = t
	case viewAgenda:
		m.agenda.cursor = t
		m.agenda = m.agenda.ResetWindow(t)
	default:
		m.calendar.cursor = t
		m.calendar.month = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	}
	return m
}

// refreshCalendarViews recomputes the per-view CalendarEvent slices from the
// current m.events using the current m.hiddenCalendars set. Use this after the
// hidden set changes (no DB round-trip needed).
func (m Model) refreshCalendarViews() Model {
	calEvents := eventsToCalendar(m.events, m.calendars, m.hiddenCalendars)
	m.calendar = m.calendar.SetEvents(calEvents)
	m.week = m.week.SetEvents(calEvents)
	m.day = m.day.SetEvents(calEvents)
	m.agenda = m.agenda.SetEvents(filterVisibleEvents(m.events, m.hiddenCalendars), m.calendars)
	return m
}

// filterVisibleEvents drops events whose calendar is currently hidden so the
// agenda row list stays in sync with the toggle set without mutating the
// original slice.
func filterVisibleEvents(events []event.Event, hidden map[int64]bool) []event.Event {
	if len(hidden) == 0 {
		return events
	}
	out := make([]event.Event, 0, len(events))
	for _, e := range events {
		if hidden[e.CalendarID] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.loadEvents(), m.loadCalendars()}
	if m.clockTickScheduled {
		cmds = append(cmds, nextClockTick(m.clockTickToken))
	}
	return tea.Batch(cmds...)
}

const sidebarWidth = 24

// footerHeight returns the total rows the footer occupies. The footer is
// always a single line: the "? help" hint (and optional sync status).
func (m Model) footerHeight() int {
	return 1
}

func (m Model) mainDims() (int, int) {
	padding := 1
	contentHeight := m.height - m.footerHeight()
	mainWidth := m.width - padding*2
	if m.showSidebar {
		mainWidth -= sidebarWidth
	}
	return mainWidth, contentHeight
}

func (m Model) calendarOffset() (int, int) {
	padding := 1
	x := padding
	if m.showSidebar {
		x += sidebarWidth
	}
	return x, padding
}

// innerDims returns the space available inside the padded main box,
// which is what the calendar renderer should fill.
func (m Model) innerDims() (int, int) {
	mw, mh := m.mainDims()
	padding := 1
	return mw - padding*2, mh - padding*2
}

// openQuitConfirm builds and opens the quit-confirm dialog. Shared by the
// q and ctrl+c entry points so the two keystrokes can't drift in styling.
func (m Model) openQuitConfirm() Model {
	m.pendingQuit = true
	m.confirmDialog = NewConfirmDialogModel("Quit chroncal?", "Quit", m.theme).
		SetSize(m.width, m.height)
	m.confirmOpen = true
	return m
}

// interceptGlobalKeys routes the quit guard (q / ctrl+c) and help (?) ahead
// of any open dialog so they work from anywhere. A second ctrl+c while the
// quit confirm is showing forces the exit. ctrl+c is truly global (it isn't
// a character anyone types into a field); q and ? are suppressed while a
// text-entry surface owns input (palette search, event form, calendar form)
// so users can type those characters normally. The quit confirm additionally
// blocks ?, and the help dialog handles its own close keys.
func (m Model) interceptGlobalKeys(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	inQuitConfirm := m.confirmOpen && m.pendingQuit
	if msg.String() == "ctrl+c" {
		if inQuitConfirm {
			m.oauthFlow.Abort() // release any in-flight OAuth listener
			return m, tea.Quit, true
		}
		if !m.confirmOpen {
			return m.openQuitConfirm(), nil, true
		}
	}
	textEntryActive := m.paletteOpen || m.formOpen || m.calendarDialogOpen || m.oauthFlowOpen
	if key.Matches(msg, m.keys.Quit) && !m.confirmOpen && !textEntryActive {
		return m.openQuitConfirm(), nil, true
	}
	if key.Matches(msg, m.keys.Help) && !inQuitConfirm && !m.helpDialogOpen && !textEntryActive {
		return m, func() tea.Msg { return HelpDialogRequestedMsg{} }, true
	}
	// Trash: shift+D opens the Recently-deleted overlay from the main grid.
	// Blocked while a text-entry surface owns input (so typing "D" in the
	// palette / form doesn't jump out).
	if key.Matches(msg, m.keys.TrashView) && !m.trashOpen && !inQuitConfirm && !textEntryActive && !m.anyOverlayOpen() {
		m.trashOpen = true
		m.trash = NewTrashModel(m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, m.loadTrash(), true
	}
	// Undo: only active on the main grid, with no overlay competing for input.
	if key.Matches(msg, m.keys.Undo) && m.undoIsAllowed() {
		entry, ok := m.undoStack.Peek()
		if ok {
			// Bumping the token invalidates any delete-push that was still
			// waiting for the 6-second window to elapse.
			m.pushDeferralToken++
			m.toast.Restoring()
			meta := entry.Meta
			title := meta.Label
			cmd := func() tea.Msg {
				err := m.app.Events.RestoreUndo(context.Background(), meta)
				return eventRestoredMsg{title: title, err: err}
			}
			return m, cmd, true
		}
	}
	return m, nil, false
}

// currentFooterContext maps the app's focus/view state to a FooterContext,
// the input the pure-render FooterModel wants.
func (m Model) currentFooterContext() FooterContext {
	switch {
	case m.calendarListDialogOpen:
		return FooterCalendarPopup
	case m.viewDialogOpen:
		return FooterEventPopup
	case m.focus == focusSidebar:
		return FooterSidebar
	}
	switch m.viewMode {
	case viewAgenda:
		if _, ok := m.agenda.SelectedEvent(); ok {
			return FooterAgenda
		}
		return FooterAgendaEmpty
	case viewWeek:
		return FooterWeek
	case viewDay:
		return FooterDay
	default:
		return FooterMonth
	}
}

// currentFooterHasRSVP reports whether the event-popup footer should advertise
// RSVP keys. Only meaningful when the event view dialog is open.
func (m Model) currentFooterHasRSVP() bool {
	if !m.viewDialogOpen {
		return false
	}
	// The event view dialog exposes RSVP only when the user is an invited
	// attendee; defer to its own rsvpActions helper via the dialog model.
	return len(m.viewDialog.rsvpActions()) > 0
}

// currentFooterShowsTodayHint reports whether the active view's selected day
// differs from today, making the `t today` footer hint actionable.
func (m Model) currentFooterShowsTodayHint() bool {
	switch m.currentFooterContext() {
	case FooterMonth, FooterWeek, FooterDay, FooterAgendaEmpty:
		cursor, today := m.viewCursorAndToday()
		return !sameDay(cursor, today)
	case FooterAgenda:
		// Agenda navigation moves the selected row, not m.cursor — so we
		// look at the selected day (falls back to cursor when nothing is
		// selected) and compare against today.
		_, today := m.viewCursorAndToday()
		return !sameDay(m.agenda.SelectedDay(), today)
	default:
		return false
	}
}

// undoIsAllowed reports whether the `u` key should trigger an undo. The guard
// is intentionally strict: any overlay, editor, or palette that might consume
// character input takes priority, otherwise a stray `u` in a title field
// would silently trigger a restore.
func (m Model) undoIsAllowed() bool {
	if m.focus != focusCalendar {
		return false
	}
	if m.anyOverlayOpen() {
		return false
	}
	return m.undoStack != nil && m.undoStack.Len() > 0
}

// anyOverlayOpen reports whether any dialog, form, or palette is currently
// on screen. While one is up it owns input and renders its own help row, so
// the app footer should degrade to status + toast rather than duplicate the
// dialog's hints.
func (m Model) anyOverlayOpen() bool {
	return m.paletteOpen || m.formOpen || m.viewDialogOpen || m.dialogOpen ||
		m.confirmOpen || m.choiceOpen ||
		m.calendarDialogOpen || m.calendarListDialogOpen || m.helpDialogOpen ||
		m.trashOpen || m.oauthFlowOpen
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(clockTickMsg); ok {
		if msg.token != m.clockTickToken {
			return m, nil
		}
		m.clockTickScheduled = false
		if !m.clockTickEnabled() {
			return m, nil
		}
		return m.scheduleClockTick()
	}

	// Global key bindings override any open dialog: ctrl+c / q always route
	// through the quit guard, and ? opens the help dialog. The quit confirm
	// itself is exempt so its y/n/esc keys keep working, and ? is a no-op
	// while the help dialog is already up (it handles its own close keys).
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		if newM, cmd, handled := m.interceptGlobalKeys(kp); handled {
			return newM, cmd
		}
	}

	// The quit guard sits on top of every other overlay (including help,
	// palette, and text-entry dialogs), so it must own input whenever it's
	// up. Without this, keystrokes like y/n/esc would be swallowed by
	// whatever dialog happens to be underneath.
	if m.confirmOpen && m.pendingQuit {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.PasteMsg:
			var cmd tea.Cmd
			m.confirmDialog, cmd = m.confirmDialog.Update(msg)
			return m, cmd
		}
	}

	// The OAuth pending modal owns key and mouse input while it's up
	// (below only the quit guard). Flow lifecycle messages, loads, and
	// sizes fall through to the main switch, which routes them to the
	// modal and dispatches the outcome.
	if m.oauthFlowOpen && (!m.confirmOpen || !m.pendingQuit) {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseClickMsg:
			var cmd tea.Cmd
			m.oauthFlow, cmd = m.oauthFlow.Update(msg)
			return m, cmd
		}
	}

	// When the palette is open, it captures all input. Only specific
	// parent-level messages (size, theme, palette-result) fall through.
	if m.paletteOpen {
		switch msg.(type) {
		case PaletteSelectedMsg, PaletteClosedMsg,
			tea.BackgroundColorMsg, tea.WindowSizeMsg:
			// fall through to main switch
		default:
			var cmd tea.Cmd
			m.palette, cmd = m.palette.Update(msg)
			return m, cmd
		}
	}

	// When a confirm/choice dialog is stacked on top of the calendar dialog
	// (e.g. the delete-calendar confirm), it must own input — otherwise Esc
	// would close the calendar dialog underneath instead of the confirm.
	if m.calendarDialogOpen && !m.confirmOpen && !m.choiceOpen {
		switch msg.(type) {
		case CalendarSavedMsg, CalendarDeleteRequestedMsg, CalendarDialogClosedMsg,
			CalendarDisconnectRemoteRequestedMsg,
			CalendarTestRequestedMsg,
			CalendarSetDefaultRequestedMsg,
			CalendarReauthRequestedMsg,
			calendarReauthReadyMsg, calendarReauthNeedsConfigMsg,
			calendarDeleteCountMsg,
			tea.BackgroundColorMsg, tea.WindowSizeMsg,
			eventsLoadedMsg, calendarsLoadedMsg,
			calendarMutationDoneMsg,
			calendarOAuthConnectPendingMsg:
			// fall through to main switch
		default:
			var cmd tea.Cmd
			m.calendarDialog, cmd = m.calendarDialog.Update(msg)
			return m, cmd
		}
	}

	// When the form is open, route most messages to it (cursor blink, keys, etc.).
	// Only let specific parent-level messages fall through to the main switch.
	if m.formOpen {
		switch msg.(type) {
		case EventFormSaveMsg, EventFormClosedMsg,
			tea.BackgroundColorMsg, tea.WindowSizeMsg,
			eventsLoadedMsg, calendarsLoadedMsg,
			eventCreatedMsg, eventUpdatedMsg:
			// fall through to main switch
		default:
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(msg)
			return m, cmd
		}
	}

	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = LoadTheme(m.themeName, msg.IsDark())
		// System theme opts into a terminal-adaptive Selected color:
		// derive it from the actual terminal background reported over
		// OSC 11 by shifting OKLCh lightness ±8 %. That guarantees a
		// visible-but-subtle highlight on any terminal theme — the
		// static Dracula Selection hexes in system.toml are the
		// fallback when OSC 11 doesn't answer (e.g. tmux without
		// passthrough).
		if m.themeName == "system" && msg.Color != nil {
			delta := 0.08
			if !msg.IsDark() {
				delta = -0.08
			}
			m.theme.Selected = oklch.ShiftLightness(msg.Color, delta)
			// Secondary buttons need more weight than a list-row fill —
			// at ±0.08 the pill barely separates from the terminal bg on
			// low-contrast themes. Use ±0.18 so the button reads as a
			// tappable surface, while list-row Selected stays subtle.
			btnDelta := 0.18
			if !msg.IsDark() {
				btnDelta = -0.18
			}
			m.theme.ButtonBg = oklch.ShiftLightness(msg.Color, btnDelta)
		}
		SetActiveTheme(m.theme)
		// Month/week/day views use the selected-color as a vibrant
		// BORDER stroke around the cursor cell; Primary (the brand
		// accent) always stands out against the cell background. The
		// neutral theme.Selected is reserved for list-row FILLS
		// (trash, event list, palette, …) where a muted highlight is
		// what you want.
		m.calendar = m.calendar.SetSelectedColor(m.theme.Primary)
		m.week = m.week.SetSelectedColor(m.theme.Primary)
		m.day = m.day.SetSelectedColor(m.theme.Primary)
		m.agenda = m.agenda.SetTheme(m.theme)
		m.sidebar = m.sidebar.SetTheme(m.theme)
		m.toast.SetTheme(m.theme)
		m.oauthFlow = m.oauthFlow.SetTheme(m.theme)
		m.footer.SetTheme(m.theme)
		if m.trashOpen {
			m.trash = m.trash.SetSelectedColor(m.theme.Selected)
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		iw, ih := m.innerDims()
		m.calendar = m.calendar.SetSize(iw, ih)
		m.week = m.week.SetSize(iw, ih)
		m.day = m.day.SetSize(iw, ih)
		m.agenda = m.agenda.SetSize(iw, ih)
		m.trash = m.trash.SetSize(m.width, m.height)
		m.dialog = m.dialog.SetSize(m.width, m.height)
		m.viewDialog = m.viewDialog.SetSize(m.width, m.height)
		m.confirmDialog = m.confirmDialog.SetSize(m.width, m.height)
		m.choiceDialog = m.choiceDialog.SetSize(m.width, m.height)
		m.form = m.form.SetSize(m.width, m.height)
		m.palette = m.palette.SetSize(m.width, m.height)
		m.calendarListDialog = m.calendarListDialog.SetSize(m.width, m.height)
		m.helpDialog = m.helpDialog.SetSize(m.width, m.height)
		m.oauthFlow = m.oauthFlow.SetSize(m.width, m.height)
		m.ready = true
		return m, nil

	case eventsLoadedMsg:
		// Guard against a stale load: rapid navigation (e.g. repeated [/] in
		// the agenda) fires multiple in-flight queries; drop any whose range
		// no longer matches the active view so a late stale response can't
		// overwrite correct rows with an empty set.
		//
		// For full (merge=false) responses the query range must equal the
		// current expected range exactly. For incremental (merge=true)
		// responses the queried slice must lie inside the expected range
		// and abut the currently-loaded range so the append is meaningful;
		// otherwise the user has since jumped elsewhere.
		expectedFrom, expectedTo := m.expectedEventRange()
		if msg.merge {
			if msg.from.Before(expectedFrom) || msg.to.After(expectedTo) {
				return m, nil
			}
			if !msg.from.Equal(m.loadedTo) && !msg.to.Equal(m.loadedFrom) {
				return m, nil
			}
		} else if !msg.from.Equal(expectedFrom) || !msg.to.Equal(expectedTo) {
			return m, nil
		}
		m.err = msg.err
		if msg.merge {
			m.events = mergeEvents(m.events, msg.events)
			if msg.from.Before(m.loadedFrom) {
				m.loadedFrom = msg.from
			}
			if msg.to.After(m.loadedTo) {
				m.loadedTo = msg.to
			}
		} else {
			m.events = msg.events
			m.loadedFrom = msg.from
			m.loadedTo = msg.to
		}
		calEvents := eventsToCalendar(msg.events, m.calendars, m.hiddenCalendars)
		switch m.viewMode {
		case viewDay:
			m.day = m.day.SetEvents(calEvents)
		case viewWeek:
			m.week = m.week.SetEvents(calEvents)
		case viewAgenda:
			// Pass m.events (the merged cache) rather than msg.events (which
			// is only the delta for incremental responses) — otherwise a
			// merge would rebuild the agenda rows with only the newly-
			// fetched slice and the previously-shown events would vanish.
			m.agenda = m.agenda.SetEvents(filterVisibleEvents(m.events, m.hiddenCalendars), m.calendars)
		default:
			m.calendar = m.calendar.SetEvents(calEvents)
		}
		if m.dialogOpen {
			dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), m.dialog.day)
			m.dialog = m.dialog.SetEvents(dayEvents)
		}
		// After an agenda load lands, pull in the next month if the loaded
		// rows don't fill the viewport — prevents the user from staring at
		// blank space below a sparse month until they navigate.
		if m.viewMode == viewAgenda {
			if cmd := m.agenda.MaybeFillViewport(); cmd != nil {
				return m, cmd
			}
		}
		return m, nil

	case calendarsLoadedMsg:
		if msg.err == nil {
			m.calendars = msg.calendars
			// Overlay any unconfirmed reorder so a reload that races an
			// in-flight SetOrder reflects the user's move instead of the stale
			// DB order. Applied to m.calendars itself so the sidebar items and
			// the manage dialog (both built below from m.calendars) agree.
			for id, pos := range m.pendingOrder {
				if c, ok := m.calendars[id]; ok {
					c.DisplayOrder = pos
					m.calendars[id] = c
				} else {
					// Gone from a full reload (deleted) — drop the stale pending
					// entry so the map can't grow without bound across
					// reorder+delete cycles.
					delete(m.pendingOrder, id)
				}
			}
			m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItems(sortedCalendarListItems(m.calendars)))
			// Prune stale hidden IDs after CalendarListModel has done its pruning.
			m.hiddenCalendars = m.sidebar.List().HiddenSet()
			m.saveUIState()
			// Rebuild the per-view CalendarEvent slices so rename/color edits
			// reflect immediately — eventsToCalendar reads colors from
			// m.calendars at conversion time.
			m = m.refreshCalendarViews()
			if m.calendarListDialogOpen {
				m.calendarListDialog = m.calendarListDialog.SetCalendars(m.calendars, m.hiddenCalendars)
			}
		}
		return m, nil

	case CalendarMonthChangedMsg:
		return m, m.loadEvents()

	case WeekChangedMsg:
		return m, m.loadEvents()

	case DayChangedMsg:
		return m, m.loadEvents()

	case AgendaCursorChangedMsg:
		m.agenda = m.agenda.ResetWindow(msg.Day)
		return m, m.loadEvents()

	case AgendaReloadMsg:
		return m, m.loadEventsIncremental()

	case AgendaEmptyDaysToggledMsg:
		m.saveUIState()
		return m, nil

	case CalendarDaySelectedMsg:
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
		if m.clickedEventID > 0 {
			clicked := m.clickedEventID
			m.clickedEventID = 0
			for _, e := range dayEvents {
				if e.ID == clicked {
					ev := e
					return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
				}
			}
		}
		m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		m.dialogOpen = true
		return m, nil

	case EventCreateMsg:
		var cmd tea.Cmd
		m.form, cmd = NewEventFormModel(msg.Day, m.calendars, m.theme)
		m.form = m.form.SetSize(m.width, m.height)
		m.formOpen = true
		return m, cmd

	case EventEditMsg:
		ev := msg.Event
		// ev.StartTime carries the clicked occurrence's time for recurring
		// events (queryEventsRange overwrites it with InstanceTime). The
		// fresh Get below returns the master row, which has the original
		// DTSTART — so we capture the clicked instance time first and pass
		// it alongside so the form can prompt for scope on save.
		instanceTime := ev.StartTime
		instanceEnd := ev.EndTime
		return m, func() tea.Msg {
			ctx := context.Background()
			fresh, err := m.app.Events.Get(ctx, ev.ID)
			if err != nil {
				return eventEditLoadedMsg{err: err}
			}
			attendees, err := m.app.Events.ListAttendees(ctx, ev.ID)
			if err != nil {
				return eventEditLoadedMsg{err: err}
			}
			fresh.Attendees = attendees
			// For a recurring instance, overwrite the master's DTSTART with
			// the clicked occurrence so the form's date/time fields reflect
			// what the user actually clicked, not the original series start.
			if fresh.RecurrenceRule != "" && !instanceTime.IsZero() {
				fresh.StartTime = instanceTime
				if !instanceEnd.IsZero() {
					fresh.EndTime = instanceEnd
				}
				return eventEditLoadedMsg{event: fresh, instanceTime: instanceTime}
			}
			return eventEditLoadedMsg{event: fresh}
		}

	case eventEditLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		var cmd tea.Cmd
		m.form, cmd = NewEventFormModelForEditInstance(msg.event, msg.instanceTime, m.calendars, m.theme)
		m.form = m.form.SetSize(m.width, m.height)
		m.formOpen = true
		m.dialogOpen = false
		if m.viewDialogOpen {
			m.viewReturnEvent = msg.event
		}
		m.viewDialogOpen = false
		return m, cmd

	case EventViewRequestedMsg:
		ev := msg.Event
		return m, func() tea.Msg {
			ctx := context.Background()
			fresh, err := m.app.Events.Get(ctx, ev.ID)
			if err != nil {
				return eventViewLoadedMsg{err: err}
			}
			attendees, err := m.app.Events.ListAttendees(ctx, ev.ID)
			if err != nil {
				return eventViewLoadedMsg{err: err}
			}
			fresh.Attendees = attendees
			alarms, err := m.app.Events.ListAlarms(ctx, ev.ID)
			if err != nil {
				return eventViewLoadedMsg{err: err}
			}
			fresh.Alarms = alarms
			return eventViewLoadedMsg{event: fresh}
		}

	case eventViewLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		cal := m.calendars[msg.event.CalendarID]
		m.viewDialog = NewEventViewDialogModel(msg.event, cal, m.theme).
			SetSize(m.width, m.height)
		m.viewDialogOpen = true
		return m, nil

	case EventViewClosedMsg:
		m.viewDialogOpen = false
		return m, nil

	case EventDuplicateMsg:
		var cmd tea.Cmd
		m.form, cmd = NewEventFormModelForDuplicate(msg.Event, m.calendars, m.theme)
		m.form = m.form.SetSize(m.width, m.height)
		m.formOpen = true
		if m.viewDialogOpen {
			m.viewReturnEvent = msg.Event
		}
		m.viewDialogOpen = false
		return m, cmd

	case EventFormSaveMsg:
		// editID is read from the live form model, not the message. The
		// form's OnSubmit closure is bound before NewEventFormModelForEdit
		// assigns editID, so EventFormSaveMsg cannot carry that value
		// reliably — see event_form.go:EventFormSaveMsg for the rationale.
		editID := m.form.editID
		m.formOpen = false
		attendees := msg.Attendees
		alarms := msg.Alarms
		// Editing one occurrence of a recurring series → defer the actual
		// write until the user picks a scope. The dialog dispatch
		// (ChoiceDialogResultMsg below) reads m.pendingEditSave and routes
		// to UpdateInstance / UpdateFromInstance / Update.
		if editID > 0 && !msg.InstanceTime.IsZero() {
			m.pendingEditSave = msg
			m.pendingScopeKind = pendingScopeEdit
			m.choiceDialog = NewChoiceDialogModel(
				fmt.Sprintf("Update %q?", msg.Title),
				m.theme,
				"This event", "This and following", "All events",
			).SetSize(m.width, m.height)
			m.choiceOpen = true
			return m, nil
		}
		if editID > 0 {
			eventID := editID
			calID := msg.CalendarID
			return m, func() tea.Msg {
				ctx := context.Background()
				// Update the row and its attendees/alarms in one transaction so a
				// failed child write rolls the whole edit back (issue #87).
				_, err := m.app.Events.UpdateWithRelations(ctx, eventID, event.UpdateParams{
					CalendarID:     calID,
					Title:          msg.Title,
					Description:    msg.Description,
					Location:       msg.Location,
					ConferenceURI:  msg.ConferenceURI,
					StartTime:      msg.StartTime,
					EndTime:        msg.EndTime,
					AllDay:         msg.AllDay,
					RecurrenceRule: msg.RecurrenceRule,
					Timezone:       msg.Timezone,
					Transp:         msg.Transp,
					Class:          msg.Class,
					Categories:     msg.Categories,
				}, attendees, alarms)
				return eventUpdatedMsg{calendarID: calID, err: err}
			}
		}
		calID := msg.CalendarID
		return m, func() tea.Msg {
			ctx := context.Background()
			created, err := m.app.Events.Create(ctx, event.CreateParams{
				CalendarID:     calID,
				Title:          msg.Title,
				Description:    msg.Description,
				Location:       msg.Location,
				ConferenceURI:  msg.ConferenceURI,
				StartTime:      msg.StartTime,
				EndTime:        msg.EndTime,
				AllDay:         msg.AllDay,
				RecurrenceRule: msg.RecurrenceRule,
				Timezone:       msg.Timezone,
				Transp:         msg.Transp,
				Class:          msg.Class,
				Categories:     msg.Categories,
			})
			if err != nil {
				return eventCreatedMsg{calendarID: calID, err: err}
			}
			if len(attendees) > 0 {
				if err = m.app.Events.ReplaceAttendees(ctx, created.ID, attendees); err != nil {
					return eventCreatedMsg{calendarID: calID, err: err}
				}
			}
			if len(alarms) > 0 {
				err = m.app.Events.ReplaceAlarms(ctx, created.ID, alarms)
			}
			return eventCreatedMsg{calendarID: calID, err: err}
		}

	case EventFormClosedMsg:
		m.formOpen = false
		if m.viewReturnEvent.ID != 0 {
			ev := m.viewReturnEvent
			m.viewReturnEvent = event.Event{}
			return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
		}
		return m, nil

	case PaletteSelectedMsg:
		m.paletteOpen = false
		if msg.Action == nil {
			return m, nil
		}
		action := msg.Action
		return m, func() tea.Msg { return action() }

	case PaletteClosedMsg:
		m.paletteOpen = false
		return m, nil

	case SwitchViewMsg:
		return m.switchToView(msg.Mode)

	case GoToTodayMsg:
		return m.goToToday()

	case eventUpdateAfterScopeMsg:
		if msg.err != nil {
			m.err = msg.err
			m.viewReturnEvent = event.Event{}
			return m, nil
		}
		cmds := []tea.Cmd{m.loadEvents()}
		if push := m.runOpportunisticPush(msg.calendarID); push != nil {
			cmds = append(cmds, push)
		}
		// On scope-dispatched edits we don't return to the prior view dialog;
		// the underlying row may have been replaced (e.g. "This and following"
		// creates a new master with a fresh UID).
		m.viewReturnEvent = event.Event{}
		return m, tea.Batch(cmds...)

	case ToggleSidebarMsg:
		return m.toggleSidebar()

	case ToggleWeekNumbersMsg:
		return m.toggleWeekNumbers()

	case eventCreatedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.viewReturnEvent = event.Event{}
			return m, nil
		}
		cmds := []tea.Cmd{m.loadEvents()}
		if push := m.runOpportunisticPush(msg.calendarID); push != nil {
			cmds = append(cmds, push)
		}
		if m.viewReturnEvent.ID != 0 {
			ev := m.viewReturnEvent
			m.viewReturnEvent = event.Event{}
			cmds = append(cmds, func() tea.Msg { return EventViewRequestedMsg{Event: ev} })
		}
		return m, tea.Batch(cmds...)

	case eventUpdatedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.viewReturnEvent = event.Event{}
			return m, nil
		}
		cmds := []tea.Cmd{m.loadEvents()}
		if push := m.runOpportunisticPush(msg.calendarID); push != nil {
			cmds = append(cmds, push)
		}
		if m.viewReturnEvent.ID != 0 {
			ev := m.viewReturnEvent
			m.viewReturnEvent = event.Event{}
			cmds = append(cmds, func() tea.Msg { return EventViewRequestedMsg{Event: ev} })
		}
		return m, tea.Batch(cmds...)

	case EventRSVPMsg:
		ev := msg.Event
		ownerEmail := m.calendars[ev.CalendarID].OwnerEmail
		return m, func() tea.Msg {
			ctx := context.Background()
			attendees, err := m.app.Events.ListAttendees(ctx, ev.ID)
			if err != nil {
				return eventRSVPUpdatedMsg{err: err}
			}
			for i, att := range attendees {
				if strings.EqualFold(att.Email, ownerEmail) {
					attendees[i].RSVPStatus = msg.Status
					break
				}
			}
			err = m.app.Events.ReplaceAttendees(ctx, ev.ID, attendees)
			return eventRSVPUpdatedMsg{err: err}
		}

	case eventRSVPUpdatedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, m.loadEvents()

	case DialogDayChangedMsg:
		if m.viewMode == viewDay {
			prevDay := m.day.cursor.Format("2006-01-02")
			m.day.cursor = msg.Day
			if m.day.cursor.Format("2006-01-02") != prevDay {
				m.dialog = NewEventDialogModel(msg.Day, nil, m.calendars, newThemedHelp(m.theme)).
					SetSelectedColor(m.theme.Selected).
					SetSize(m.width, m.height)
				return m, m.loadEvents()
			}
			dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
			m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
				SetSelectedColor(m.theme.Selected).
				SetSize(m.width, m.height)
			return m, nil
		}
		if m.viewMode == viewWeek {
			prevWeek := m.week.WeekStartDate()
			m.week.cursor = msg.Day
			if m.week.WeekStartDate() != prevWeek {
				m.dialog = NewEventDialogModel(msg.Day, nil, m.calendars, newThemedHelp(m.theme)).
					SetSelectedColor(m.theme.Selected).
					SetSize(m.width, m.height)
				return m, m.loadEvents()
			}
			dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
			m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
				SetSelectedColor(m.theme.Selected).
				SetSize(m.width, m.height)
			return m, nil
		}
		m.calendar.cursor = msg.Day
		if msg.Day.Year() != m.calendar.month.Year() || msg.Day.Month() != m.calendar.month.Month() {
			m.calendar.month = time.Date(msg.Day.Year(), msg.Day.Month(), 1, 0, 0, 0, 0, msg.Day.Location())
			m.dialog = NewEventDialogModel(msg.Day, nil, m.calendars, newThemedHelp(m.theme)).
				SetSelectedColor(m.theme.Selected).
				SetSize(m.width, m.height)
			return m, m.loadEvents()
		}
		dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), msg.Day)
		m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, nil

	case EventDialogClosedMsg:
		m.dialogOpen = false
		return m, nil

	case EventDeleteMsg:
		m.pendingDelete = msg.Event
		if msg.Event.RecurrenceRule != "" {
			m.pendingScopeKind = pendingScopeDelete
			m.choiceDialog = NewChoiceDialogModel(
				fmt.Sprintf("Delete %q?", msg.Event.Title),
				m.theme,
				"This event", "This and following", "All events",
			).SetSize(m.width, m.height)
			m.choiceOpen = true
		} else {
			m.confirmDialog = NewConfirmDialogModel(
				fmt.Sprintf("Delete %q?", msg.Event.Title),
				"Delete",
				m.theme,
			).Destructive().SetSize(m.width, m.height)
			m.confirmOpen = true
		}
		return m, nil

	case SidebarFocusEscapedMsg:
		m.sidebar = m.sidebar.Blur()
		m.focus = focusCalendar
		return m, nil

	case MiniMonthDateSelectedMsg:
		m = m.navigateMainTo(msg.Date)
		return m, m.loadEvents()

	case MiniMonthMonthChangedMsg:
		// Sidebar month shifted — refresh the mini-month density dots so
		// the new month's events render. Main-view navigation is driven
		// only by explicit day selection (MiniMonthDateSelectedMsg); month
		// changes here are preview-only.
		return m, m.loadMiniMonthEvents()

	case miniMonthEventsLoadedMsg:
		if msg.err != nil {
			// Don't surface a sidebar fetch error as m.err — the main view
			// still works; silently drop and leave the old density map.
			return m, nil
		}
		// Guard against a stale load: if the user shifted months between
		// request and response, only accept the result if it still matches.
		current := m.sidebar.MiniMonth().DisplayMonth()
		if current.Year() != msg.month.Year() || current.Month() != msg.month.Month() {
			return m, nil
		}
		m.miniMonthEvents = msg.events
		m = m.refreshMiniMonthDays()
		return m, nil

	case CalendarVisibilityToggledMsg:
		if m.hiddenCalendars == nil {
			m.hiddenCalendars = map[int64]bool{}
		}
		if msg.Hidden {
			m.hiddenCalendars[msg.ID] = true
		} else {
			delete(m.hiddenCalendars, msg.ID)
		}
		m.saveUIState()
		m = m.refreshCalendarViews()
		// Re-filter cached mini-month events against the new visibility set.
		m = m.refreshMiniMonthDays()
		if m.dialogOpen {
			dayEvents := eventsOn(filterVisibleEvents(m.events, m.hiddenCalendars), m.dialog.day)
			m.dialog = m.dialog.SetEvents(dayEvents)
		}
		return m, nil

	case CalendarReorderedMsg:
		// A reorder can originate from either the sidebar list or the manage
		// dialog; mirror it into m.calendars so both views (and any later
		// reload-from-cache) stay coherent, and record it as pending so a
		// reload racing the async SetOrder below doesn't revert to the stale DB
		// order.
		ids := msg.IDs
		if m.pendingOrder == nil {
			m.pendingOrder = make(map[int64]int64, len(ids))
		}
		for i, id := range ids {
			m.pendingOrder[id] = int64(i)
			if info, ok := m.calendars[id]; ok {
				info.DisplayOrder = int64(i)
				m.calendars[id] = info
			}
		}
		// Re-sort the sidebar from the updated order. This is what makes a
		// dialog-originated reorder show up behind the dialog; for a
		// sidebar-originated one it just re-applies the order the list already
		// swapped to. Preserve the cursor by calendar identity so a reorder
		// (from either surface) never leaves the sidebar highlight on a
		// different calendar than before.
		m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItemsPreservingCursor(sortedCalendarListItems(m.calendars)))
		if m.calendarListDialogOpen {
			m.calendarListDialog = m.calendarListDialog.SetCalendars(m.calendars, m.hiddenCalendars)
		}
		return m, func() tea.Msg {
			return calendarOrderSavedMsg{ids: ids, err: m.app.Calendars.SetOrder(context.Background(), ids)}
		}

	case calendarOrderSavedMsg:
		if msg.err != nil {
			return m, m.toast.Failed(msg.err.Error())
		}
		// Clear only the positions this save confirmed: a newer reorder may
		// have updated m.pendingOrder while this write was in flight, and that
		// one stays pending until its own save lands.
		for i, id := range msg.ids {
			if m.pendingOrder[id] == int64(i) {
				delete(m.pendingOrder, id)
			}
		}
		return m, nil

	case CalendarDialogRequestedMsg:
		params := CalendarDialogParams{Color: "#a6e3a1"}
		if msg.ID > 0 {
			ctx := context.Background()
			cal, err := m.app.Calendars.Get(ctx, msg.ID)
			if err != nil {
				m.err = err
				return m, nil
			}
			params = CalendarDialogParams{
				ID:          cal.ID,
				Name:        cal.Name,
				Color:       cal.Color,
				Description: cal.Description,
				OwnerEmail:  cal.OwnerEmail,
				RemoteURL:   cal.RemoteURL,
				IsDefault:   cal.IsDefault,
			}
			if cal.AccountID != 0 {
				if acct, aerr := m.app.Queries.GetAccount(ctx, cal.AccountID); aerr == nil {
					params.RemoteLinked = true
					params.RemoteAuthType = acct.AuthType
					params.RemoteUsername = acct.Username
					params.LastSyncAt = cal.LastSyncAt
					params.LastSyncAttemptedAt = cal.LastSyncAttemptedAt
					params.LastSyncError = cal.LastSyncError
				}
			}
		} else {
			// Create mode: only offer the "Save and Set as Default" path
			// when another calendar already exists. The first calendar
			// auto-defaults silently in the service, so the button would
			// be redundant — and confusing if it appeared to do nothing.
			params.OfferDefault = len(m.calendars) > 0
		}
		m.calendarDialog = NewCalendarDialogModel(params, m.theme).SetSize(m.width, m.height)
		m.calendarDialogOpen = true
		return m, nil

	case CalendarSavedMsg:
		// Keep the dialog open until the mutation succeeds so we can
		// show validation errors (e.g. duplicate name) on the form.
		saved := msg
		return m, func() tea.Msg {
			ctx := context.Background()
			var (
				cal calendar.Calendar
				err error
			)
			if saved.ID == 0 {
				cal, err = m.app.Calendars.Create(ctx, saved.Name, saved.Color, saved.Description)
			} else {
				cal, err = m.app.Calendars.Update(ctx, saved.ID, saved.Name, saved.Color, saved.Description)
			}
			if err != nil {
				return calendarMutationDoneMsg{err: err}
			}

			if err := m.app.Calendars.SetOwnerEmail(ctx, cal.ID, saved.OwnerEmail); err != nil {
				return calendarMutationDoneMsg{err: err}
			}

			// oauth2 saves defer the link until the browser flow completes.
			// The row, owner email, and default are committed now, so a
			// cancelled or failed flow leaves a usable local calendar
			// rather than a half-link.
			if saved.RemoteURL != "" && calendar.NormalizeAuthType(saved.AuthType) == "oauth2" {
				if saved.ID == 0 && saved.MakeDefault {
					if derr := m.app.Calendars.SetDefault(ctx, cal.ID); derr != nil {
						return calendarMutationDoneMsg{err: derr}
					}
				}
				return calendarOAuthConnectPendingMsg{cal: cal, saved: saved}
			}

			if saved.RemoteURL != "" {
				// Discard, don't stderr: plaintext-store warnings written
				// mid-frame corrupt the renderer. Plaintext is an explicit
				// opt-in whose warning the user saw at setup; the keyring
				// is the default store and emits nothing.
				credStore, storeErr := auth.NewCredentialStoreWithWarnings(true, io.Discard)
				if storeErr != nil {
					return calendarMutationDoneMsg{err: storeErr}
				}
				cred := auth.Credential{Username: saved.Username}
				switch calendar.NormalizeAuthType(saved.AuthType) {
				case "basic":
					cred.Password = saved.Password
				case "bearer":
					cred.AccessToken = saved.Password
				}

				// Best-effort PROPFIND for the remote calendar-color so the
				// UI shows the server's color immediately, before the first
				// sync runs. Fetch failures are non-fatal — Connect proceeds
				// without seeding the color.
				metaCtx, metaCancel := context.WithTimeout(ctx, 10*time.Second)
				meta, _ := caldav.FetchCalendarMetadata(metaCtx, saved.RemoteURL, saved.Username, saved.Password, saved.AuthType, saved.AllowInsecure)
				metaCancel()

				if cerr := m.app.Calendars.Connect(ctx, cal, calendar.RemoteLink{
					RemoteURL:     saved.RemoteURL,
					Username:      saved.Username,
					AuthType:      saved.AuthType,
					AllowInsecure: saved.AllowInsecure,
					RemoteColor:   meta.Color,
				}, cred, credStore); cerr != nil {
					return calendarMutationDoneMsg{err: cerr}
				}
			}

			// MakeDefault only matters on create; edit-mode default moves
			// through the dedicated CalendarSetDefaultRequestedMsg path so
			// the rule stays in one place.
			if saved.ID == 0 && saved.MakeDefault {
				if derr := m.app.Calendars.SetDefault(ctx, cal.ID); derr != nil {
					return calendarMutationDoneMsg{err: derr}
				}
			}

			return calendarMutationDoneMsg{err: nil}
		}

	case CalendarReauthRequestedMsg:
		// One flow at a time; a manual sync mid-flight would race the
		// credential write. oauthPending closes the gap before the async
		// credential load lands and sets oauthFlowOpen — without it, a
		// double-press would launch two flows and leak the first listener.
		if m.oauthFlowOpen || m.oauthPending || m.syncing {
			return m, nil
		}
		m.oauthPending = true
		req := msg
		return m, func() tea.Msg {
			ctx := context.Background()
			cal, err := m.app.Calendars.Get(ctx, req.ID)
			if err != nil {
				return calendarReauthReadyMsg{err: err}
			}
			if cal.AccountID == 0 {
				return calendarReauthReadyMsg{err: fmt.Errorf("calendar %q is not linked to an account", cal.Name)}
			}
			credStore, err := auth.NewCredentialStoreWithWarnings(true, io.Discard)
			if err != nil {
				return calendarReauthReadyMsg{err: err}
			}
			cred, err := credStore.Get(cal.AccountID)
			if err != nil {
				return calendarReauthReadyMsg{err: fmt.Errorf("load credential: %w", err)}
			}
			// Config entered in the fallback dialog overrides the stored one.
			if req.ClientID != "" {
				cred.OAuthClientID = req.ClientID
			}
			if req.ClientSecret != "" {
				cred.OAuthClientSecret = req.ClientSecret
			}
			// Credentials linked before the client secret was persisted
			// can't re-auth silently — route to the config-entry fallback.
			if cred.OAuthClientID == "" || cred.OAuthClientSecret == "" {
				return calendarReauthNeedsConfigMsg{calendarID: req.ID, clientIDPrefill: cred.OAuthClientID}
			}
			return calendarReauthReadyMsg{calendarID: req.ID, name: cal.Name, accountID: cal.AccountID, cred: cred}
		}

	case calendarReauthReadyMsg:
		if msg.err != nil {
			m.oauthPending = false
			m.statusToken++
			m.syncStatus = "Re-authentication failed: " + msg.err.Error()
			return m, m.expireStatusAfter(8*time.Second, m.statusToken)
		}
		m.oauthPurpose = oauthFlowPurpose{
			calendarID:   msg.calendarID,
			calendarName: msg.name,
			accountID:    msg.accountID,
			cred:         msg.cred,
		}
		return m.startOAuthFlow(msg.cred.OAuthClientID, msg.cred.OAuthClientSecret)

	case calendarReauthNeedsConfigMsg:
		// Premise-2 fallback: reopen the dialog with editable client-config
		// rows. Mirrors the CalendarDialogRequestedMsg open path. The flow
		// hasn't opened, so release the pending guard — the user will press
		// Re-authenticate again from the reopened dialog.
		m.oauthPending = false
		ctx := context.Background()
		cal, err := m.app.Calendars.Get(ctx, msg.calendarID)
		if err != nil {
			m.err = err
			return m, nil
		}
		params := CalendarDialogParams{
			ID:                   cal.ID,
			Name:                 cal.Name,
			Color:                cal.Color,
			Description:          cal.Description,
			OwnerEmail:           cal.OwnerEmail,
			RemoteURL:            cal.RemoteURL,
			IsDefault:            cal.IsDefault,
			LastSyncAt:           cal.LastSyncAt,
			LastSyncAttemptedAt:  cal.LastSyncAttemptedAt,
			LastSyncError:        cal.LastSyncError,
			NeedOAuthConfig:      true,
			OAuthClientIDPrefill: msg.clientIDPrefill,
		}
		if cal.AccountID != 0 {
			if acct, aerr := m.app.Queries.GetAccount(ctx, cal.AccountID); aerr == nil {
				params.RemoteLinked = true
				params.RemoteAuthType = acct.AuthType
				params.RemoteUsername = acct.Username
			}
		}
		m.calendarDialog = NewCalendarDialogModel(params, m.theme).SetSize(m.width, m.height)
		m.calendarDialogOpen = true
		return m, nil

	case calendarOAuthConnectPendingMsg:
		m.oauthPurpose = oauthFlowPurpose{
			isConnect:    true,
			calendarID:   msg.cal.ID,
			calendarName: msg.cal.Name,
			saved:        msg.saved,
			cal:          msg.cal,
		}
		// The row already exists; refresh the sidebar before the (long) flow.
		newM, cmd := m.startOAuthFlow(msg.saved.OAuthClientID, msg.saved.OAuthClientSecret)
		return newM, tea.Batch(cmd, newM.loadCalendars())

	case oauthFlowStartedMsg:
		var cmd tea.Cmd
		m.oauthFlow, cmd = m.oauthFlow.Update(msg)
		return m, cmd

	case oauthFlowDoneMsg:
		var cmd tea.Cmd
		m.oauthFlow, cmd = m.oauthFlow.Update(msg)
		switch m.oauthFlow.State() {
		case OAuthFlowDone:
			m.oauthFlowOpen = false
			if m.oauthPurpose.isConnect {
				return m, m.finishOAuthConnect(msg.result)
			}
			return m, m.finishOAuthReauth(msg.result)
		case OAuthFlowCancelled:
			m.oauthFlowOpen = false
			m.statusToken++
			if m.oauthPurpose.isConnect {
				m.syncStatus = fmt.Sprintf("Authorization cancelled — %q saved as a local calendar", m.oauthPurpose.calendarName)
			} else {
				m.syncStatus = "Authorization cancelled"
			}
			return m, m.expireStatusAfter(6*time.Second, m.statusToken)
		default:
			// Failed: the modal stays up showing the error; esc closes it.
			// Abort so the chained Waiting-phase context cancel func is
			// invoked now rather than leaking until process exit (Wait's
			// deferred Close already released the listener).
			m.oauthFlow.Abort()
		}
		return m, cmd

	case oauthFlowClosedMsg:
		m.oauthFlowOpen = false
		return m, nil

	case oauthCredentialStoredMsg:
		if msg.err != nil {
			// Consent succeeded but the write didn't — distinct from a flow
			// failure; nothing is corrupted and repeating is safe.
			m.statusToken++
			m.syncStatus = fmt.Sprintf("Authorized, but storing the tokens failed: %s — re-authenticating again is safe", msg.err)
			return m, m.expireStatusAfter(10*time.Second, m.statusToken)
		}
		// Success ends with a sync of this calendar so the persisted health
		// flips and the sidebar ⚠ clears.
		return m, func() tea.Msg { return SyncCalendarRequestedMsg{ID: msg.calendarID, Name: msg.name} }

	case oauthConnectFinishedMsg:
		if msg.err != nil {
			m.statusToken++
			m.syncStatus = fmt.Sprintf("Connect failed: %s — %q stays local", msg.err, msg.name)
			return m, tea.Batch(m.expireStatusAfter(10*time.Second, m.statusToken), m.loadCalendars())
		}
		return m, tea.Batch(
			m.loadCalendars(),
			func() tea.Msg { return SyncCalendarRequestedMsg{ID: msg.calendarID, Name: msg.name} },
		)

	case CalendarDisconnectRemoteRequestedMsg:
		id := msg.ID
		return m, func() tea.Msg {
			ctx := context.Background()
			cal, err := m.app.Calendars.Get(ctx, id)
			if err != nil {
				return calendarMutationDoneMsg{err: err}
			}
			credStore, _ := auth.NewCredentialStoreWithWarnings(true, io.Discard)
			return calendarMutationDoneMsg{err: m.app.Calendars.Disconnect(ctx, cal, credStore)}
		}

	case CalendarSetDefaultRequestedMsg:
		id := msg.ID
		return m, func() tea.Msg {
			return calendarMutationDoneMsg{err: m.app.Calendars.SetDefault(context.Background(), id)}
		}

	case CalendarTestRequestedMsg:
		req := msg
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			meta, err := caldav.VerifyCalendarURL(ctx, req.URL, req.Username, req.Password, req.AuthType, req.AllowInsecure)
			if err != nil {
				return CalendarTestResultMsg{Message: err.Error()}
			}
			message := "Connected"
			if meta.DisplayName != "" {
				message = fmt.Sprintf("Connected · %s", meta.DisplayName)
			}
			return CalendarTestResultMsg{OK: true, Message: message}
		}

	case CalendarDeleteRequestedMsg:
		// Fetch the event count before showing the confirm dialog so the
		// user knows how many events will be deleted alongside the calendar.
		// When the target is the current default, slot in a picker dialog
		// before the destructive confirm so the user explicitly chooses
		// the replacement default — never silently promoted.
		id, name := msg.ID, msg.Name
		info, known := m.calendars[id]
		if known && info.IsDefault {
			candidates := defaultPromotionCandidates(m.calendars, id)
			if len(candidates) == 0 {
				// Last calendar: service will return ErrLastCalendar; let
				// the normal confirm flow surface the error verbatim.
				return m, func() tea.Msg {
					count, _ := m.app.Events.CountByCalendar(context.Background(), id)
					return calendarDeleteCountMsg{id: id, name: name, eventCount: count}
				}
			}
			m.pendingCalendarDelete = id
			m.pendingCalendarDeleteName = name
			m.pendingCalendarPromoteCands = make([]int64, len(candidates))
			labels := make([]string, len(candidates))
			for i, c := range candidates {
				m.pendingCalendarPromoteCands[i] = c.id
				labels[i] = c.name
			}
			m.pendingScopeKind = pendingScopeCalendarPromote
			message := fmt.Sprintf("%q is the default calendar.\n\nChoose a new default before deleting it:", name)
			m.choiceDialog = NewChoiceDialogModel(message, m.theme, labels...).
				SetSize(m.width, m.height)
			m.choiceOpen = true
			return m, nil
		}
		return m, func() tea.Msg {
			count, _ := m.app.Events.CountByCalendar(context.Background(), id)
			return calendarDeleteCountMsg{id: id, name: name, eventCount: count}
		}

	case calendarDeleteCountMsg:
		// Keep the edit dialog open behind the confirm — if the user
		// cancels the confirm, they return to the edit dialog instead of
		// losing their in-progress changes. The confirm dialog takes input
		// priority, so the edit dialog is visible but inert.
		m.pendingCalendarDelete = msg.id
		m.pendingCalendarDeleteName = msg.name
		message := fmt.Sprintf("Delete calendar %q?", msg.name)
		if msg.eventCount > 0 {
			if msg.eventCount == 1 {
				message = fmt.Sprintf("Delete calendar %q?\n\n%d event will be deleted", msg.name, msg.eventCount)
			} else {
				message = fmt.Sprintf("Delete calendar %q?\n\n%d events will be deleted", msg.name, msg.eventCount)
			}
		}
		if m.pendingCalendarPromoteName != "" {
			message += fmt.Sprintf("\n\n%q will become the default.", m.pendingCalendarPromoteName)
		}
		m.confirmDialog = NewConfirmDialogModel(message, "Delete", m.theme).
			Destructive().
			SetSize(m.width, m.height)
		m.confirmOpen = true
		return m, nil

	case CalendarDialogClosedMsg:
		m.calendarDialogOpen = false
		return m, nil

	case CalendarListDialogRequestedMsg:
		m.calendarListDialog = NewCalendarListDialogModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetMutedColor(m.theme.Muted).
			SetSize(m.width, m.height)
		m.calendarListDialogOpen = true
		return m, nil

	case CalendarListDialogClosedMsg:
		m.calendarListDialogOpen = false
		return m, nil

	case HelpDialogRequestedMsg:
		m.helpDialog = NewHelpDialogModel(m.theme).SetSize(m.width, m.height)
		m.helpDialogOpen = true
		return m, nil

	case TrashViewRequestedMsg:
		if m.trashOpen {
			return m, nil
		}
		m.trashOpen = true
		m.trash = NewTrashModel(m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, m.loadTrash()

	case HelpDialogClosedMsg:
		m.helpDialogOpen = false
		return m, nil

	case SyncAllRequestedMsg:
		if m.syncing {
			return m, nil
		}
		m.syncing = true
		m.statusToken++
		m.syncStatus = "Preparing sync…"
		m.syncTargets = nil
		m.syncTotals = syncTotals{}
		return m, tea.Batch(m.runSyncAllPlan(), m.syncSpinner.Tick)

	case syncAllPlannedMsg:
		if len(msg.targets) == 0 {
			m.syncing = false
			m.statusToken++
			m.syncStatus = "No connected calendars to sync"
			return m, m.expireStatusAfter(6*time.Second, m.statusToken)
		}
		m.syncTargets = msg.targets
		m.syncTotals = syncTotals{}
		first := msg.targets[0]
		m.statusToken++
		m.syncStatus = fmt.Sprintf("Syncing %s (1/%d)…", syncProgressLabel(first.Name), len(msg.targets))
		return m, m.runSyncOne(first, 0, len(msg.targets))

	case syncCalendarFinishedMsg:
		if msg.result != nil {
			m.syncTotals.pushed += msg.result.Pushed
			m.syncTotals.pulled += msg.result.Pulled
			m.syncTotals.deleted += msg.result.Deleted
			m.syncTotals.conflicts += msg.result.Conflicts
			m.syncTotals.errCount += len(msg.result.Errors)
			if m.syncTotals.firstErr == nil && len(msg.result.Errors) > 0 {
				m.syncTotals.firstErr = msg.result.Errors[0]
			}
		}
		if msg.err != nil {
			m.syncTotals.errCount++
			if m.syncTotals.firstErr == nil {
				m.syncTotals.firstErr = msg.err
			}
		}
		next := msg.index + 1
		if next >= msg.total {
			label := fmt.Sprintf("%d calendar(s)", msg.total)
			summary := syncSummary(label, m.syncTotals.pushed, m.syncTotals.pulled, m.syncTotals.deleted, m.syncTotals.conflicts)
			finishErr := m.syncTotals.firstErr
			m.syncTargets = nil
			m.syncTotals = syncTotals{}
			return m, func() tea.Msg {
				return syncFinishedMsg{summary: summary, err: finishErr, reload: true}
			}
		}
		target := m.syncTargets[next]
		m.statusToken++
		m.syncStatus = fmt.Sprintf("Syncing %s (%d/%d)…", syncProgressLabel(target.Name), next+1, msg.total)
		return m, m.runSyncOne(target, next, msg.total)

	case SyncCalendarRequestedMsg:
		if m.syncing {
			// A sync is already running (e.g. re-auth finished mid-sync).
			// Queue this one so it isn't lost — syncFinishedMsg drains it,
			// guaranteeing the post-reauth sync runs and the ⚠ clears.
			m.pendingSyncCalendar = syncTarget(msg)
			return m, nil
		}
		m.syncing = true
		m.statusToken++
		label := msg.Name
		if label == "" {
			label = "calendar"
		}
		m.syncStatus = fmt.Sprintf("Syncing %s…", label)
		return m, tea.Batch(m.runSyncCalendar(msg.ID, msg.Name), m.syncSpinner.Tick)

	case spinner.TickMsg:
		// Two spinners can be live: the sync footer's and the OAuth pending
		// modal's. Each bubbles spinner ignores ticks that aren't its own,
		// so routing to both is safe.
		var cmds []tea.Cmd
		if m.oauthFlowOpen {
			var c tea.Cmd
			m.oauthFlow, c = m.oauthFlow.Update(msg)
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if m.syncing {
			var c tea.Cmd
			m.syncSpinner, c = m.syncSpinner.Update(msg)
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case syncFinishedMsg:
		m.syncing = false
		m.statusToken++
		if msg.err != nil {
			if msg.summary != "" {
				m.syncStatus = fmt.Sprintf("%s — %s", msg.summary, msg.err.Error())
			} else {
				m.syncStatus = "Sync failed: " + msg.err.Error()
			}
		} else {
			m.syncStatus = msg.summary
		}
		// Always reload calendars so the sidebar health marker reflects the
		// just-attempted sync — including a *failed* run (reload == false),
		// whose error was persisted by updateSyncHealth before we got here.
		// Events only change on a successful pull, so reload those only then.
		cmds := []tea.Cmd{m.expireStatusAfter(6*time.Second, m.statusToken), m.loadCalendars()}
		if msg.reload {
			cmds = append(cmds, m.loadEvents())
		}
		// Drain a sync queued while this one ran (e.g. a re-auth that
		// completed mid-sync). Re-dispatch as a fresh request now that
		// m.syncing is false so the deferred sync actually runs.
		if m.pendingSyncCalendar.ID != 0 {
			next := m.pendingSyncCalendar
			m.pendingSyncCalendar = syncTarget{}
			cmds = append(cmds, func() tea.Msg {
				return SyncCalendarRequestedMsg(next)
			})
		}
		return m, tea.Batch(cmds...)

	case opportunisticPushFinishedMsg:
		// Don't stomp the manual-sync status line or reset m.syncing.
		if m.syncing {
			return m, nil
		}
		if msg.err == nil && msg.summary == "" {
			return m, nil
		}
		m.statusToken++
		if msg.err != nil {
			if msg.summary != "" {
				m.syncStatus = fmt.Sprintf("%s — %s", msg.summary, msg.err.Error())
			} else {
				m.syncStatus = "Sync failed: " + msg.err.Error()
			}
		} else {
			m.syncStatus = msg.summary
		}
		return m, m.expireStatusAfter(4*time.Second, m.statusToken)

	case syncStatusExpiredMsg:
		if msg.token == m.statusToken && !m.syncing {
			m.syncStatus = ""
		}
		return m, nil

	case toastTickMsg:
		m.toast.Update(msg)
		return m, nil

	case calendarMutationDoneMsg:
		if msg.err != nil {
			if m.calendarDialogOpen {
				// Show the error on the Name field so the user can fix it.
				m.calendarDialog.form.SetError(cdIdxName, msg.err.Error())
				return m, nil
			}
			m.err = msg.err
			return m, nil
		}
		m.calendarDialogOpen = false
		// Reload events too: deleting a calendar cascades to its events in
		// the DB, so the in-memory cache is stale and would keep rendering
		// orphaned events (with no color mapping) until the next event reload.
		return m, tea.Batch(m.loadCalendars(), m.loadEvents())

	case ChoiceDialogResultMsg:
		m.choiceOpen = false
		kind := m.pendingScopeKind
		m.pendingScopeKind = pendingScopeNone
		if msg.Choice < 0 {
			m.pendingEditSave = EventFormSaveMsg{}
			m.pendingDelete = event.Event{}
			if kind == pendingScopeCalendarPromote {
				m.pendingCalendarDelete = 0
				m.pendingCalendarDeleteName = ""
				m.pendingCalendarPromote = 0
				m.pendingCalendarPromoteName = ""
				m.pendingCalendarPromoteCands = nil
			}
			return m, nil
		}
		if kind == pendingScopeCalendarPromote {
			if msg.Choice < 0 || msg.Choice >= len(m.pendingCalendarPromoteCands) {
				m.pendingCalendarDelete = 0
				m.pendingCalendarDeleteName = ""
				m.pendingCalendarPromoteCands = nil
				return m, nil
			}
			promoteID := m.pendingCalendarPromoteCands[msg.Choice]
			m.pendingCalendarPromote = promoteID
			if info, ok := m.calendars[promoteID]; ok {
				m.pendingCalendarPromoteName = info.Name
			}
			id, name := m.pendingCalendarDelete, m.pendingCalendarDeleteName
			m.pendingCalendarPromoteCands = nil
			return m, func() tea.Msg {
				count, _ := m.app.Events.CountByCalendar(context.Background(), id)
				return calendarDeleteCountMsg{id: id, name: name, eventCount: count}
			}
		}
		if kind == pendingScopeEdit {
			save := m.pendingEditSave
			m.pendingEditSave = EventFormSaveMsg{}
			editID := m.form.editID
			choice := msg.Choice
			return m, func() tea.Msg {
				return m.dispatchEditScope(editID, choice, save)
			}
		}
		ev := m.pendingDelete
		return m, func() tea.Msg {
			switch msg.Choice {
			case 0: // This event
				meta, err := m.app.Events.DeleteInstanceWithUndo(context.Background(), ev.UID, ev.StartTime)
				return eventDeletedMsg{
					calendarID: ev.CalendarID,
					meta:       meta,
					title:      ev.Title,
					err:        err,
				}
			case 1: // This and following
				meta, err := m.app.Events.DeleteFromInstanceWithUndo(context.Background(), ev.UID, ev.StartTime)
				return eventDeletedMsg{
					calendarID: ev.CalendarID,
					meta:       meta,
					title:      ev.Title,
					err:        err,
				}
			case 2: // All events
				meta, err := m.app.Events.DeleteSeriesWithUndo(context.Background(), ev.UID)
				return eventDeletedMsg{
					calendarID: ev.CalendarID,
					meta:       meta,
					title:      ev.Title,
					err:        err,
				}
			}
			return eventDeletedMsg{calendarID: ev.CalendarID}
		}

	case ConfirmDialogResultMsg:
		m.confirmOpen = false
		if m.pendingQuit {
			m.pendingQuit = false
			if msg.Confirmed {
				m.oauthFlow.Abort() // release any in-flight OAuth listener
				return m, tea.Quit
			}
			return m, nil
		}
		if !msg.Confirmed {
			m.pendingCalendarDelete = 0
			m.pendingCalendarDeleteName = ""
			m.pendingCalendarPromote = 0
			m.pendingCalendarPromoteName = ""
			m.pendingPurgeEntries = nil
			m.pendingPurgeTitle = ""
			return m, nil
		}
		if len(m.pendingPurgeEntries) > 0 {
			entries := m.pendingPurgeEntries
			title := m.pendingPurgeTitle
			m.pendingPurgeEntries = nil
			m.pendingPurgeTitle = ""
			return m, func() tea.Msg {
				for _, e := range entries {
					if err := m.app.Trash.Purge(context.Background(), e); err != nil {
						return trashActionDoneMsg{action: "purged", title: title, err: err}
					}
				}
				return trashActionDoneMsg{action: "purged", title: title, err: nil}
			}
		}
		if m.pendingCalendarDelete != 0 {
			id := m.pendingCalendarDelete
			newDefaultID := m.pendingCalendarPromote
			m.pendingCalendarDelete = 0
			m.pendingCalendarDeleteName = ""
			m.pendingCalendarPromote = 0
			m.pendingCalendarPromoteName = ""
			// Delete confirmed: close the edit dialog too.
			m.calendarDialogOpen = false
			return m, func() tea.Msg {
				credStore, _ := auth.NewCredentialStoreWithWarnings(true, io.Discard)
				err := m.app.Calendars.DeleteWithRemoteCleanup(context.Background(), id, newDefaultID, credStore)
				return calendarMutationDoneMsg{err: err}
			}
		}
		ev := m.pendingDelete
		return m, func() tea.Msg {
			meta, err := m.app.Events.DeleteWithUndo(context.Background(), ev.ID)
			return eventDeletedMsg{
				calendarID: ev.CalendarID,
				meta:       meta,
				title:      ev.Title,
				err:        err,
			}
		}

	case eventDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.viewDialogOpen = false
		// Every soft-delete is reversible; push the undo entry unconditionally.
		// UID being empty means no row was actually deleted (defensive).
		if msg.meta.UID == "" {
			return m, tea.Batch(m.loadEvents(), m.runOpportunisticPush(msg.calendarID))
		}
		m.undoStack.Push(UndoEntry{
			Meta:      msg.meta,
			DeletedAt: time.Now(),
		})
		synced := false // opportunistic push hasn't run yet when toast shows
		toastCmd := m.toast.Deleted(msg.title, synced)
		m.pushDeferralToken++
		token := m.pushDeferralToken
		calID := msg.calendarID
		deferCmd := tea.Tick(ToastAutoDismissDelay, func(time.Time) tea.Msg {
			return deferredPushMsg{calendarID: calID, token: token}
		})
		return m, tea.Batch(m.loadEvents(), toastCmd, deferCmd)

	case deferredPushMsg:
		// If a restore bumped the token between the delete and this tick,
		// the deferred push is stale — drop it.
		if msg.token != m.pushDeferralToken {
			return m, nil
		}
		if push := m.runOpportunisticPush(msg.calendarID); push != nil {
			return m, push
		}
		return m, nil

	case eventRestoredMsg:
		if msg.err != nil {
			// Route the dismiss tick — previously dropped, so failed toasts
			// never auto-cleared in the live app.
			cmd := m.toast.Failed(msg.err.Error())
			// Leave the entry on the stack so the user can retry with
			// different context (e.g. restoring the calendar first).
			return m, cmd
		}
		m.undoStack.Pop()
		toastCmd := m.toast.Restored(msg.title)
		return m, tea.Batch(m.loadEvents(), toastCmd)

	case TrashDialogClosedMsg:
		m.trashOpen = false
		return m, nil

	case trashLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.trash = m.trash.SetEntries(msg.entries, m.calendars)
		return m, nil

	case TrashReloadMsg:
		return m, m.loadTrash()

	case TrashRestoreRequestedMsg:
		entries := msg.Entries
		if len(entries) == 0 {
			return m, nil
		}
		title := trashBulkTitle(entries)
		return m, func() tea.Msg {
			for _, e := range entries {
				if err := m.app.Trash.Restore(context.Background(), e); err != nil {
					return trashActionDoneMsg{action: "restored", title: title, err: err}
				}
			}
			return trashActionDoneMsg{action: "restored", title: title, err: nil}
		}

	case TrashPurgeRequestedMsg:
		if len(msg.Entries) == 0 {
			return m, nil
		}
		m.pendingPurgeEntries = msg.Entries
		m.pendingPurgeTitle = trashBulkTitle(msg.Entries)
		var message string
		if len(msg.Entries) == 1 {
			message = fmt.Sprintf("Purge %q forever? This can't be undone.", msg.Entries[0].Title)
		} else {
			message = fmt.Sprintf("Purge %d items forever? This can't be undone.", len(msg.Entries))
		}
		m.confirmDialog = NewConfirmDialogModel(message, "Purge", m.theme).
			Destructive().
			SetSize(m.width, m.height)
		m.confirmOpen = true
		return m, nil

	case trashActionDoneMsg:
		if msg.err != nil {
			cmd := m.toast.Failed(msg.err.Error())
			return m, cmd
		}
		m.trash = m.trash.ClearMarks()
		cmds := []tea.Cmd{m.loadTrash(), m.loadEvents()}
		switch msg.action {
		case "restored":
			cmds = append(cmds, m.toast.Restored(msg.title))
		case "purged":
			cmds = append(cmds, m.toast.Purged(msg.title))
		}
		return m, tea.Batch(cmds...)

	case tea.MouseWheelMsg:
		if m.helpDialogOpen {
			var cmd tea.Cmd
			m.helpDialog, cmd = m.helpDialog.Update(msg)
			return m, cmd
		}
		if m.viewDialogOpen {
			var cmd tea.Cmd
			m.viewDialog, cmd = m.viewDialog.Update(msg)
			return m, cmd
		}
		if m.dialogOpen {
			var cmd tea.Cmd
			m.dialog, cmd = m.dialog.Update(msg)
			return m, cmd
		}
		if m.calendarListDialogOpen {
			var cmd tea.Cmd
			m.calendarListDialog, cmd = m.calendarListDialog.Update(msg)
			return m, cmd
		}
		if m.trashOpen {
			var cmd tea.Cmd
			m.trash, cmd = m.trash.Update(msg)
			return m, cmd
		}
		if !m.dialogOpen && !m.choiceOpen && !m.confirmOpen {
			switch m.viewMode {
			case viewWeek:
				switch msg.Button {
				case tea.MouseWheelUp:
					m.week.scrollOffset -= m.week.linesPerHour
					if m.week.scrollOffset < 0 {
						m.week.scrollOffset = 0
					}
				case tea.MouseWheelDown:
					m.week.scrollOffset += m.week.linesPerHour
					if ms := m.week.maxScroll(); m.week.scrollOffset > ms {
						m.week.scrollOffset = ms
					}
				}
			case viewDay:
				switch msg.Button {
				case tea.MouseWheelUp:
					m.day.scrollOffset -= m.day.linesPerHour
					if m.day.scrollOffset < 0 {
						m.day.scrollOffset = 0
					}
				case tea.MouseWheelDown:
					m.day.scrollOffset += m.day.linesPerHour
					if ms := m.day.maxScroll(); m.day.scrollOffset > ms {
						m.day.scrollOffset = ms
					}
				}
			case viewAgenda:
				var cmd tea.Cmd
				switch msg.Button {
				case tea.MouseWheelUp:
					cmd = m.agenda.ScrollBy(-agendaWheelStep)
				case tea.MouseWheelDown:
					cmd = m.agenda.ScrollBy(agendaWheelStep)
				}
				return m, cmd
			default:
				// viewMonth: no wheel scrolling
			}
		}
		return m, nil

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		if m.helpDialogOpen {
			var cmd tea.Cmd
			m.helpDialog, cmd = m.helpDialog.Update(msg)
			return m, cmd
		}
		if m.choiceOpen {
			var cmd tea.Cmd
			m.choiceDialog, cmd = m.choiceDialog.Update(msg)
			return m, cmd
		}
		if m.confirmOpen {
			var cmd tea.Cmd
			m.confirmDialog, cmd = m.confirmDialog.Update(msg)
			return m, cmd
		}
		if m.dialogOpen {
			var cmd tea.Cmd
			m.dialog, cmd = m.dialog.Update(msg)
			return m, cmd
		}
		if m.viewDialogOpen {
			var cmd tea.Cmd
			m.viewDialog, cmd = m.viewDialog.Update(msg)
			return m, cmd
		}
		if m.calendarListDialogOpen {
			var cmd tea.Cmd
			m.calendarListDialog, cmd = m.calendarListDialog.Update(msg)
			return m, cmd
		}
		if m.trashOpen {
			var cmd tea.Cmd
			m.trash, cmd = m.trash.Update(msg)
			return m, cmd
		}
		// Footer label hit-test: clicking the context label (MONTH/WEEK/DAY/
		// AGENDA) cycles through views. The sweep in View() populates the
		// tracker with the footer label's zone whenever no overlay is open.
		if target := MouseResolve(msg.X, msg.Y); target == "footer:label" {
			return m.cycleView()
		}
		// Sidebar hit-test. The sidebar content starts at (padding, padding)
		// inside the outer screen, with a 1-col right border. If the click
		// lands inside that x-range we dispatch to the sidebar in its local
		// coordinates instead of the main calendar.
		if m.showSidebar {
			padding := 1
			if msg.X >= padding && msg.X < sidebarWidth-padding {
				localX := msg.X - padding
				localY := msg.Y - padding
				// Moving focus to the sidebar mirrors keyboard navigation;
				// otherwise the chevrons would click but not visibly focus.
				if m.focus != focusSidebar {
					m.focus = focusSidebar
					m.sidebar = m.sidebar.Focus()
				}
				var cmd tea.Cmd
				m.sidebar, cmd = m.sidebar.HandleClick(localX, localY)
				return m, cmd
			}
		}
		// Click landed outside the sidebar — pull focus back to the main
		// view so subsequent keystrokes target the calendar rather than
		// the sidebar that was last clicked.
		if m.focus == focusSidebar {
			m.sidebar = m.sidebar.Blur()
			m.focus = focusCalendar
		}
		ox, oy := m.calendarOffset()
		switch m.viewMode {
		case viewDay:
			day, ok := m.day.DayAtPosition(msg.X-ox, msg.Y-oy)
			if !ok {
				return m, nil
			}
			m.clickedEventID = m.day.EventAtPosition(msg.X-ox, msg.Y-oy)
			var cmd tea.Cmd
			m.day, cmd = m.day.selectDay(day)
			return m, cmd
		case viewWeek:
			day, ok := m.week.DayAtPosition(msg.X-ox, msg.Y-oy)
			if !ok {
				return m, nil
			}
			m.clickedEventID = m.week.EventAtPosition(msg.X-ox, msg.Y-oy)
			var cmd tea.Cmd
			m.week, cmd = m.week.selectDay(day)
			return m, cmd
		case viewAgenda:
			var cmd tea.Cmd
			m.agenda, cmd = m.agenda.HandleClick(msg.X-ox, msg.Y-oy)
			return m, cmd
		default:
			day, ok := m.calendar.DayAtPosition(msg.X-ox, msg.Y-oy)
			if !ok {
				return m, nil
			}
			m.clickedEventID = m.calendar.EventAtPosition(msg.X-ox, msg.Y-oy)
			var cmd tea.Cmd
			m.calendar, cmd = m.calendar.selectDay(day)
			return m, cmd
		}

	case tea.KeyPressMsg:
		// Help sits on top of every other overlay (see View()), so it must
		// own input — including Esc — whenever it's open.
		if m.helpDialogOpen {
			var cmd tea.Cmd
			m.helpDialog, cmd = m.helpDialog.Update(msg)
			return m, cmd
		}
		if m.choiceOpen {
			var cmd tea.Cmd
			m.choiceDialog, cmd = m.choiceDialog.Update(msg)
			return m, cmd
		}
		if m.confirmOpen {
			var cmd tea.Cmd
			m.confirmDialog, cmd = m.confirmDialog.Update(msg)
			return m, cmd
		}
		if m.viewDialogOpen {
			var cmd tea.Cmd
			m.viewDialog, cmd = m.viewDialog.Update(msg)
			return m, cmd
		}
		if m.dialogOpen {
			var cmd tea.Cmd
			m.dialog, cmd = m.dialog.Update(msg)
			return m, cmd
		}
		if m.calendarListDialogOpen {
			var cmd tea.Cmd
			m.calendarListDialog, cmd = m.calendarListDialog.Update(msg)
			return m, cmd
		}
		if m.trashOpen {
			var cmd tea.Cmd
			m.trash, cmd = m.trash.Update(msg)
			return m, cmd
		}
		switch {
		case key.Matches(msg, m.keys.Palette):
			return m.openPalette()
		case key.Matches(msg, m.keys.MonthView):
			return m.switchToView(viewMonth)
		case key.Matches(msg, m.keys.WeekView):
			return m.switchToView(viewWeek)
		case key.Matches(msg, m.keys.DayView):
			return m.switchToView(viewDay)
		case key.Matches(msg, m.keys.AgendaView):
			return m.switchToView(viewAgenda)
		case key.Matches(msg, m.keys.Sidebar):
			return m.toggleSidebar()
		case key.Matches(msg, m.keys.WeekNumbers):
			return m.toggleWeekNumbers()
		case key.Matches(msg, m.keys.CalendarCreate):
			return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} }
		case key.Matches(msg, m.keys.CalendarList):
			return m, func() tea.Msg { return CalendarListDialogRequestedMsg{} }
		case key.Matches(msg, m.keys.Sync):
			return m, func() tea.Msg { return SyncAllRequestedMsg{} }
		case key.Matches(msg, m.keys.SwitchFocus):
			// Only handle Tab/Shift+Tab at the app level when entering the
			// sidebar from the main view. Forward Tab lands on the first
			// sidebar tab stop (the prev-month chevron); backward Shift+Tab
			// lands on the last (the bottom calendar list row). Once focus
			// is inside the sidebar, the key falls through to
			// m.sidebar.Update which cycles between its internal stops and
			// emits SidebarFocusEscapedMsg to hand focus back to the main
			// view.
			if m.showSidebar && m.focus != focusSidebar {
				m.focus = focusSidebar
				if msg.String() == "shift+tab" {
					m.sidebar = m.sidebar.FocusAtEnd()
				} else {
					m.sidebar = m.sidebar.FocusAtStart()
				}
				return m, nil
			}
			// Fall through to the sidebar routing below.
		case key.Matches(msg, m.keys.Create):
			var cursor time.Time
			switch m.viewMode {
			case viewDay:
				cursor = m.day.Cursor()
			case viewWeek:
				cursor = m.week.Cursor()
			case viewAgenda:
				cursor = m.agenda.SelectedDay()
			default:
				cursor = m.calendar.Cursor()
			}
			var cmd tea.Cmd
			m.form, cmd = NewEventFormModel(cursor, m.calendars, m.theme)
			m.form = m.form.SetSize(m.width, m.height)
			m.formOpen = true
			return m, cmd
		}
		if m.focus == focusCalendar {
			switch m.viewMode {
			case viewDay:
				var cmd tea.Cmd
				m.day, cmd = m.day.Update(msg)
				return m, cmd
			case viewWeek:
				var cmd tea.Cmd
				m.week, cmd = m.week.Update(msg)
				return m, cmd
			case viewAgenda:
				var cmd tea.Cmd
				m.agenda, cmd = m.agenda.Update(msg)
				return m, cmd
			default:
				var cmd tea.Cmd
				m.calendar, cmd = m.calendar.Update(msg)
				return m, cmd
			}
		}
		if m.focus == focusSidebar {
			var cmd tea.Cmd
			m.sidebar, cmd = m.sidebar.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	return m, nil
}

func (m Model) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}

	if m.width == 0 {
		return v
	}

	if !m.ready {
		v.Content = "\n  Loading..."
		return v
	}

	padding := 1
	mainWidth, contentHeight := m.mainDims()

	var mainContent string
	switch m.viewMode {
	case viewDay:
		mainContent = m.day.View()
	case viewWeek:
		mainContent = m.week.View()
	case viewAgenda:
		mainContent = m.agenda.View()
	default:
		mainContent = m.calendar.View()
	}
	if m.err != nil {
		mainContent = lipgloss.NewStyle().Foreground(m.theme.Error).Render("Error: " + m.err.Error())
	}

	main := lipgloss.NewStyle().
		Width(mainWidth).
		Height(contentHeight).
		Padding(padding).
		Foreground(m.theme.Text).
		Render(mainContent)

	var body string
	if m.showSidebar {
		// Render content and border as two siblings joined horizontally
		// so Faint (used to match the calendar grid chrome when
		// unfocused) applies to the border glyphs only and never
		// dims the sidebar's inner text.
		sb := m.sidebar.SetSize(sidebarWidth-padding*2, contentHeight-padding*2)
		inner := lipgloss.NewStyle().
			Width(sidebarWidth).
			Height(contentHeight).
			Padding(padding).
			Foreground(m.theme.Text).
			Render(sb.View())

		borderStyle := lipgloss.NewStyle().Faint(true)
		if m.focus == focusSidebar {
			borderStyle = lipgloss.NewStyle().Foreground(m.theme.Primary)
		}
		borderChar := borderStyle.Render("│")
		borderLines := make([]string, contentHeight)
		for i := range borderLines {
			borderLines[i] = borderChar
		}
		border := strings.Join(borderLines, "\n")

		sidebar := lipgloss.JoinHorizontal(lipgloss.Top, inner, border)
		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	} else {
		body = main
	}

	var statusText string
	if m.syncStatus != "" {
		statusColor := m.theme.Primary
		if m.syncing {
			statusColor = m.theme.Muted
		}
		statusText = lipgloss.NewStyle().Foreground(statusColor).Render(m.syncStatus)
		if m.syncing {
			m.syncSpinner.Style = lipgloss.NewStyle().Foreground(m.theme.TextDim)
			statusText = m.syncSpinner.View() + " " + statusText
		}
	}
	innerWidth := m.width - padding*2
	var footerLine string
	if m.anyOverlayOpen() {
		// A dialog owns the bottom of the screen; don't duplicate its hints.
		// "? help" is misleading while the help dialog itself is up.
		footerLine = m.footer.RenderMinimal(innerWidth, statusText, m.toast.View(), !m.helpDialogOpen)
	} else {
		footerLine = m.footer.Render(
			m.currentFooterContext(),
			innerWidth,
			statusText,
			m.toast.View(),
			m.currentFooterHasRSVP(),
			m.currentFooterShowsTodayHint(),
		)
	}
	footer := lipgloss.NewStyle().
		PaddingLeft(padding).
		PaddingRight(padding).
		Render(footerLine)
	v.Content = lipgloss.JoinVertical(lipgloss.Left, body, footer)
	// Strip mouse-tracking markers embedded by the footer label so they don't
	// leak into terminal output, and populate the tracker's zones so clicks
	// on the label can be resolved. Overlays, if any, will overwrite zones
	// during their own View() sweeps below — that's fine, since the footer
	// label isn't clickable while an overlay owns input.
	v.Content = MouseSweep(v.Content)

	if m.dialogOpen {
		v.Content = m.compositeDialog(v.Content)
	}
	if m.viewDialogOpen {
		bw, bh := m.viewDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.viewDialog.View(), bw, bh)
	}
	if m.formOpen {
		bw, bh := m.form.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.form.View(), bw, bh)
		if m.form.DatePickerOpen() {
			pw, ph := m.form.DatePickerBoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.DatePickerView(), pw, ph)
		}
		if m.form.EndsDatePickerOpen() {
			pw, ph := m.form.DatePickerBoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.EndsDatePickerView(), pw, ph)
		}
		if m.form.TimezonePickerOpen() {
			tw, th := m.form.TimezonePickerBoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.TimezonePickerView(), tw, th)
		}
		if m.form.RRuleEditorOpen() {
			ew, eh := m.form.rruleEditor.BoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.rruleEditor.View(), ew, eh)
			if m.form.rruleEditor.EndsDatePickerOpen() {
				pw, ph := m.form.rruleEditor.EndsDatePickerBoxSize()
				v.Content = m.compositeOverlay(v.Content, m.form.rruleEditor.EndsDatePickerView(), pw, ph)
			}
		}
		if m.form.AlarmEditorOpen() {
			ew, eh := m.form.alarmEditor.BoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.alarmEditor.View(), ew, eh)
		}
	}
	if m.calendarListDialogOpen {
		bw, bh := m.calendarListDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.calendarListDialog.View(), bw, bh)
	}
	if m.trashOpen {
		bw, bh := m.trash.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.trash.View(), bw, bh)
	}
	if m.calendarDialogOpen {
		bw, bh := m.calendarDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.calendarDialog.View(), bw, bh)
	}
	if m.oauthFlowOpen {
		bw, bh := m.oauthFlow.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.oauthFlow.View(), bw, bh)
	}
	if m.choiceOpen {
		bw, bh := m.choiceDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.choiceDialog.View(), bw, bh)
	}
	// Regular confirms belong in the normal stack, but the quit guard must
	// sit above palette and help (which otherwise render on top) because it
	// owns input whenever pendingQuit is set.
	if m.confirmOpen && !m.pendingQuit {
		bw, bh := m.confirmDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.confirmDialog.View(), bw, bh)
	}
	if m.paletteOpen {
		bw, bh := m.palette.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.palette.View(), bw, bh)
	}
	if m.helpDialogOpen {
		bw, bh := m.helpDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.helpDialog.View(), bw, bh)
	}
	if m.confirmOpen && m.pendingQuit {
		bw, bh := m.confirmDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.confirmDialog.View(), bw, bh)
	}

	return v
}

// compositeDialog draws the dialog box over the already-rendered main view
// using an ultraviolet screen buffer. The background content outside the
// dialog's rectangle is preserved unchanged.
func (m Model) compositeDialog(background string) string {
	if m.width <= 0 || m.height <= 0 {
		return background
	}

	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(background).Draw(buf, buf.Bounds())

	dialogView := m.dialog.View()
	if dialogView == "" {
		return buf.Render()
	}

	boxW, boxH := m.dialog.BoxSize()
	if boxW <= 0 || boxH <= 0 {
		return buf.Render()
	}
	x := (m.width - boxW) / 2
	y := (m.height - boxH) / 2
	rect := image.Rect(x, y, x+boxW, y+boxH)
	uv.NewStyledString(dialogView).Draw(buf, rect)

	return buf.Render()
}

func (m Model) compositeOverlay(background, overlay string, boxW, boxH int) string {
	if m.width <= 0 || m.height <= 0 || boxW <= 0 || boxH <= 0 || overlay == "" {
		return background
	}
	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(background).Draw(buf, buf.Bounds())
	x := (m.width - boxW) / 2
	y := (m.height - boxH) / 2
	rect := image.Rect(x, y, x+boxW, y+boxH)
	uv.NewStyledString(overlay).Draw(buf, rect)
	return buf.Render()
}

// viewCursorAndToday returns the cursor and today from whichever view is active.
func (m Model) viewCursorAndToday() (time.Time, time.Time) {
	switch m.viewMode {
	case viewDay:
		return m.day.cursor, m.day.today
	case viewWeek:
		return m.week.cursor, m.week.today
	case viewAgenda:
		return m.agenda.cursor, m.agenda.today
	default:
		return m.calendar.cursor, m.calendar.today
	}
}

// switchToView changes the active view mode and synchronizes cursor/today.
// Safe to call even when already in the requested mode (no-op).
func (m Model) switchToView(mode viewMode) (tea.Model, tea.Cmd) {
	if m.viewMode == mode {
		var cmd tea.Cmd
		m, cmd = m.scheduleClockTick()
		return m, cmd
	}
	if m.focus == focusSidebar {
		m.sidebar = m.sidebar.Blur()
		m.focus = focusCalendar
	}
	cursor, today := m.viewCursorAndToday()
	m.viewMode = mode
	switch mode {
	case viewMonth:
		m.calendar.cursor = cursor
		m.calendar.today = today
		if m.calendar.cursor.Year() != m.calendar.month.Year() || m.calendar.cursor.Month() != m.calendar.month.Month() {
			m.calendar.month = time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, cursor.Location())
		}
	case viewWeek:
		m.week.cursor = cursor
		m.week.today = today
	case viewDay:
		m.day.cursor = cursor
		m.day.today = today
	case viewAgenda:
		m.agenda.cursor = cursor
		m.agenda.today = today
		m.agenda = m.agenda.ResetWindow(cursor)
		if sameDay(cursor, today) {
			m.agenda = m.agenda.SelectCurrentOrNext(time.Now())
		}
	}
	cmds := []tea.Cmd{m.switchView()}
	if m.clockTickEnabled() {
		var cmd tea.Cmd
		m, cmd = m.scheduleClockTick()
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	} else {
		m = m.disableClockTick()
	}
	return m, tea.Batch(cmds...)
}

// cycleView advances to the next view mode in the order month → week → day
// → agenda → month, preserving cursor/today across the switch.
func (m Model) cycleView() (tea.Model, tea.Cmd) {
	var next viewMode
	switch m.viewMode {
	case viewMonth:
		next = viewWeek
	case viewWeek:
		next = viewDay
	case viewDay:
		next = viewAgenda
	default:
		next = viewMonth
	}
	return m.switchToView(next)
}

// goToToday moves the cursor in the active view to today and reloads events.
func (m Model) goToToday() (tea.Model, tea.Cmd) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch m.viewMode {
	case viewDay:
		m.day.cursor = today
		m.day.today = today
	case viewWeek:
		m.week.cursor = today
		m.week.today = today
	case viewAgenda:
		m.agenda.cursor = today
		m.agenda.today = today
		m.agenda = m.agenda.ResetWindow(today)
	default:
		m.calendar.cursor = today
		m.calendar.today = today
		if m.calendar.month.Year() != today.Year() || m.calendar.month.Month() != today.Month() {
			m.calendar.month = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
		}
	}
	return m, m.loadEvents()
}

// toggleWeekNumbers toggles the ISO week-number gutter in month/week views.
func (m Model) toggleWeekNumbers() (tea.Model, tea.Cmd) {
	m.showWeekNumbers = !m.showWeekNumbers
	m.calendar = m.calendar.SetShowWeekNumbers(m.showWeekNumbers)
	m.week = m.week.SetShowWeekNumbers(m.showWeekNumbers)
	m.saveUIState()
	return m, nil
}

// toggleSidebar toggles the sidebar panel and resyncs view sizes.
func (m Model) toggleSidebar() (tea.Model, tea.Cmd) {
	m.showSidebar = !m.showSidebar
	if !m.showSidebar {
		m.focus = focusCalendar
	}
	iw, ih := m.innerDims()
	m.calendar = m.calendar.SetSize(iw, ih)
	m.week = m.week.SetSize(iw, ih)
	m.day = m.day.SetSize(iw, ih)
	m.agenda = m.agenda.SetSize(iw, ih)
	m.saveUIState()
	return m, nil
}

// openPalette initializes and shows the command palette.
func (m Model) openPalette() (tea.Model, tea.Cmd) {
	cmds := buildPaletteCommands(m)
	palette, cmd := NewPaletteModel(cmds, m.theme, makePaletteSearchFunc(m))
	palette = palette.SetSize(m.width, m.height)
	m.palette = palette
	m.paletteOpen = true
	return m, cmd
}

// switchView resizes all views and reloads events after a view mode change.
func (m *Model) switchView() tea.Cmd {
	iw, ih := m.innerDims()
	m.calendar = m.calendar.SetSize(iw, ih)
	m.week = m.week.SetSize(iw, ih)
	m.day = m.day.SetSize(iw, ih)
	m.agenda = m.agenda.SetSize(iw, ih)
	m.saveUIState()
	return m.loadEvents()
}

func (m Model) saveUIState() {
	var vm string
	switch m.viewMode {
	case viewWeek:
		vm = "week"
	case viewDay:
		vm = "day"
	case viewAgenda:
		vm = "agenda"
	default:
		vm = "month"
	}
	ids := make([]int64, 0, len(m.hiddenCalendars))
	for id, hidden := range m.hiddenCalendars {
		if hidden {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	_ = config.SaveUIState(config.UIState{
		ShowSidebar:         m.showSidebar,
		ViewMode:            vm,
		HiddenCalendars:     ids,
		AgendaShowEmptyDays: m.agenda.ShowEmptyDays(),
		ShowWeekNumbers:     m.showWeekNumbers,
	})
}

func Run(a *app.App, themeName string) error {
	// Query the terminal before Bubble Tea takes over stdin. The helper
	// sends OSC 11 (bg) + OSC 10 (fg) + OSC 4 (palette 0..15) + DA1 in
	// one raw-mode session:
	//
	//   - bg: if reported, used as-is; otherwise inferred from fg
	//     luminance with a neutral stand-in. Nil when nothing answers.
	//   - palette: the terminal's actual 16-color ANSI rendering. Used
	//     by the theme loader to resolve ANSI index references to real
	//     hex values, so OKLCh contrast computations are exact.
	//
	// We install the palette globally before NewModel so the initial
	// theme load (and every subsequent reload) can consult it. The bg
	// arrives as a synthetic BackgroundColorMsg that the existing
	// handler turns into a re-theme.
	bg, palette := detectTerminalState(os.Stdin, os.Stdout)
	SetActivePalette(palette)

	model := NewModel(a, themeName)
	// Bubbletea renders on a frame-rate ticker (default 60 FPS). At 60
	// FPS only ~16 ms is spent per visible frame, so when the user
	// holds a navigation key the highlight steps once per ~16 ms even
	// though our Update+View takes <1 ms. Bumping to the max (120 FPS)
	// halves the perceived step latency under key repeat without
	// changing app code paths.
	p := tea.NewProgram(model, tea.WithFPS(120))

	if bg != nil {
		go func() { p.Send(tea.BackgroundColorMsg{Color: bg}) }()
	}

	_, err := p.Run()
	return err
}
