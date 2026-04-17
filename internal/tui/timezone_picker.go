package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ianaTimezones is a curated list of common IANA timezone identifiers.
var ianaTimezones = []string{
	"UTC",
	// Africa
	"Africa/Abidjan",
	"Africa/Accra",
	"Africa/Addis_Ababa",
	"Africa/Algiers",
	"Africa/Cairo",
	"Africa/Casablanca",
	"Africa/Dar_es_Salaam",
	"Africa/Johannesburg",
	"Africa/Lagos",
	"Africa/Nairobi",
	"Africa/Tunis",
	// America
	"America/Anchorage",
	"America/Argentina/Buenos_Aires",
	"America/Bogota",
	"America/Caracas",
	"America/Chicago",
	"America/Denver",
	"America/Edmonton",
	"America/Guatemala",
	"America/Halifax",
	"America/Havana",
	"America/Lima",
	"America/Los_Angeles",
	"America/Manaus",
	"America/Mexico_City",
	"America/Monterrey",
	"America/Montevideo",
	"America/New_York",
	"America/Panama",
	"America/Phoenix",
	"America/Regina",
	"America/Santiago",
	"America/Sao_Paulo",
	"America/St_Johns",
	"America/Toronto",
	"America/Vancouver",
	"America/Winnipeg",
	// Asia
	"Asia/Almaty",
	"Asia/Amman",
	"Asia/Baghdad",
	"Asia/Baku",
	"Asia/Bangkok",
	"Asia/Beirut",
	"Asia/Colombo",
	"Asia/Dhaka",
	"Asia/Dubai",
	"Asia/Ho_Chi_Minh",
	"Asia/Hong_Kong",
	"Asia/Irkutsk",
	"Asia/Istanbul",
	"Asia/Jakarta",
	"Asia/Jerusalem",
	"Asia/Kabul",
	"Asia/Karachi",
	"Asia/Kathmandu",
	"Asia/Kolkata",
	"Asia/Krasnoyarsk",
	"Asia/Kuala_Lumpur",
	"Asia/Kuwait",
	"Asia/Magadan",
	"Asia/Manila",
	"Asia/Muscat",
	"Asia/Novosibirsk",
	"Asia/Riyadh",
	"Asia/Seoul",
	"Asia/Shanghai",
	"Asia/Singapore",
	"Asia/Taipei",
	"Asia/Tashkent",
	"Asia/Tehran",
	"Asia/Tokyo",
	"Asia/Vladivostok",
	"Asia/Yakutsk",
	"Asia/Yekaterinburg",
	// Atlantic
	"Atlantic/Azores",
	"Atlantic/Cape_Verde",
	"Atlantic/Reykjavik",
	// Australia
	"Australia/Adelaide",
	"Australia/Brisbane",
	"Australia/Darwin",
	"Australia/Hobart",
	"Australia/Melbourne",
	"Australia/Perth",
	"Australia/Sydney",
	// Europe
	"Europe/Amsterdam",
	"Europe/Athens",
	"Europe/Belgrade",
	"Europe/Berlin",
	"Europe/Brussels",
	"Europe/Bucharest",
	"Europe/Budapest",
	"Europe/Copenhagen",
	"Europe/Dublin",
	"Europe/Helsinki",
	"Europe/Kyiv",
	"Europe/Lisbon",
	"Europe/London",
	"Europe/Madrid",
	"Europe/Minsk",
	"Europe/Moscow",
	"Europe/Oslo",
	"Europe/Paris",
	"Europe/Prague",
	"Europe/Riga",
	"Europe/Rome",
	"Europe/Samara",
	"Europe/Sofia",
	"Europe/Stockholm",
	"Europe/Tallinn",
	"Europe/Vienna",
	"Europe/Vilnius",
	"Europe/Warsaw",
	"Europe/Zurich",
	// Indian
	"Indian/Maldives",
	"Indian/Mauritius",
	// Pacific
	"Pacific/Auckland",
	"Pacific/Chatham",
	"Pacific/Fiji",
	"Pacific/Guam",
	"Pacific/Honolulu",
	"Pacific/Midway",
	"Pacific/Noumea",
	"Pacific/Pago_Pago",
	"Pacific/Tongatapu",
}

// tzEntry holds a timezone name and its precomputed display label.
type tzEntry struct {
	Name  string // IANA name, e.g. "America/New_York"
	Label string // display label, e.g. "America/New_York  (UTC-04:00)"
}

// tzFocusZone tracks which part of the picker has focus.
type tzFocusZone int

const (
	tzFocusSearch tzFocusZone = iota
	tzFocusList
	tzFocusCancel
	tzFocusOk
)

// TimezonePickerModel is the model for the timezone picker overlay.
type TimezonePickerModel struct {
	all       []tzEntry // full list
	filtered  []tzEntry // filtered subset
	filter    *TextField
	cursor    int         // index in filtered
	offset    int         // scroll offset
	focus     tzFocusZone // current focus zone
	done      bool        // true when selection confirmed
	cancelled bool
	selected  string // final selection (IANA name)
	theme     Theme
}

const tzPickerVisibleRows = 8

// NewTimezonePickerModel creates a new timezone picker with the given
// timezone pre-selected.
func NewTimezonePickerModel(current string, theme Theme) TimezonePickerModel {
	now := time.Now()
	entries := make([]tzEntry, 0, len(ianaTimezones))
	for _, tz := range ianaTimezones {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			continue
		}
		_, off := now.In(loc).Zone()
		label := fmt.Sprintf("%s  (%s)", tz, formatTZOffset(off))
		entries = append(entries, tzEntry{Name: tz, Label: label})
	}

	f := NewTextField("")
	f.SetCharLimit(60)
	f.Focus()

	m := TimezonePickerModel{
		all:    entries,
		filter: f,
		theme:  theme,
	}
	m.applyFilter()

	// Pre-select current timezone in the filtered list.
	for i, e := range m.filtered {
		if e.Name == current {
			m.cursor = i
			m.ensureVisible()
			break
		}
	}

	return m
}


func (m *TimezonePickerModel) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if query == "" {
		m.filtered = make([]tzEntry, len(m.all))
		copy(m.filtered, m.all)
	} else {
		m.filtered = m.filtered[:0]
		for _, e := range m.all {
			if strings.Contains(strings.ToLower(e.Label), query) {
				m.filtered = append(m.filtered, e)
			}
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(len(m.filtered)-1, 0)
	}
	m.ensureVisible()
}

func (m *TimezonePickerModel) ensureVisible() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+tzPickerVisibleRows {
		m.offset = m.cursor - tzPickerVisibleRows + 1
	}
}

// Done reports whether a timezone was selected.
func (m TimezonePickerModel) Done() bool { return m.done }

// Cancelled reports whether the picker was dismissed.
func (m TimezonePickerModel) Cancelled() bool { return m.cancelled }

// Selected returns the IANA timezone name that was selected.
func (m TimezonePickerModel) Selected() string { return m.selected }

// BtnFocus returns the current button focus for rendering in the parent.
func (m TimezonePickerModel) BtnFocus() tzFocusZone { return m.focus }

// Update handles keyboard input for the timezone picker.
func (m TimezonePickerModel) Update(msg tea.Msg) (TimezonePickerModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	switch kp.String() {
	case "esc":
		m.cancelled = true
		return m, nil
	case "tab":
		m.focus = (m.focus + 1) % (tzFocusOk + 1)
		if m.focus == tzFocusSearch {
			m.filter.Focus()
		} else {
			m.filter.Blur()
		}
		return m, nil
	case "shift+tab":
		m.focus = (m.focus - 1 + (tzFocusOk + 1)) % (tzFocusOk + 1)
		if m.focus == tzFocusSearch {
			m.filter.Focus()
		} else {
			m.filter.Blur()
		}
		return m, nil
	case "enter":
		switch m.focus {
		case tzFocusCancel:
			m.cancelled = true
		default:
			if len(m.filtered) > 0 {
				m.selected = m.filtered[m.cursor].Name
				m.done = true
			}
		}
		return m, nil
	case "up", "ctrl+p":
		if m.focus == tzFocusSearch || m.focus == tzFocusList {
			if m.cursor > 0 {
				m.cursor--
				m.ensureVisible()
			}
		}
		return m, nil
	case "down", "ctrl+n":
		if m.focus == tzFocusSearch || m.focus == tzFocusList {
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
				m.ensureVisible()
			}
		}
		return m, nil
	case "pgup":
		m.cursor = max(m.cursor-tzPickerVisibleRows, 0)
		m.ensureVisible()
		return m, nil
	case "pgdown":
		m.cursor = min(m.cursor+tzPickerVisibleRows, max(len(m.filtered)-1, 0))
		m.ensureVisible()
		return m, nil
	}

	// Forward to filter input only when search is focused.
	if m.focus == tzFocusSearch {
		prev := m.filter.Value()
		cmd := m.filter.Update(kp)
		if m.filter.Value() != prev {
			m.applyFilter()
		}
		return m, cmd
	}

	return m, nil
}

// View renders the timezone picker.
func (m TimezonePickerModel) View() string {
	var lines []string

	// Search input with inline label and focus marker.
	label := lipgloss.NewStyle().Faint(true).Render("Search")
	marker := "  "
	if m.focus == tzFocusSearch {
		marker = lipgloss.NewStyle().Faint(true).Render(Glyphs["focus"]) + " "
	}
	lines = append(lines, label+" "+marker+m.filter.View())
	lines = append(lines, "")

	// Timezone list — always emit exactly tzPickerVisibleRows + 1 lines
	// so the buttons stay pinned to the bottom.
	listLines := 0
	if len(m.filtered) == 0 {
		dimStyle := lipgloss.NewStyle().Faint(true)
		lines = append(lines, dimStyle.Render("No matching timezones"))
		listLines++
	} else {
		end := min(m.offset+tzPickerVisibleRows, len(m.filtered))
		for i := m.offset; i < end; i++ {
			entry := m.filtered[i]
			if i == m.cursor {
				lines = append(lines, lipgloss.NewStyle().Reverse(true).Render(entry.Label))
			} else {
				lines = append(lines, entry.Label)
			}
			listLines++
		}
	}
	// Scroll indicator or blank.
	totalListRows := tzPickerVisibleRows + 1
	if m.offset > 0 || (len(m.filtered) > 0 && m.offset+tzPickerVisibleRows < len(m.filtered)) {
		dimStyle := lipgloss.NewStyle().Faint(true)
		indicator := fmt.Sprintf("%d/%d", m.cursor+1, len(m.filtered))
		lines = append(lines, dimStyle.Render(indicator))
		listLines++
	}
	// Pad remaining rows so total is always tzPickerVisibleRows + 1.
	for listLines < totalListRows {
		lines = append(lines, "")
		listLines++
	}

	return strings.Join(lines, "\n")
}
