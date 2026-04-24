package tui

import (
	"image/color"
	"io"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

// detectTimeout caps the total time spent waiting for terminal color
// query responses. Terminals that answer respond in microseconds; this
// is just a ceiling for the unresponsive case.
const detectTimeout = 750 * time.Millisecond

// detectTerminalBG best-effort detects a background color suitable for
// seeding the adaptive theme. Returns a concrete bg color plus a
// fallback hint (cream for light, near-black for dark) when only the
// foreground is reported. Returns nil when the terminal answers neither
// OSC 11 nor OSC 10 — callers should treat that as "unknown" and leave
// the theme's static configuration in place.
//
// Strategy:
//
//  1. Send OSC 11 (bg), OSC 10 (fg), and DA1 (Primary Device Attributes)
//     together in a single raw-mode session. The DA1 response is a
//     near-universal cutoff — every mainstream terminal answers it,
//     which bounds the query even when the OSC sequences go ignored.
//
//  2. If OSC 11 answered, use that RGB directly.
//
//  3. Otherwise, if OSC 10 answered, derive the theme mode from the
//     foreground's OKLCh lightness (dark fg → light theme, light fg →
//     dark theme) and return a neutral bg stand-in so downstream
//     OKLCh-based adjustments still have something to work against.
//
//  4. If neither answers, return nil.
func detectTerminalBG(in term.File, out term.File) color.Color {
	bg, fg := queryTerminalColors(in, out, detectTimeout)
	if bg != nil {
		return bg
	}
	if fg == nil {
		return nil
	}
	L, _, _, ok := oklch.FromColor(fg)
	if !ok {
		return nil
	}
	// Dark foreground implies the terminal is running a LIGHT theme
	// (and vice versa). Fall back to a neutral stand-in for bg so the
	// downstream adaptive Selected shift has a sensible anchor.
	if L < 0.5 {
		return lipgloss.Color("#F1F1F0") // neutral cream
	}
	return lipgloss.Color("#1E1E2E") // neutral near-black
}

// queryTerminalColors sends OSC 11 + OSC 10 + DA1 in a single raw-mode
// session and returns whichever of bg / fg the terminal reported.
// Either (or both) may be nil. Mirrors the internal pattern lipgloss
// v2 uses for its own BackgroundColor helper, but with both color
// queries issued together so we pay the MakeRaw/Restore cost once.
func queryTerminalColors(in term.File, out term.File, timeout time.Duration) (bg, fg color.Color) {
	if !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return nil, nil
	}
	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		return nil, nil
	}
	defer term.Restore(in.Fd(), state) //nolint:errcheck

	rd, err := uv.NewCancelReader(in)
	if err != nil {
		return nil, nil
	}
	defer rd.Close() //nolint:errcheck

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-time.After(timeout):
			rd.Cancel()
		}
	}()

	query := ansi.RequestBackgroundColor + ansi.RequestForegroundColor + ansi.RequestPrimaryDeviceAttributes
	if _, err := io.WriteString(out, query); err != nil {
		return nil, nil
	}

	pa := ansi.GetParser()
	defer ansi.PutParser(pa)

	var acc []byte
	var buf [256]byte
	var pstate byte
	for {
		n, err := rd.Read(buf[:])
		if err != nil {
			return bg, fg
		}
		p := buf[:]
		for n > 0 {
			seq, _, read, newState := ansi.DecodeSequence(p[:n], pstate, pa)
			acc = append(acc, seq...)
			if newState == ansi.NormalState {
				s := string(acc)
				switch {
				case ansi.HasOscPrefix(s):
					parts := strings.Split(string(pa.Data()), ";")
					if len(parts) == 2 {
						c := ansi.XParseColor(parts[1])
						switch pa.Command() {
						case 10:
							fg = c
						case 11:
							bg = c
						}
					}
				case ansi.HasCsiPrefix(s):
					if pa.Command() == ansi.Command('?', 0, 'c') {
						return bg, fg
					}
				}
				acc = acc[:0]
			}
			pstate = newState
			n -= read
			p = p[read:]
		}
	}
}

