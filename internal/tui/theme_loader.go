package tui

import (
	"embed"
	"fmt"
	"image/color"
	"io/fs"
	"strings"
	"sync"

	lipgloss "charm.land/lipgloss/v2"
	toml "github.com/pelletier/go-toml/v2"
)

//go:embed themes/*.toml
var builtinThemeFS embed.FS

// rawTheme is the TOML shape for a theme file. Each color token is decoded
// as `any` so we can accept both flat strings ("#abc123" / "240") and
// {light,dark} variant tables.
type rawTheme struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`

	// Structural chrome.
	Primary   any `toml:"primary"`
	Secondary any `toml:"secondary"`
	Accent    any `toml:"accent"`
	Muted     any `toml:"muted"`
	Text      any `toml:"text"`
	TextDim   any `toml:"text_dim"`
	Border    any `toml:"border"`
	Today     any `toml:"today"`
	Selected  any `toml:"selected"`
	Surface   any `toml:"surface"`
	Error     any `toml:"error"`

	// Badges.
	BadgeOK      any `toml:"badge_ok"`
	BadgeWarn    any `toml:"badge_warn"`
	BadgeDanger  any `toml:"badge_danger"`
	BadgeInfo    any `toml:"badge_info"`
	BadgeNeutral any `toml:"badge_neutral"`
	BadgeText    any `toml:"badge_text"`

	// Form.
	FormLabel     any `toml:"form_label"`
	FormRequired  any `toml:"form_required"`
	FormError     any `toml:"form_error"`
	FormHighlight any `toml:"form_highlight"`

	// Buttons.
	ButtonPrimaryBg        any `toml:"button_primary_bg"`
	ButtonPrimaryFocusedBg any `toml:"button_primary_focused_bg"`
	ButtonSecondaryBg      any `toml:"button_secondary_bg"`
	ButtonDangerBg         any `toml:"button_danger_bg"`
	ButtonDangerFocusedBg  any `toml:"button_danger_focused_bg"`
	ButtonGhostFg          any `toml:"button_ghost_fg"`
	ButtonText             any `toml:"button_text"`

	// Calendar palette swatches.
	CalendarSwatches []string `toml:"calendar_swatches"`
}

var (
	rawThemeCacheMu sync.RWMutex
	rawThemeCache   = map[string]*rawTheme{}
)

// LoadBuiltinTheme loads a theme embedded into the binary (look under
// internal/tui/themes/*.toml) and resolves light/dark variants against
// hasDarkBG.
func LoadBuiltinTheme(name string, hasDarkBG bool) (Theme, error) {
	raw, err := readBuiltinRaw(name)
	if err != nil {
		return Theme{}, err
	}
	return resolveTheme(raw, hasDarkBG)
}

// BuiltinThemeNames returns the list of embedded theme identifiers.
func BuiltinThemeNames() []string {
	entries, err := fs.ReadDir(builtinThemeFS, "themes")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	return names
}

func readBuiltinRaw(name string) (*rawTheme, error) {
	rawThemeCacheMu.RLock()
	cached, ok := rawThemeCache[name]
	rawThemeCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	data, err := builtinThemeFS.ReadFile("themes/" + name + ".toml")
	if err != nil {
		return nil, fmt.Errorf("theme %q: %w", name, err)
	}
	var raw rawTheme
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("theme %q: parse: %w", name, err)
	}

	rawThemeCacheMu.Lock()
	rawThemeCache[name] = &raw
	rawThemeCacheMu.Unlock()
	return &raw, nil
}

// resolveColor parses a single TOML color value. Accepted shapes:
//
//	"#abc123"                           // flat hex
//	"240"                               // flat ANSI 256 palette index
//	{ light = "...", dark = "..." }     // variant table
func resolveColor(v any, hasDarkBG bool, field string) (color.Color, error) {
	switch x := v.(type) {
	case string:
		return lipgloss.Color(x), nil
	case map[string]any:
		key := "light"
		if hasDarkBG {
			key = "dark"
		}
		s, ok := x[key].(string)
		if !ok {
			return nil, fmt.Errorf("field %q variant missing %q string", field, key)
		}
		return lipgloss.Color(s), nil
	case nil:
		return nil, fmt.Errorf("field %q is missing", field)
	default:
		return nil, fmt.Errorf("field %q: unsupported color value %T", field, v)
	}
}

func resolveTheme(r *rawTheme, hasDarkBG bool) (Theme, error) {
	var firstErr error
	pick := func(field string, v any) color.Color {
		c, err := resolveColor(v, hasDarkBG, field)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return c
	}

	t := Theme{
		Primary:   pick("primary", r.Primary),
		Secondary: pick("secondary", r.Secondary),
		Accent:    pick("accent", r.Accent),
		Muted:     pick("muted", r.Muted),
		Text:      pick("text", r.Text),
		TextDim:   pick("text_dim", r.TextDim),
		Border:    pick("border", r.Border),
		Today:     pick("today", r.Today),
		Selected:  pick("selected", r.Selected),
		Surface:   pick("surface", r.Surface),
		Error:     pick("error", r.Error),

		BadgeOK:      pick("badge_ok", r.BadgeOK),
		BadgeWarn:    pick("badge_warn", r.BadgeWarn),
		BadgeDanger:  pick("badge_danger", r.BadgeDanger),
		BadgeInfo:    pick("badge_info", r.BadgeInfo),
		BadgeNeutral: pick("badge_neutral", r.BadgeNeutral),
		BadgeText:    pick("badge_text", r.BadgeText),

		FormLabel:     pick("form_label", r.FormLabel),
		FormRequired:  pick("form_required", r.FormRequired),
		FormError:     pick("form_error", r.FormError),
		FormHighlight: pick("form_highlight", r.FormHighlight),

		ButtonPrimaryBg:        pick("button_primary_bg", r.ButtonPrimaryBg),
		ButtonPrimaryFocusedBg: pick("button_primary_focused_bg", r.ButtonPrimaryFocusedBg),
		ButtonSecondaryBg:      pick("button_secondary_bg", r.ButtonSecondaryBg),
		ButtonDangerBg:         pick("button_danger_bg", r.ButtonDangerBg),
		ButtonDangerFocusedBg:  pick("button_danger_focused_bg", r.ButtonDangerFocusedBg),
		ButtonGhostFg:          pick("button_ghost_fg", r.ButtonGhostFg),
		ButtonText:             pick("button_text", r.ButtonText),

		CalendarSwatches: append([]string(nil), r.CalendarSwatches...),
	}
	if firstErr != nil {
		return Theme{}, firstErr
	}
	return t, nil
}
