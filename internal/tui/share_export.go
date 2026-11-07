package tui

import (
	"context"
	"errors"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/freebusy"
	"github.com/douglasdemoura/chroncal/internal/sharefreebusy"
)

// handleShareExport validates and exports share files.
func (m Model) handleShareExport(msg ShareExportRequestedMsg) (tea.Model, tea.Cmd) {
	if errText := ValidateShareExport(msg.From, msg.To, msg.Path); errText != "" {
		m.err = errors.New(errText)
		return m, nil
	}
	ids, owners, errText := shareCalendarIDsOwners(m, msg.Calendars)
	if errText != "" {
		m.err = errors.New(errText)
		return m, nil
	}
	if _, statErr := os.Stat(msg.Path); statErr == nil && !m.shareOverwrite {
		m.shareOverwrite = true
		m.err = errors.New("file exists, press Export again to overwrite")
		return m, nil
	}
	m.shareOverwrite = false
	m.shareOpen = false
	from, to, _ := sharefreebusy.CapRange(msg.From, msg.To)
	work := msg.WorkHours
	return m, func() tea.Msg {
		result, err := freebusy.Compute(context.Background(), m.app.Recurrences, from, to, ids, owners)
		if err != nil {
			return ShareExportDoneMsg{Err: err, Path: msg.Path}
		}
		ics, err := sharefreebusy.ExportFreeWork(result.Periods, from, to, organizerForShare(m), work)
		if err != nil {
			return ShareExportDoneMsg{Err: err, Path: msg.Path}
		}
		if err := os.WriteFile(msg.Path, ics, 0o600); err != nil {
			return ShareExportDoneMsg{Err: err, Path: msg.Path}
		}
		free := sharefreebusy.Invert(result.Periods, from, to)
		if work {
			free = sharefreebusy.ClampWorkHours(free)
		}
		if err := os.WriteFile(msg.Path+".txt", []byte(sharefreebusy.FormatText(free)), 0o600); err != nil {
			return ShareExportDoneMsg{Err: err, Path: msg.Path}
		}
		return ShareExportDoneMsg{Path: msg.Path}
	}
}

// shareCalendarIDsOwners returns visible calendar IDs and owners.
// spec is All or a comma name list.
func shareCalendarIDsOwners(m Model, spec string) ([]int64, map[int64]string, string) {
	owners := map[int64]string{}
	ids := make([]int64, 0, len(m.calendars))
	spec = strings.TrimSpace(spec)
	wantAll := spec == "" || strings.EqualFold(spec, "All")
	var want map[string]bool
	if !wantAll {
		want = map[string]bool{}
		for _, name := range strings.Split(spec, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				want[strings.ToLower(name)] = true
			}
		}
		if len(want) == 0 {
			return nil, nil, "Enter calendar names or All"
		}
	}
	for id, cal := range m.calendars {
		if m.hiddenCalendars[id] {
			continue
		}
		if !wantAll && !want[strings.ToLower(cal.Name)] {
			continue
		}
		ids = append(ids, id)
		owners[id] = cal.OwnerEmail
	}
	if len(ids) == 0 {
		return nil, nil, "No matching calendar"
	}
	return ids, owners, ""
}

func organizerForShare(m Model) string {
	for _, cal := range m.calendars {
		if cal.OwnerEmail != "" {
			return cal.OwnerEmail
		}
	}
	return ""
}
