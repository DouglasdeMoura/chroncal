package tui

import (
	"embed"
	"fmt"
	"image/color"
	"io/fs"
	"strconv"
	"strings"
	"sync"

	lipgloss "charm.land/lipgloss/v2"
	toml "github.com/pelletier/go-toml/v2"

	"github.com/douglasdemoura/chroncal/internal/config"
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
	Primary      any `toml:"primary"`
	Accent       any `toml:"accent"`
	Muted        any `toml:"muted"`
	Text         any `toml:"text"`
	TextDim      any `toml:"text_dim"`
	Border       any `toml:"border"`
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
	ButtonBg any `toml:"button_bg"`

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
// a warning to the state-dir log file and fall back to the default so a
// typo in config.toml cannot make the TUI unusable. The warning must not
// go to stderr. LoadTheme runs while the TUI owns the terminal, and a
// stderr write prints over the display.
func LoadTheme(name string, hasDarkBG bool) Theme {
	if name == "" {
		name = DefaultThemeName
	}
	t, err := LoadBuiltinTheme(name, hasDarkBG)
	if err == nil {
		return t
	}
	config.SharedStateDirLogger().Warn("theme failed to load; falling back to the default theme",
		"theme", name, "error", err.Error(), "fallback", DefaultThemeName)
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
// RGB via activePalette when an OSC 4 response is available. Themes can
// then lean on ANSI references (primary = "4"). Exact OKLCh contrast
// computations still work against real hex values. When the terminal
// reports no palette entry, a static Base16-style hex for the current
// background polarity stands in (see assumedPaletteDark). Indices 16..255
// and unrecognized strings fall through to lipgloss.Color.
func resolveColor(v any, hasDarkBG bool, field string) (color.Color, error) {
	switch x := v.(type) {
	case string:
		return resolveString(x, hasDarkBG), nil
	case map[string]any:
		key := "light"
		if hasDarkBG {
			key = "dark"
		}
		s, ok := x[key].(string)
		if !ok {
			return nil, fmt.Errorf("field %q variant missing %q string", field, key)
		}
		return resolveString(s, hasDarkBG), nil
	case nil:
		return nil, fmt.Errorf("field %q is missing", field)
	default:
		return nil, fmt.Errorf("field %q: unsupported color value %T", field, v)
	}
}

// assumedPaletteDark and assumedPaletteLight hold the ANSI 16-color
// values the loader substitutes when the terminal reported no palette
// entry for a slot. The values follow the Base16 convention that
// system.toml documents: slot 0 is the background, slot 7 the foreground,
// slot 8 the dim line, and slots 9..14 mirror slots 1..6. Slot 1 uses a
// slightly darker red than the Base16 default. That keeps the
// computed-foreground ratio above 4.5.
//
// The loader returns these hexes instead of ANSI indices. The terminal
// then renders the exact value the OKLCh contrast math used. A pill and
// its computed label can no longer disagree about polarity.
var assumedPaletteDark = Palette{
	rgbColor("#181818"), rgbColor("#a03e35"), rgbColor("#a1b56c"), rgbColor("#ba823f"),
	rgbColor("#7cafc2"), rgbColor("#aa759f"), rgbColor("#86c1b9"), rgbColor("#d8d8d8"),
	rgbColor("#585858"), rgbColor("#a03e35"), rgbColor("#a1b56c"), rgbColor("#ba823f"),
	rgbColor("#7cafc2"), rgbColor("#aa759f"), rgbColor("#86c1b9"), rgbColor("#f8f8f8"),
}

var assumedPaletteLight = Palette{
	rgbColor("#f8f8f8"), rgbColor("#a03e35"), rgbColor("#a1b56c"), rgbColor("#ba823f"),
	rgbColor("#7cafc2"), rgbColor("#aa759f"), rgbColor("#86c1b9"), rgbColor("#383838"),
	rgbColor("#b8b8b8"), rgbColor("#a03e35"), rgbColor("#a1b56c"), rgbColor("#ba823f"),
	rgbColor("#7cafc2"), rgbColor("#aa759f"), rgbColor("#86c1b9"), rgbColor("#181818"),
}

// rgbColor parses a "#rrggbb" literal into a color. The assumed palettes
// call it at package init. A malformed literal would silently decode to
// black, so keep the literals valid. The tests exercise every slot.
func rgbColor(hex string) color.RGBA {
	var c color.RGBA
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &c.R, &c.G, &c.B)
	c.A = 0xff
	return c
}

// resolveString turns a single TOML color string into a color.Color. An
// ANSI index 0..15 first asks the terminal palette. Without a palette
// answer, the assumed Base16-style hex for the current background
// polarity stands in. The terminal then renders the exact value the
// contrast math used. An index must not fall through to lipgloss here:
// lipgloss answers with its VGA defaults. Those defaults sit on the wrong
// lightness side for themed terminals, so every computed foreground
// flipped polarity against the rendered color.
func resolveString(s string, hasDarkBG bool) color.Color {
	if idx, ok := ansi16Index(s); ok {
		if c := activePalette.Lookup(idx); c != nil {
			return c
		}
		if hasDarkBG {
			return assumedPaletteDark.Lookup(idx)
		}
		return assumedPaletteLight.Lookup(idx)
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
		Primary:      pick("primary", r.Primary),
		Accent:       pick("accent", r.Accent),
		Muted:        pick("muted", r.Muted),
		Text:         pick("text", r.Text),
		TextDim:      pick("text_dim", r.TextDim),
		Border:       pick("border", r.Border),
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

		ButtonBg: pick("button_bg", r.ButtonBg),

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
