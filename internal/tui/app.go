package tui

import (
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
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
)

type appKeyMap struct {
	Quit           key.Binding
	MonthView      key.Binding
	WeekView       key.Binding
	DayView        key.Binding
	Sidebar        key.Binding
	Create         key.Binding
	SwitchFocus    key.Binding
	Help           key.Binding
	Palette        key.Binding
	CalendarCreate key.Binding
	CalendarList   key.Binding
	Sync           key.Binding
}

func defaultAppKeys() appKeyMap {
	return appKeyMap{
		Quit:           key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		MonthView:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "month")),
		WeekView:       key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "week")),
		DayView:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "day")),
		Sidebar:        key.NewBinding(key.WithKeys("\\"), key.WithHelp("\\", "sidebar")),
		Create:         key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new")),
		SwitchFocus:    key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "switch focus")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Palette:        key.NewBinding(key.WithKeys("/", "ctrl+p", "ctrl+k"), key.WithHelp("/", "commands")),
		CalendarCreate: key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "new calendar")),
		CalendarList:   key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "calendars")),
		Sync:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
	}
}

// compositeKeyMap merges app-level and view-level keybindings for the help view.
type compositeKeyMap struct {
	viewShort []key.Binding
	viewFull  [][]key.Binding
	appKeys   appKeyMap
}

func (c compositeKeyMap) ShortHelp() []key.Binding {
	return append(c.viewShort,
		c.appKeys.Create,
		c.appKeys.Sidebar,
		c.appKeys.MonthView,
		c.appKeys.WeekView,
		c.appKeys.DayView,
		c.appKeys.Sync,
		c.appKeys.Palette,
		c.appKeys.Help,
		c.appKeys.Quit,
	)
}

func (c compositeKeyMap) FullHelp() [][]key.Binding {
	appGroup := []key.Binding{
		c.appKeys.Create,
		c.appKeys.MonthView,
		c.appKeys.WeekView,
		c.appKeys.DayView,
		c.appKeys.Sidebar,
		c.appKeys.CalendarCreate,
		c.appKeys.CalendarList,
		c.appKeys.Sync,
		c.appKeys.SwitchFocus,
		c.appKeys.Palette,
		c.appKeys.Help,
		c.appKeys.Quit,
	}
	return append(c.viewFull, appGroup)
}

type eventsLoadedMsg struct {
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
	err error
}

type calendarMutationDoneMsg struct{ err error }

type calendarDeleteCountMsg struct {
	id         int64
	name       string
	eventCount int64
}

type eventEditLoadedMsg struct {
	event event.Event
	err   error
}

type eventViewLoadedMsg struct {
	event event.Event
	err   error
}

type eventUpdatedMsg struct {
	err error
}

type eventDeletedMsg struct {
	err error
}

// SyncAllRequestedMsg asks the app to sync every connected calendar.
type SyncAllRequestedMsg struct{}

// SyncCalendarRequestedMsg asks the app to sync a single calendar.
type SyncCalendarRequestedMsg struct {
	ID   int64
	Name string
}

// syncFinishedMsg is emitted when a sync run completes.
type syncFinishedMsg struct {
	summary string
	err     error
	reload  bool
}

// syncStatusExpiredMsg clears the footer status line after a delay. The token
// is compared against the current statusToken so a newer status isn't wiped
// by an old tick.
type syncStatusExpiredMsg struct {
	token int
}

type Model struct {
	app             *app.App
	theme           Theme
	keys            appKeyMap
	help            help.Model
	width           int
	height          int
	viewMode        viewMode
	calendar        CalendarModel
	week            WeekModel
	day             DayModel
	events          []event.Event
	calendars       map[int64]CalendarInfo
	dialog          EventDialogModel
	dialogOpen      bool
	viewDialog      EventViewDialogModel
	viewDialogOpen  bool
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
	err             error
	ready           bool
	showSidebar     bool
	focus           appFocus
	hiddenCalendars map[int64]bool
	clickedEventID  int64

	sidebar               SidebarModel
	calendarDialog        CalendarDialogModel
	calendarDialogOpen    bool
	pendingCalendarDelete int64

	calendarListDialog     CalendarListDialogModel
	calendarListDialogOpen bool

	// miniMonthEvents caches the raw events for the sidebar mini-month's
	// displayed month so visibility toggles can re-filter without a DB hit.
	miniMonthEvents []event.Event

	// syncStatus is a transient footer line shown during/after a sync run.
	// statusToken is bumped whenever the status changes so stale Tick
	// expirations can tell whether they still own the current line.
	syncStatus  string
	statusToken int
	syncing     bool
}

func NewModel(a *app.App) Model {
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
	}
	sb := NewSidebarModel(NewMiniMonthModel(now), NewCalendarListModel(nil, hidden))
	return Model{
		app:             a,
		keys:            defaultAppKeys(),
		help:            help.New(),
		viewMode:        vm,
		calendar:        NewCalendarModel(now),
		week:            NewWeekModel(now),
		day:             NewDayModel(now),
		showSidebar:     ui.ShowSidebar,
		hiddenCalendars: hidden,
		focus:           focusCalendar,
		sidebar:         sb,
	}
}

func (m Model) loadEvents() tea.Cmd {
	var from, to time.Time
	switch m.viewMode {
	case viewDay:
		d := m.day.Cursor()
		from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		to = from.AddDate(0, 0, 1)
	case viewWeek:
		start := m.week.WeekStartDate()
		from = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		to = from.AddDate(0, 0, 7)
	default:
		month := m.calendar.Month()
		from = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
		to = from.AddDate(0, 1, 0)
	}
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
		return eventsLoadedMsg{events: events, err: err}
	}
	// The mini-month shows a full month regardless of the main view's range,
	// so refresh its per-day event counts alongside every main reload.
	return tea.Batch(mainCmd, m.loadMiniMonthEvents())
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
		out = append(out, CalendarEvent{
			ID:        e.ID,
			Title:     e.Title,
			AllDay:    e.AllDay,
			Day:       eventDay(e),
			Color:     calendars[e.CalendarID].Color,
			StartTime: eventDay(e),
			EndTime:   e.EndTime.Local(),
		})
	}
	return out
}

func (m Model) loadCalendars() tea.Cmd {
	return func() tea.Msg {
		cals, err := m.app.Calendars.List(context.Background())
		if err != nil {
			return calendarsLoadedMsg{err: err}
		}
		info := make(map[int64]CalendarInfo, len(cals))
		for _, c := range cals {
			info[c.ID] = CalendarInfo{Name: c.Name, Color: c.Color, OwnerEmail: c.OwnerEmail}
		}
		return calendarsLoadedMsg{calendars: info}
	}
}

// newSyncService builds a sync.Service using the app's shared SQLite handle.
// Logs are discarded so sync work doesn't clobber the rendered TUI; users
// run `chroncal sync run` from a shell if they need verbose output.
func (m Model) newSyncService() (*syncpkg.Service, error) {
	credStore, err := auth.NewCredentialStore(true)
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return syncpkg.NewService(m.app.DB, m.app.Queries, credStore, m.app.Calendars, m.app.Events, m.app.Todos, m.app.Journals, logger), nil
}

func (m Model) runSyncAll() tea.Cmd {
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		results, err := svc.SyncAll(ctx, syncpkg.ConflictServerWins)
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		if len(results) == 0 {
			return syncFinishedMsg{summary: "No connected calendars to sync", reload: false}
		}
		var pushed, pulled, deleted, conflicts, errCount int
		var firstErr error
		for _, r := range results {
			pushed += r.Pushed
			pulled += r.Pulled
			deleted += r.Deleted
			conflicts += r.Conflicts
			errCount += len(r.Errors)
			if firstErr == nil && len(r.Errors) > 0 {
				firstErr = r.Errors[0]
			}
		}
		summary := fmt.Sprintf("Synced %d calendar(s) · pushed %d · pulled %d · deleted %d · conflicts %d",
			len(results), pushed, pulled, deleted, conflicts)
		if errCount > 0 {
			return syncFinishedMsg{summary: summary, err: firstErr, reload: true}
		}
		return syncFinishedMsg{summary: summary, reload: true}
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
		summary := fmt.Sprintf("Synced %s · pushed %d · pulled %d · deleted %d · conflicts %d",
			label, result.Pushed, result.Pulled, result.Deleted, result.Conflicts)
		var firstErr error
		if len(result.Errors) > 0 {
			firstErr = result.Errors[0]
		}
		return syncFinishedMsg{summary: summary, err: firstErr, reload: true}
	}
}

func (m Model) expireStatusAfter(d time.Duration, token int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return syncStatusExpiredMsg{token: token}
	})
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
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.loadEvents(), m.loadCalendars())
}

const sidebarWidth = 24

func (m Model) footerHeight() int {
	h := 1
	if m.help.ShowAll {
		h = 8
	}
	if m.syncStatus != "" {
		h++
	}
	return h
}

func (m Model) mainDims() (int, int) {
	padding := 1
	contentHeight := m.height - m.footerHeight() - padding*2
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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// When the palette is open, it captures all input. Only specific
	// parent-level messages (size, theme, palette-result) fall through.
	if m.paletteOpen {
		switch msg.(type) {
		case PaletteSelectedMsg, PaletteClosedMsg,
			tea.BackgroundColorMsg, tea.WindowSizeMsg:
			// fall through to main switch
		default:
			if kp, ok := msg.(tea.KeyPressMsg); ok && kp.String() == "ctrl+c" {
				return m, tea.Quit
			}
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
			calendarDeleteCountMsg,
			tea.BackgroundColorMsg, tea.WindowSizeMsg,
			eventsLoadedMsg, calendarsLoadedMsg,
			calendarMutationDoneMsg:
			// fall through to main switch
		default:
			if kp, ok := msg.(tea.KeyPressMsg); ok && kp.String() == "ctrl+c" {
				return m, tea.Quit
			}
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
			if kp, ok := msg.(tea.KeyPressMsg); ok && kp.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(msg)
			return m, cmd
		}
	}

	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = NewTheme(msg.IsDark())
		m.calendar = m.calendar.SetSelectedColor(m.theme.Text)
		m.week = m.week.SetSelectedColor(m.theme.Text)
		m.day = m.day.SetSelectedColor(m.theme.Text)
		m.sidebar = m.sidebar.SetTheme(m.theme)
		m.help = newThemedHelp(m.theme)
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		iw, ih := m.innerDims()
		m.calendar = m.calendar.SetSize(iw, ih)
		m.week = m.week.SetSize(iw, ih)
		m.day = m.day.SetSize(iw, ih)
		m.dialog = m.dialog.SetSize(m.width, m.height)
		m.viewDialog = m.viewDialog.SetSize(m.width, m.height)
		m.confirmDialog = m.confirmDialog.SetSize(m.width, m.height)
		m.choiceDialog = m.choiceDialog.SetSize(m.width, m.height)
		m.form = m.form.SetSize(m.width, m.height)
		m.palette = m.palette.SetSize(m.width, m.height)
		m.calendarListDialog = m.calendarListDialog.SetSize(m.width, m.height)
		m.ready = true
		return m, nil

	case eventsLoadedMsg:
		m.err = msg.err
		m.events = msg.events
		calEvents := eventsToCalendar(msg.events, m.calendars, m.hiddenCalendars)
		switch m.viewMode {
		case viewDay:
			m.day = m.day.SetEvents(calEvents)
		case viewWeek:
			m.week = m.week.SetEvents(calEvents)
		default:
			m.calendar = m.calendar.SetEvents(calEvents)
		}
		if m.dialogOpen {
			dayEvents := eventsOn(m.events, m.dialog.day)
			m.dialog = m.dialog.SetEvents(dayEvents)
		}
		return m, nil

	case calendarsLoadedMsg:
		if msg.err == nil {
			m.calendars = msg.calendars
			items := make([]CalendarListItem, 0, len(m.calendars))
			for id, c := range m.calendars {
				items = append(items, CalendarListItem{ID: id, Name: c.Name, Color: c.Color})
			}
			slices.SortFunc(items, func(a, b CalendarListItem) int { return strings.Compare(a.Name, b.Name) })
			m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItems(items))
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

	case CalendarDaySelectedMsg:
		dayEvents := eventsOn(m.events, msg.Day)
		m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		if m.clickedEventID > 0 {
			for i, e := range m.dialog.events {
				if e.ID == m.clickedEventID {
					m.dialog.selected = i
					break
				}
			}
			m.clickedEventID = 0
		}
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
			return eventEditLoadedMsg{event: fresh}
		}

	case eventEditLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		var cmd tea.Cmd
		m.form, cmd = NewEventFormModelForEdit(msg.event, m.calendars, m.theme)
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
		m.formOpen = false
		attendees := msg.Attendees
		alarms := msg.Alarms
		if msg.EventID > 0 {
			eventID := msg.EventID
			return m, func() tea.Msg {
				ctx := context.Background()
				_, err := m.app.Events.Update(ctx, eventID, event.UpdateParams{
					CalendarID:     msg.CalendarID,
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
				})
				if err != nil {
					return eventUpdatedMsg{err: err}
				}
				if err = m.app.Events.ReplaceAttendees(ctx, eventID, attendees); err != nil {
					return eventUpdatedMsg{err: err}
				}
				err = m.app.Events.ReplaceAlarms(ctx, eventID, alarms)
				return eventUpdatedMsg{err: err}
			}
		}
		return m, func() tea.Msg {
			ctx := context.Background()
			created, err := m.app.Events.Create(ctx, event.CreateParams{
				CalendarID:     msg.CalendarID,
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
			})
			if err != nil {
				return eventCreatedMsg{err: err}
			}
			if len(attendees) > 0 {
				if err = m.app.Events.ReplaceAttendees(ctx, created.ID, attendees); err != nil {
					return eventCreatedMsg{err: err}
				}
			}
			if len(alarms) > 0 {
				err = m.app.Events.ReplaceAlarms(ctx, created.ID, alarms)
			}
			return eventCreatedMsg{err: err}
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

	case ToggleSidebarMsg:
		return m.toggleSidebar()

	case ToggleHelpMsg:
		m.help.ShowAll = !m.help.ShowAll
		iw, ih := m.innerDims()
		m.calendar = m.calendar.SetSize(iw, ih)
		m.week = m.week.SetSize(iw, ih)
		m.day = m.day.SetSize(iw, ih)
		return m, nil

	case eventCreatedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.viewReturnEvent = event.Event{}
			return m, nil
		}
		cmds := []tea.Cmd{m.loadEvents()}
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
			dayEvents := eventsOn(m.events, msg.Day)
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
			dayEvents := eventsOn(m.events, msg.Day)
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
		dayEvents := eventsOn(m.events, msg.Day)
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
			m.choiceDialog = NewChoiceDialogModel(
				fmt.Sprintf("Delete %q?", msg.Event.Title),
				"This event", "This and following", "All events",
			).SetSize(m.width, m.height)
			m.choiceOpen = true
		} else {
			m.confirmDialog = NewConfirmDialogModel(
				fmt.Sprintf("Delete %q?", msg.Event.Title),
				"Delete",
			).SetSize(m.width, m.height)
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
			}
			if cal.AccountID != 0 {
				if acct, aerr := m.app.Queries.GetAccount(ctx, cal.AccountID); aerr == nil {
					params.RemoteLinked = true
					params.RemoteAuthType = acct.AuthType
					params.RemoteUsername = acct.Username
				}
			}
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

			if saved.RemoteURL != "" {
				credStore, storeErr := auth.NewCredentialStore(true)
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
				if cerr := m.app.Calendars.Connect(ctx, cal, calendar.RemoteLink{
					RemoteURL:     saved.RemoteURL,
					Username:      saved.Username,
					AuthType:      saved.AuthType,
					AllowInsecure: saved.AllowInsecure,
				}, cred, credStore); cerr != nil {
					return calendarMutationDoneMsg{err: cerr}
				}
			}

			return calendarMutationDoneMsg{err: nil}
		}

	case CalendarDisconnectRemoteRequestedMsg:
		id := msg.ID
		return m, func() tea.Msg {
			ctx := context.Background()
			cal, err := m.app.Calendars.Get(ctx, id)
			if err != nil {
				return calendarMutationDoneMsg{err: err}
			}
			credStore, _ := auth.NewCredentialStore(true)
			return calendarMutationDoneMsg{err: m.app.Calendars.Disconnect(ctx, cal, credStore)}
		}

	case CalendarTestRequestedMsg:
		req := msg
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			name, err := caldav.VerifyCalendarURL(ctx, req.URL, req.Username, req.Password, req.AuthType, req.AllowInsecure)
			if err != nil {
				return CalendarTestResultMsg{Message: err.Error()}
			}
			message := "Connected"
			if name != "" {
				message = fmt.Sprintf("Connected · %s", name)
			}
			return CalendarTestResultMsg{OK: true, Message: message}
		}

	case CalendarDeleteRequestedMsg:
		// Fetch the event count before showing the confirm dialog so the
		// user knows how many events will be deleted alongside the calendar.
		id, name := msg.ID, msg.Name
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
		message := fmt.Sprintf("Delete calendar %q?", msg.name)
		if msg.eventCount > 0 {
			if msg.eventCount == 1 {
				message = fmt.Sprintf("Delete calendar %q?\n\n%d event will be deleted", msg.name, msg.eventCount)
			} else {
				message = fmt.Sprintf("Delete calendar %q?\n\n%d events will be deleted", msg.name, msg.eventCount)
			}
		}
		m.confirmDialog = NewConfirmDialogModel(message, "Delete").
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

	case SyncAllRequestedMsg:
		if m.syncing {
			return m, nil
		}
		m.syncing = true
		m.statusToken++
		m.syncStatus = "Syncing all calendars…"
		return m, m.runSyncAll()

	case SyncCalendarRequestedMsg:
		if m.syncing {
			return m, nil
		}
		m.syncing = true
		m.statusToken++
		label := msg.Name
		if label == "" {
			label = "calendar"
		}
		m.syncStatus = fmt.Sprintf("Syncing %s…", label)
		return m, m.runSyncCalendar(msg.ID, msg.Name)

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
		cmds := []tea.Cmd{m.expireStatusAfter(6*time.Second, m.statusToken)}
		if msg.reload {
			cmds = append(cmds, m.loadEvents(), m.loadCalendars())
		}
		return m, tea.Batch(cmds...)

	case syncStatusExpiredMsg:
		if msg.token == m.statusToken && !m.syncing {
			m.syncStatus = ""
		}
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
		if msg.Choice < 0 {
			return m, nil
		}
		ev := m.pendingDelete
		return m, func() tea.Msg {
			var err error
			switch msg.Choice {
			case 0: // This event
				err = m.app.Events.DeleteInstance(context.Background(), ev.UID, ev.StartTime)
			case 1: // This and following
				err = m.app.Events.DeleteFromInstance(context.Background(), ev.UID, ev.StartTime)
			case 2: // All events
				err = m.app.Events.DeleteSeries(context.Background(), ev.UID)
			}
			return eventDeletedMsg{err: err}
		}

	case ConfirmDialogResultMsg:
		m.confirmOpen = false
		if !msg.Confirmed {
			m.pendingCalendarDelete = 0
			return m, nil
		}
		if m.pendingCalendarDelete != 0 {
			id := m.pendingCalendarDelete
			m.pendingCalendarDelete = 0
			// Delete confirmed: close the edit dialog too.
			m.calendarDialogOpen = false
			return m, func() tea.Msg {
				credStore, _ := auth.NewCredentialStore(true)
				err := m.app.Calendars.DeleteWithRemoteCleanup(context.Background(), id, credStore)
				return calendarMutationDoneMsg{err: err}
			}
		}
		ev := m.pendingDelete
		return m, func() tea.Msg {
			err := m.app.Events.Delete(context.Background(), ev.ID)
			return eventDeletedMsg{err: err}
		}

	case eventDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.viewDialogOpen = false
		return m, m.loadEvents()

	case tea.MouseWheelMsg:
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
			default:
				// viewMonth: no wheel scrolling
			}
		}
		return m, nil

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
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
		if m.choiceOpen {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.choiceDialog, cmd = m.choiceDialog.Update(msg)
			return m, cmd
		}
		if m.confirmOpen {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.confirmDialog, cmd = m.confirmDialog.Update(msg)
			return m, cmd
		}
		if m.viewDialogOpen {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.viewDialog, cmd = m.viewDialog.Update(msg)
			return m, cmd
		}
		if m.dialogOpen {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.dialog, cmd = m.dialog.Update(msg)
			return m, cmd
		}
		if m.calendarListDialogOpen {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.calendarListDialog, cmd = m.calendarListDialog.Update(msg)
			return m, cmd
		}
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Palette):
			return m.openPalette()
		case key.Matches(msg, m.keys.Help):
			return m.Update(ToggleHelpMsg{})
		case key.Matches(msg, m.keys.MonthView):
			return m.switchToView(viewMonth)
		case key.Matches(msg, m.keys.WeekView):
			return m.switchToView(viewWeek)
		case key.Matches(msg, m.keys.DayView):
			return m.switchToView(viewDay)
		case key.Matches(msg, m.keys.Sidebar):
			return m.toggleSidebar()
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
			// lands on the last (the "+ Add calendar" row). Once focus is
			// inside the sidebar, the key falls through to m.sidebar.Update
			// which cycles between its internal stops and emits
			// SidebarFocusEscapedMsg to hand focus back to the main view.
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
		sidebarBorder := m.theme.Border
		if m.focus == focusSidebar {
			sidebarBorder = m.theme.Primary
		}
		sb := m.sidebar.SetSize(sidebarWidth-padding*2, contentHeight-padding*2)
		sidebar := lipgloss.NewStyle().
			Width(sidebarWidth).
			Height(contentHeight).
			Padding(padding).
			BorderRight(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(sidebarBorder).
			Foreground(m.theme.Text).
			Render(sb.View())
		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	} else {
		body = main
	}

	m.help.SetWidth(m.width - padding*4)
	helpView := m.help.View(m.currentKeyMap())
	footerContent := helpView
	if m.syncStatus != "" {
		statusColor := m.theme.Primary
		if m.syncing {
			statusColor = m.theme.Muted
		}
		statusLine := lipgloss.NewStyle().
			Foreground(statusColor).
			Render(m.syncStatus)
		footerContent = lipgloss.JoinVertical(lipgloss.Left, statusLine, helpView)
	}
	footer := lipgloss.NewStyle().
		Width(m.width - padding*2).
		Padding(padding).
		Render(footerContent)

	v.Content = lipgloss.JoinVertical(lipgloss.Left, body, footer)

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
	if m.calendarDialogOpen {
		bw, bh := m.calendarDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.calendarDialog.View(), bw, bh)
	}
	if m.choiceOpen {
		bw, bh := m.choiceDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.choiceDialog.View(), bw, bh)
	}
	if m.confirmOpen {
		bw, bh := m.confirmDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.confirmDialog.View(), bw, bh)
	}
	if m.paletteOpen {
		bw, bh := m.palette.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.palette.View(), bw, bh)
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
	default:
		return m.calendar.cursor, m.calendar.today
	}
}

// switchToView changes the active view mode and synchronizes cursor/today.
// Safe to call even when already in the requested mode (no-op).
func (m Model) switchToView(mode viewMode) (tea.Model, tea.Cmd) {
	if m.viewMode == mode {
		return m, nil
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
	}
	return m, m.switchView()
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
	default:
		m.calendar.cursor = today
		m.calendar.today = today
		if m.calendar.month.Year() != today.Year() || m.calendar.month.Month() != today.Month() {
			m.calendar.month = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
		}
	}
	return m, m.loadEvents()
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
		ShowSidebar:     m.showSidebar,
		ViewMode:        vm,
		HiddenCalendars: ids,
	})
}

func (m Model) currentKeyMap() compositeKeyMap {
	appKeys := m.keys
	// Disable the binding for the currently active view.
	switch m.viewMode {
	case viewMonth:
		appKeys.MonthView.SetEnabled(false)
	case viewWeek:
		appKeys.WeekView.SetEnabled(false)
	case viewDay:
		appKeys.DayView.SetEnabled(false)
	}
	// Only show SwitchFocus when sidebar is visible.
	appKeys.SwitchFocus.SetEnabled(m.showSidebar)

	var viewShort []key.Binding
	var viewFull [][]key.Binding
	switch m.viewMode {
	case viewDay:
		k := m.day.keys
		viewShort = k.ShortHelp()
		viewFull = k.FullHelp()
	case viewWeek:
		k := m.week.keys
		viewShort = k.ShortHelp()
		viewFull = k.FullHelp()
	default:
		k := m.calendar.keys
		viewShort = k.ShortHelp()
		viewFull = k.FullHelp()
	}
	return compositeKeyMap{
		viewShort: viewShort,
		viewFull:  viewFull,
		appKeys:   appKeys,
	}
}

func Run(a *app.App) error {
	model := NewModel(a)
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}
