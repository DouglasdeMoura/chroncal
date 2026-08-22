package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Private

// focusedSelect returns the SelectField behind a generic "select:prev/next"
// arrow click for the given field. It returns nil if the field does not
// currently own a focused select. It unwraps the RecurrenceOnField composite.
// That nested monthly select renders those same generic targets.
func focusedSelect(field FormField) *SelectField {
	switch fld := field.(type) {
	case *SelectField:
		if fld.focused {
			return fld
		}
	case *RecurrenceOnField:
		if fld.mode == RecurrenceOnMonthly && fld.monthly.focused {
			return fld.monthly
		}
	}
	return nil
}

func (f Form) handleClick(target string) (Form, tea.Cmd) {
	if target == "" {
		return f, nil
	}

	// Select arrow clicks: find the SelectField that owns the arrow,
	// focus it, and simulate the corresponding keypress.
	if target == "select:prev" || target == "select:next" {
		key := "right"
		if target == "select:prev" {
			key = "left"
		}
		for i := range f.items {
			// A focused RecurrenceOnField in monthly mode renders its nested
			// monthly select's arrows with these same generic targets.
			// Resolve against it too, not only top-level SelectFields.
			sf := focusedSelect(f.items[i].Field)
			if sf == nil {
				continue
			}
			cmd := sf.Update(keyMsg(key))
			if f.onRebuild != nil {
				f.onRebuild(&f)
			}
			return f, cmd
		}
		return f, nil
	}

	// Index-keyed select arrows: "select:prev:i" / "select:next:i" focus the
	// owning field and apply the keypress in one click, even when the select
	// was unfocused (issue #498).
	if strings.HasPrefix(target, "select:prev:") || strings.HasPrefix(target, "select:next:") {
		key := "right"
		prefix := "select:next:"
		if strings.HasPrefix(target, "select:prev:") {
			key = "left"
			prefix = "select:prev:"
		}
		if idx, err := strconv.Atoi(strings.TrimPrefix(target, prefix)); err == nil &&
			idx >= 0 && idx < len(f.items) {
			if sf, ok := f.items[idx].Field.(*SelectField); ok {
				f, _ = f.focusIndex(idx)
				cmd := sf.Update(keyMsg(key))
				if f.onRebuild != nil {
					f.onRebuild(&f)
				}
				return f, cmd
			}
		}
		return f, nil
	}

	// Palette swatch clicks: "palette:N" selects swatch N and focuses the
	// field. Works for both standalone PaletteField and the composite
	// ColorField.
	if strings.HasPrefix(target, "palette:") {
		if idx, err := strconv.Atoi(strings.TrimPrefix(target, "palette:")); err == nil {
			for i := range f.items {
				switch pf := f.items[i].Field.(type) {
				case *PaletteField:
					pf.selected = idx
					f, cmd := f.focusIndex(i)
					if f.onRebuild != nil {
						f.onRebuild(&f)
					}
					return f, cmd
				case *ColorField:
					pf.palette.SetSelected(idx)
					if v := pf.palette.Value(); v != "" {
						pf.hex.SetValue(v)
					}
					pf.syncFromHex()
					f, cmd := f.focusIndex(i)
					if f.onRebuild != nil {
						f.onRebuild(&f)
					}
					return f, cmd
				}
			}
		}
		return f, nil
	}

	if strings.HasPrefix(target, "recurrenceon:") {
		if idx, err := strconv.Atoi(strings.TrimPrefix(target, "recurrenceon:")); err == nil {
			for i := range f.items {
				if rf, ok := f.items[i].Field.(*RecurrenceOnField); ok {
					rf.ToggleWeekDay(idx)
					if f.onRebuild != nil {
						f.onRebuild(&f)
					}
					return f.focusIndex(i)
				}
			}
		}
		return f, nil
	}

	if strings.HasPrefix(target, "quantityselect:") {
		for i := range f.items {
			if qf, ok := f.items[i].Field.(*QuantitySelectField); ok {
				f, _ = f.focusIndex(i)
				cmd := qf.HandleClickTarget(target)
				if f.onRebuild != nil {
					f.onRebuild(&f)
				}
				return f, cmd
			}
		}
		return f, nil
	}

	for i := range f.items {
		if target == fieldTarget(i) {
			// Mirror Tab, which skips non-focusable fields: a click must not
			// focus or edit a disabled field (issue #497).
			if !f.items[i].Field.IsFocusable() {
				return f, nil
			}
			if cb, ok := f.items[i].Field.(*CheckboxField); ok {
				cb.Toggle()
				if f.onRebuild != nil {
					f.onRebuild(&f)
				}
			}
			return f.focusIndex(i)
		}
	}

	switch target {
	case "submit":
		f.blurCurrent()
		f.focused = f.submitIndex()
		return f.submitIfValid()
	case "cancel":
		f.blurCurrent()
		f.focused = f.cancelIndex()
		if f.onCancel != nil {
			return f, f.onCancel(&f)
		}
	default:
		for i := range f.actionButtons {
			if target == actionTarget(i) {
				f.blurCurrent()
				f.focused = f.actionIndex(i)
				ab := f.actionButtons[i]
				return f, func() tea.Msg { return ab.OnPress() }
			}
		}
	}

	return f, nil
}
