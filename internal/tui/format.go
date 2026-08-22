package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

const (
	defaultLinesPerHour = 4
	totalHours          = 24
	timeLabelWidth      = 8
)

// CalendarEvent is the render-only view of an event inside the month grid.
// Callers resolve colors and other domain data before they pass these in.
type CalendarEvent struct {
	ID        int64
	Title     string
	Color     string // hex like "#a6e3a1"; empty → default muted background
	AllDay    bool
	Day       time.Time // local day the event should render on
	StartTime time.Time
	EndTime   time.Time
}

type placedEvent struct {
	event      CalendarEvent
	col        int
	startRow   int
	endRow     int
	subCol     int
	numSubCols int
}

func resolveOverlaps(placed []placedEvent) {
	for col := range 7 {
		var idxs []int
		for i, p := range placed {
			if p.col == col {
				idxs = append(idxs, i)
			}
		}
		if len(idxs) == 0 {
			continue
		}

		sort.Slice(idxs, func(a, b int) bool {
			pa, pb := placed[idxs[a]], placed[idxs[b]]
			if pa.startRow != pb.startRow {
				return pa.startRow < pb.startRow
			}
			return pa.endRow > pb.endRow
		})

		type cluster struct {
			end  int
			idxs []int
		}
		var clusters []cluster

		for _, idx := range idxs {
			p := placed[idx]
			merged := false
			for ci := range clusters {
				if p.startRow < clusters[ci].end {
					clusters[ci].idxs = append(clusters[ci].idxs, idx)
					if p.endRow > clusters[ci].end {
						clusters[ci].end = p.endRow
					}
					merged = true
					break
				}
			}
			if !merged {
				clusters = append(clusters, cluster{end: p.endRow, idxs: []int{idx}})
			}
		}

		for _, cl := range clusters {
			n := len(cl.idxs)
			for sub, idx := range cl.idxs {
				placed[idx].subCol = sub
				placed[idx].numSubCols = n
			}
		}
	}
}

func placeEvents(events []CalendarEvent, colFn func(CalendarEvent) int, lph int) []placedEvent {
	var placed []placedEvent
	totalRows := totalHours * lph
	for _, ev := range events {
		if ev.AllDay {
			continue
		}
		col := colFn(ev)
		if col < 0 {
			continue
		}
		startRow := ev.StartTime.Hour()*lph + ev.StartTime.Minute()*lph/60
		endRow := startRow + 1
		if !ev.EndTime.IsZero() {
			endRow = ev.EndTime.Hour()*lph + ev.EndTime.Minute()*lph/60
		}
		if endRow <= startRow {
			endRow = startRow + 1
		}
		if endRow > totalRows {
			endRow = totalRows
		}
		placed = append(placed, placedEvent{
			event:    ev,
			col:      col,
			startRow: startRow,
			endRow:   endRow,
		})
	}
	return placed
}

func findPlacedEvents(placed []placedEvent, row, col int) []placedEvent {
	var result []placedEvent
	for _, p := range placed {
		if p.col == col && row >= p.startRow && row < p.endRow {
			result = append(result, p)
		}
	}
	return result
}

func hitSubCol(matches []placedEvent, xInCol, totalWidth int) int64 {
	n := matches[0].numSubCols
	widths := make([]int, n)
	base := totalWidth / n
	rem := totalWidth - base*n
	for i := range n {
		widths[i] = base
		if i < rem {
			widths[i]++
		}
	}
	offset := 0
	for sub := range n {
		if xInCol >= offset && xInCol < offset+widths[sub] {
			for _, m := range matches {
				if m.subCol == sub {
					return m.event.ID
				}
			}
			return 0
		}
		offset += widths[sub]
	}
	return 0
}

func renderTimeCellContent(p placedEvent, row, width int) string {
	relRow := row - p.startRow
	bg := ActiveTheme().Muted
	fg := oklch.ContrastingFg(bg)
	if p.event.Color != "" {
		bg = lipgloss.Color(p.event.Color)
		fg = oklch.ContrastingFg(bg)
	}

	var text string
	switch relRow {
	case 0:
		text = " " + p.event.Title
	case 1:
		if !p.event.EndTime.IsZero() {
			text = " " + p.event.StartTime.Format("15:04") + "-" + p.event.EndTime.Format("15:04")
		}
	}

	text = truncateTo(text, width)

	return lipgloss.NewStyle().Background(bg).Foreground(fg).Width(width).Render(text)
}

func renderOverlappingCells(matches []placedEvent, row, totalWidth int) string {
	sort.Slice(matches, func(a, b int) bool {
		return matches[a].subCol < matches[b].subCol
	})

	n := matches[0].numSubCols
	widths := make([]int, n)
	base := totalWidth / n
	rem := totalWidth - base*n
	for i := range n {
		widths[i] = base
		if i < rem {
			widths[i]++
		}
	}

	active := make(map[int]placedEvent)
	for _, m := range matches {
		active[m.subCol] = m
	}

	var b strings.Builder
	for sub := range n {
		if p, ok := active[sub]; ok {
			b.WriteString(renderTimeCellContent(p, row, widths[sub]))
		} else {
			b.WriteString(strings.Repeat(" ", widths[sub]))
		}
	}
	return b.String()
}

func renderTimeLabel(row, lph int, isNowRow bool, nowTimeLabel string) string {
	if isNowRow {
		s := lipgloss.NewStyle().Foreground(ActiveTheme().Today).Bold(true)
		return s.Render(fmt.Sprintf("  %s", nowTimeLabel)) + " "
	}
	if row%lph == 0 {
		s := lipgloss.NewStyle().Faint(true)
		return s.Render(fmt.Sprintf("  %02d:00", row/lph)) + " "
	}
	return strings.Repeat(" ", timeLabelWidth)
}

func renderEventPill(ev CalendarEvent, cellW int, faint bool) string {
	text := truncateTo(" "+ev.Title, cellW)

	bg := ActiveTheme().Muted
	if ev.Color != "" {
		bg = lipgloss.Color(ev.Color)
	}
	if faint {
		bg = oklch.Dim(bg, 0.78)
	}
	fg := oklch.ContrastingFg(bg)
	return lipgloss.NewStyle().Background(bg).Foreground(fg).
		Width(cellW).Render(text)
}
