package tui

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// View renders the complete dialog (border, title, body, help row).
func (m ListDialogModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	title := m.renderTitleRow(innerW)
	helpText := m.renderHelpLine(innerW)

	var body string
	if m.isNarrow() {
		body = m.viewStacked(innerW, bodyH)
	} else {
		body = m.viewColumns(innerW, bodyH)
	}

	// Build the framed dialog manually instead of going through
	// lipgloss.NewStyle().Border().Render(content): that path forces
	// lipgloss to re-wrap and re-measure every grapheme of the styled
	// content, which is by far the biggest single cost on a dense
	// dialog (96+ rows). Each content line is already innerW cells
	// wide, so we can splice them between hand-built border + padding
	// strings and skip the global measurement pass entirely.
	blank := strings.Repeat(" ", innerW)
	contentLines := make([]string, 0, innerH)
	contentLines = append(contentLines, title, m.renderSubtitle(innerW))
	contentLines = append(contentLines, strings.Split(body, "\n")...)

	contentLines = append(contentLines, blank, helpText)
	return framedDialog(boxW, contentLines)
}

// framedDialog wraps innerLines with the rounded border + (1,2,0,1)
// padding the dialog has always used. innerLines MUST already be at the
// inner content width (boxW - 5). The title row, helpText, and the
// row-zipped body are all width-padded by their producers. A skip of
// the per-line measurement here is then safe. It also saves ~25% of View
// on a dense dialog because lipgloss.Width is the single biggest cost.
func framedDialog(boxW int, innerLines []string) string {
	const (
		padLeft  = 1
		padRight = 2
	)
	innerW := boxW - 2 - padLeft - padRight
	top := "╭" + strings.Repeat("─", boxW-2) + "╮"
	bottom := "╰" + strings.Repeat("─", boxW-2) + "╯"
	leftPad := strings.Repeat(" ", padLeft)
	rightPad := strings.Repeat(" ", padRight)
	emptyRow := "│" + leftPad + strings.Repeat(" ", innerW) + rightPad + "│"

	var b strings.Builder
	b.Grow(boxW * (len(innerLines) + 3))
	b.WriteString(top)
	b.WriteByte('\n')
	b.WriteString(emptyRow)
	b.WriteByte('\n')
	for _, line := range innerLines {
		b.WriteString("│")
		b.WriteString(leftPad)
		b.WriteString(line)
		b.WriteString(rightPad)
		b.WriteString("│\n")
	}
	b.WriteString(bottom)
	return b.String()
}

func (m *ListDialogModel) viewColumns(innerW, bodyH int) string {
	listW := listColumnWidth(innerW)
	detailsW := detailColumnWidth(innerW)

	m.adjustScroll(bodyH)
	// All three column renderers now produce lines that are exactly w
	// cells wide (renderList via padLines, renderDivider by construction,
	// renderDetails by padding its variable-width parts internally), so
	// we can split each without the per-line lipgloss.Width measurement
	// splitAndPad would do.
	listCol := trustedSplit(m.renderList(listW, bodyH), listW, bodyH)
	dividerCol := trustedSplit(m.renderDivider(dialogDividerWidth, bodyH), dialogDividerWidth, bodyH)
	detailsCol := trustedSplit(m.renderDetails(detailsW, bodyH), detailsW, bodyH)

	// Manual row-zip: lipgloss.JoinHorizontal re-measures every grapheme
	// of every line in every column to align them. We've already padded
	// each column to its fixed cell width, so straight concatenation
	// produces the same visual output without the measurement pass.
	var b strings.Builder
	b.Grow((listW + dialogDividerWidth + detailsW + 1) * bodyH)
	for i := 0; i < bodyH; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(listCol[i])
		b.WriteString(dividerCol[i])
		b.WriteString(detailsCol[i])
	}
	return b.String()
}

// trustedSplit splits s by newlines and returns exactly h rows. Callers
// must guarantee every emitted line is already w cells wide. Rows that
// are gone are filled with blanks. It skips lipgloss.Width entirely.
// That is the whole point of the "trusted" name. The fast path is ~60%
// faster than splitAndPad on width-correct input.
func trustedSplit(s string, w, h int) []string {
	out := make([]string, h)
	if h <= 0 {
		return out
	}
	lines := strings.Split(s, "\n")
	n := len(lines)
	if n > h {
		n = h
	}
	for i := 0; i < n; i++ {
		out[i] = lines[i]
	}
	if n < h {
		blank := strings.Repeat(" ", w)
		for i := n; i < h; i++ {
			out[i] = blank
		}
	}
	return out
}

func (m *ListDialogModel) viewStacked(innerW, bodyH int) string {
	rowCount := max(len(m.rows), 1)
	listH := min(max(rowCount+1, 3), max(bodyH/3, 3))
	detailsH := max(bodyH-listH-1, 3)

	m.adjustScroll(listH)
	list := m.renderList(innerW, listH)
	sep := lipgloss.NewStyle().Faint(true).Width(innerW).
		Render(strings.Repeat("─", innerW))
	details := m.renderDetails(innerW, detailsH)

	return lipgloss.JoinVertical(lipgloss.Left, list, sep, details)
}

func (m *ListDialogModel) adjustScroll(visibleH int) {
	// When the list overflows, renderList reserves the last visible row
	// for the scroll indicator (e.g. "5/96 ▼"), overwriting whatever row
	// would otherwise sit there. Treat that slot as out of bounds for the
	// selection so the highlighted row never lands on it.
	contentH := visibleH
	if len(m.rows) > visibleH && contentH > 1 {
		contentH = visibleH - 1
	}
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+contentH {
		m.scroll = m.selected - contentH + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m ListDialogModel) renderList(w, h int) string {
	if len(m.rows) == 0 {
		if m.emptyList == "" {
			return padLines(nil, w, h)
		}
		msg := lipgloss.NewStyle().Faint(true).Render(m.emptyList)
		return padLines([]string{msg}, w, h)
	}

	total := len(m.rows)
	visibleStart := m.scroll
	visibleEnd := min(visibleStart+h, total)

	lines := make([]string, 0, h)
	for i := visibleStart; i < visibleEnd; i++ {
		lines = append(lines, renderListRow(m.rows[i], w, i == m.selected, m.focusZone == ListZoneList, m.selectedColor))
	}

	if total > h {
		indicator := fmt.Sprintf("%d/%d ", m.selected+1, total)
		arrows := ""
		if m.scroll > 0 {
			arrows += "▲"
		}
		if visibleEnd < total {
			if arrows != "" {
				arrows += " "
			}
			arrows += "▼"
		}
		if arrows != "" {
			indicator += arrows + " "
		}
		indicator = truncateTo(indicator, w)

		faintIndicator := lipgloss.NewStyle().Faint(true).Render(indicator)
		if len(lines) >= h {
			lines[h-1] = faintIndicator
		} else {
			lines = append(lines, faintIndicator)
		}
	}

	return padLines(lines, w, h)
}

func (m ListDialogModel) renderDivider(w, h int) string {
	bar := lipgloss.NewStyle().Faint(true).Render("│")
	pad := strings.Repeat(" ", (w-1)/2)
	rest := strings.Repeat(" ", w-len(pad)-1)
	line := pad + bar + rest
	lines := make([]string, h)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func (m ListDialogModel) renderActions(w int) string {
	key := m.actionsCacheKey(w)
	if m.cache != nil && key == m.cache.actionsKey && m.cache.actions != "" {
		return m.cache.actions
	}
	bs := DefaultButtonStyles()
	parts := make([]string, len(m.actions))
	for i, a := range m.actions {
		focused := !a.Disabled && m.focusZone == ListZoneActions && i == m.focusedAction
		switch {
		case a.Danger:
			parts[i] = bs.Danger.Render(a.Label, focused)
		default:
			parts[i] = bs.Normal.Render(a.Label, focused)
		}
		if a.Disabled {
			parts[i] = lipgloss.NewStyle().Faint(true).Render(parts[i])
		}
	}

	out := truncateTo(strings.Join(parts, " "), w)
	if m.cache != nil {
		m.cache.actionsKey = key
		m.cache.actions = out
	}
	return out
}

// renderHelpLine produces the centered short-help line at the bottom
// of the dialog. The result is memoized. shortHelp only changes when
// the caller swaps focus zones or transitions between empty/non-empty
// states. The cache then skips a full lipgloss render (and the bubbles
// help layout it wraps) on every key press while the user scrolls.
func (m ListDialogModel) renderHelpLine(innerW int) string {
	key := m.helpCacheKey(innerW)
	if m.cache != nil && key == m.cache.helpKey && m.cache.help != "" {
		return m.cache.help
	}
	m.help.SetWidth(innerW)
	out := lipgloss.NewStyle().
		Width(innerW).
		Align(lipgloss.Center).
		Render(m.help.ShortHelpView(m.shortHelp))
	if m.cache != nil {
		m.cache.helpKey = key
		m.cache.help = out
	}
	return out
}

// helpCacheKey fingerprints the inputs that affect renderHelpLine: the
// inner width and every binding's key + help text. shortHelp is
// rebuilt by the caller on each refresh so identity comparison would
// always miss. A content-based fingerprint hits whenever the rendered
// output would be identical.
func (m ListDialogModel) helpCacheKey(innerW int) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(innerW))
	h.Write(buf[:])
	for _, b := range m.shortHelp {
		help := b.Help()
		h.Write([]byte(help.Key))
		h.Write([]byte{0})
		h.Write([]byte(help.Desc))
		h.Write([]byte{0})
		var flags byte
		if b.Enabled() {
			flags = 1
		}
		h.Write([]byte{flags})
	}
	return h.Sum64()
}

// actionsCacheKey returns a 64-bit fingerprint of every input that
// affects renderActions' output. Each Set* on the model that touches
// one of those inputs naturally changes the fingerprint. The cache
// then invalidates lazily with no eager records.
func (m ListDialogModel) actionsCacheKey(w int) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(w))
	h.Write(buf[:])
	binary.LittleEndian.PutUint64(buf[:], uint64(m.focusZone))
	h.Write(buf[:])
	binary.LittleEndian.PutUint64(buf[:], uint64(m.focusedAction))
	h.Write(buf[:])
	for _, a := range m.actions {
		h.Write([]byte(a.Label))
		var flags byte
		if a.Danger {
			flags |= 1
		}
		if a.Primary {
			flags |= 2
		}
		if a.Disabled {
			flags |= 4
		}

		h.Write([]byte{flags, 0})
	}
	return h.Sum64()
}

func (m *ListDialogModel) renderDetails(w, h int) string {
	lines := m.detailLines
	if len(m.rows) == 0 {
		lines = m.emptyDetails
	}

	bodyH := h
	var pinned string
	if m.hasPinnedTitle() {
		pinned = paneTitle(m.detailTitle, w)
		bodyH = max(bodyH-2, 1)
	}

	if len(m.actions) == 0 {
		m.body.SetWidth(w)
		m.body.SetHeight(bodyH)
		m.body.SetContentLines(lines)
		if pinned != "" {
			return pinned + "\n" + m.body.View()
		}
		return m.body.View()
	}

	bodyH = max(bodyH-2, 1)
	m.body.SetWidth(w)
	m.body.SetHeight(bodyH)
	m.body.SetContentLines(lines)

	parts := make([]string, 0, 4)
	if pinned != "" {
		parts = append(parts, pinned)
	}
	// renderActions returns ≤ w cells (truncateTo); pad it so the whole
	// detail column is width-correct and viewColumns can trustedSplit it
	// without re-measuring every body line.
	parts = append(parts, m.body.View(), m.actionsSeparator(w), padTrailing(m.renderActions(w), w))
	return strings.Join(parts, "\n")
}

// actionsSeparator renders the faint rule that sits between the detail
// body and the action bar. When the body has scrolled-away content above
// or below, a centered "↑↓ more" hint is embedded in the rule to advertise
// the scroll affordance. That is the same treatment used in the
// single-event dialog.
func (m ListDialogModel) actionsSeparator(w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	hint := m.scrollHint()
	hw := lipgloss.Width(hint)
	if hint == "" || w <= hw+2 {
		return faint.Render(strings.Repeat("─", w))
	}
	left := (w - hw - 2) / 2
	right := w - hw - 2 - left
	return faint.Render(strings.Repeat("─", left)) + " " + faint.Render(hint) + " " + faint.Render(strings.Repeat("─", right))
}

// scrollHint returns "↓ more" / "↑ more" / "↑↓ more" based on what
// the user can still scroll to. Empty when the body fits with no scroll.
func (m ListDialogModel) scrollHint() string {
	if !m.bodyOverflows() {
		return ""
	}
	switch {
	case m.body.AtTop():
		return "↓ more"
	case m.body.AtBottom():
		return "↑ more"
	default:
		return "↑↓ more"
	}
}

// bodyOverflows reports whether the detail body has more content than
// the viewport can show at once.
func (m ListDialogModel) bodyOverflows() bool {
	return m.body.TotalLineCount() > m.body.VisibleLineCount()
}

func (m ListDialogModel) renderSubtitle(innerW int) string {
	return lipgloss.NewStyle().
		Faint(true).
		Width(innerW).
		Render(truncateTo(m.subtitle, innerW))
}

// renderTitleRow composes the bold title with optional quiet context and an
// optional action at the right edge. Context truncates before either control.
func (m ListDialogModel) renderTitleRow(innerW int) string {
	var button string
	if m.titleAction != nil {
		focused := !m.titleAction.Disabled && m.focusZone == ListZoneTitleAction
		button = renderTitleActionButton(*m.titleAction, focused)
	}

	buttonW := lipgloss.Width(button)
	titleNeed := min(lipgloss.Width(m.title), max(innerW-buttonW, 0))
	contextBudget := max(innerW-buttonW-titleNeed, 0)
	context := ""
	if m.titleContext != "" {
		separatorW := 2
		if button != "" {
			separatorW++
		}
		if contextBudget > separatorW {
			context = truncateTo(m.titleContext, contextBudget-separatorW)
		}
	}

	right := button
	if context != "" {
		renderedContext := lipgloss.NewStyle().Faint(true).Render(context)
		right = "  " + renderedContext
		if button != "" {
			right += " " + button
		}
	}
	titleW := max(innerW-lipgloss.Width(right), 0)
	title := lipgloss.NewStyle().
		Bold(true).
		Width(titleW).
		Render(truncateTo(m.title, titleW))
	return lipgloss.JoinHorizontal(lipgloss.Top, title, right)
}

// renderTitleActionButton renders a title-line button without the extra
// margin-right cell used by action-bar buttons. It then sits flush with the
// dialog's right edge.
func renderTitleActionButton(a ListDialogAction, focused bool) string {
	bs := DefaultButtonStyles().Normal
	style := bs.Normal
	if focused && !a.Disabled {
		style = bs.Focused
	}
	out := style.UnsetMarginRight().Render(a.Label)
	if a.Disabled {
		out = lipgloss.NewStyle().Faint(true).Render(out)
	}
	return out
}
