package tui

import (
	"embed"
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"

	lipgloss "charm.land/lipgloss/v2"
	toml "github.com/pelletier/go-toml/v2"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
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
	Today        any `toml:"today"`
	Selected     any `toml:"selected"`
	SelectedText any `toml:"selected_text"`
	Surface      any `toml:"surface"`
	Error        any `toml:"error"`

	// Badges.
	BadgeOK      any `toml:"badge_ok"`
	BadgeWarn    any `toml:"badge_warn"`
	BadgeDanger  any `toml:"badge_danger"`
	BadgeInfo    any `toml:"badge_info"`
	BadgeNeutral any `toml:"badge_neutral"`

	// Form.
	FormLabel     any `toml:"form_label"`
	FormRequired  any `toml:"form_required"`
	FormError     any `toml:"form_error"`
	FormHighlight any `toml:"form_highlight"`

	// Buttons.
	ButtonBg       any `toml:"button_bg"`
	ButtonDangerBg any `toml:"button_danger_bg"`
	ButtonGhostFg  any `toml:"button_ghost_fg"`

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

// LoadTheme resolves a theme by name with a safe fallback to the default.
// An empty name is treated as the default. Unknown or malformed themes log
// the error and fall back to the default so a typo in config.toml cannot
// make the TUI unusable.
func LoadTheme(name string, hasDarkBG bool) Theme {
	if name == "" {
		name = DefaultThemeName
	}
	t, err := LoadBuiltinTheme(name, hasDarkBG)
	if err == nil {
		return t
	}
	fmt.Fprintf(os.Stderr, "theme %q failed to load (%v); falling back to %q\n",
		name, err, DefaultThemeName)
	def, err := LoadBuiltinTheme(DefaultThemeName, hasDarkBG)
	if err != nil {
		panic("built-in default theme failed to load: " + err.Error())
	}
	return def
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
//
// ANSI indices 0..15 are translated to the terminal's actually-rendered
// RGB via activePalette when an OSC 4 response is available — so themes
// can lean on ANSI references (primary = "4") and still benefit from
// exact OKLCh contrast computations against real hex values. Indices
// 16..255 and unrecognized strings fall through to lipgloss.Color.
func resolveColor(v any, hasDarkBG bool, field string) (color.Color, error) {
	switch x := v.(type) {
	case string:
		return resolveString(x), nil
	case map[string]any:
		key := "light"
		if hasDarkBG {
			key = "dark"
		}
		s, ok := x[key].(string)
		if !ok {
			return nil, fmt.Errorf("field %q variant missing %q string", field, key)
		}
		return resolveString(s), nil
	case nil:
		return nil, fmt.Errorf("field %q is missing", field)
	default:
		return nil, fmt.Errorf("field %q: unsupported color value %T", field, v)
	}
}

// resolveString turns a single TOML color string into a color.Color. If
// it's an ANSI index 0..15 and the queried terminal palette has a value
// for that slot, the palette's hex wins; otherwise lipgloss handles it.
func resolveString(s string) color.Color {
	if idx, ok := ansi16Index(s); ok {
		if c := activePalette.Lookup(idx); c != nil {
			return c
		}
	}
	return lipgloss.Color(s)
}

// ansi16Index returns the integer 0..15 if s is a bare decimal in that
// range, otherwise ok=false.
func ansi16Index(s string) (int, bool) {
	if s == "" || len(s) > 2 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 15 {
		return 0, false
	}
	return n, true
}

func resolveTheme(r *rawTheme, hasDarkBG bool) (Theme, error) {
	var firstErr error
	pick := func(field string, v any) color.Color {
		// "auto" is a sentinel for "derive me at the end from other
		// resolved tokens". Returning nil here lets the post-process
		// step below fill it in once Text and Surface are known.
		if isAutoSentinel(v) {
			return nil
		}
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
		Today:        pick("today", r.Today),
		Selected:     pick("selected", r.Selected),
		SelectedText: pick("selected_text", r.SelectedText),
		Surface:      pick("surface", r.Surface),
		Error:        pick("error", r.Error),

		BadgeOK:      pick("badge_ok", r.BadgeOK),
		BadgeWarn:    pick("badge_warn", r.BadgeWarn),
		BadgeDanger:  pick("badge_danger", r.BadgeDanger),
		BadgeInfo:    pick("badge_info", r.BadgeInfo),
		BadgeNeutral: pick("badge_neutral", r.BadgeNeutral),

		FormLabel:     pick("form_label", r.FormLabel),
		FormRequired:  pick("form_required", r.FormRequired),
		FormError:     pick("form_error", r.FormError),
		FormHighlight: pick("form_highlight", r.FormHighlight),

		ButtonBg:       pick("button_bg", r.ButtonBg),
		ButtonDangerBg: pick("button_danger_bg", r.ButtonDangerBg),
		ButtonGhostFg:  pick("button_ghost_fg", r.ButtonGhostFg),

		CalendarSwatches: append([]string(nil), r.CalendarSwatches...),
	}
	if firstErr != nil {
		return Theme{}, firstErr
	}

	// Post-process "auto" tokens once Text and Surface are resolved. On
	// dark Base16 themes the ANSI dim color (base03 / color8) sits
	// deliberately close to the background so it fades comments into the
	// page — that's wrong for UI body-adjacent text we want the user to
	// read. Deriving dim/muted via OKLab interpolation between Text and
	// Surface gives a perceptually balanced mid-tone on any palette.
	if t.TextDim == nil {
		// 70 % text, 30 % surface — close enough to text to read as
		// body-adjacent (footer hints, weekday header) on every bg.
		t.TextDim = oklch.Mix(t.Text, t.Surface, 0.30)
	}
	if t.Muted == nil {
		// 55 % text, 45 % surface — noticeably dimmer than TextDim but
		// still well above the deliberately-faded base03 line.
		t.Muted = oklch.Mix(t.Text, t.Surface, 0.45)
	}

	return t, nil
}

// isAutoSentinel reports whether a raw TOML color value is the string
// literal "auto", which signals "compute me at theme-load time from
// Text and Surface". Currently honored for the text_dim and muted
// tokens; other fields fall through resolveColor as-is.
func isAutoSentinel(v any) bool {
	s, ok := v.(string)
	return ok && s == "auto"
}
