package tui

import (
	"fmt"
	"image/color"
	"io"
	"strconv"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

// detectTimeout caps the total time spent waiting for terminal color
// query responses. Terminals that answer respond in microseconds; this
// is just a ceiling for the unresponsive case.
const detectTimeout = 750 * time.Millisecond

// Palette is the 16-entry ANSI color palette as actually rendered by the
// terminal. Entries are nil when the terminal didn't answer the OSC 4
// query for that index.
type Palette [16]color.Color

// Lookup returns the palette entry for an ANSI index, or nil when the
// index is out of range or wasn't reported.
func (p *Palette) Lookup(idx int) color.Color {
	if p == nil || idx < 0 || idx > 15 {
		return nil
	}
	return p[idx]
}

// detectTerminalState best-effort detects the terminal's background and
// its 16-color ANSI palette. Returns a concrete bg color plus a fallback
// hint (cream for light, near-black for dark) when only the foreground
// is reported. The palette is non-nil when at least one OSC 4 response
// arrived; individual entries are nil when that specific index wasn't
// reported.
//
// Strategy:
//
//  1. Send OSC 11 (bg) + OSC 10 (fg) + OSC 4 (palette 0..15) + DA1
//     (Primary Device Attributes) together in a single raw-mode session.
//     DA1 is the cutoff — every mainstream terminal answers it, which
//     bounds the query even when individual OSC sequences are ignored.
//
//  2. If OSC 11 answered, use that RGB directly.
//
//  3. Otherwise, if OSC 10 answered, derive the theme mode from the
//     foreground's OKLCh lightness (dark fg → light theme, light fg →
//     dark theme) and return a neutral bg stand-in so downstream
//     OKLCh-based adjustments still have something to work against.
//
//  4. If neither answers, the returned bg is nil. The palette may still
//     have entries — they're independent queries.
func detectTerminalState(in term.File, out term.File) (color.Color, *Palette) {
	bg, fg, pal := queryTerminalColors(in, out, detectTimeout)
	resolvedBG := bg
	if resolvedBG == nil && fg != nil {
		if L, _, _, ok := oklch.FromColor(fg); ok {
			// Dark foreground implies a LIGHT terminal theme (and vice
			// versa). Fall back to a neutral stand-in so the downstream
			// adaptive Selected shift has a sensible anchor.
			if L < 0.5 {
				resolvedBG = lipgloss.Color("#F1F1F0") // neutral cream
			} else {
				resolvedBG = lipgloss.Color("#1E1E2E") // neutral near-black
			}
		}
	}
	if pal != nil && paletteEmpty(pal) {
		pal = nil
	}
	return resolvedBG, pal
}

func paletteEmpty(p *Palette) bool {
	for _, c := range p {
		if c != nil {
			return false
		}
	}
	return true
}

// queryTerminalColors sends OSC 11 + OSC 10 + OSC 4 (×16) + DA1 in a
// single raw-mode session and returns whichever of bg / fg / palette
// entries the terminal reported. Any output may be nil / empty. The
// MakeRaw/Restore cost is paid once for the whole batch.
func queryTerminalColors(in term.File, out term.File, timeout time.Duration) (bg, fg color.Color, pal *Palette) {
	if !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return nil, nil, nil
	}
	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		return nil, nil, nil
	}
	defer term.Restore(in.Fd(), state) //nolint:errcheck

	rd, err := uv.NewCancelReader(in)
	if err != nil {
		return nil, nil, nil
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

	var qb strings.Builder
	qb.WriteString(ansi.RequestBackgroundColor)
	qb.WriteString(ansi.RequestForegroundColor)
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&qb, "\x1b]4;%d;?\x07", i)
	}
	qb.WriteString(ansi.RequestPrimaryDeviceAttributes)
	if _, err := io.WriteString(out, qb.String()); err != nil {
		return nil, nil, nil
	}

	pa := ansi.GetParser()
	defer ansi.PutParser(pa)

	var palette Palette
	pal = &palette

	var acc []byte
	var buf [256]byte
	var pstate byte
	for {
		n, err := rd.Read(buf[:])
		if err != nil {
			return bg, fg, pal
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
					switch pa.Command() {
					case 10:
						if len(parts) == 2 {
							fg = ansi.XParseColor(parts[1])
						}
					case 11:
						if len(parts) == 2 {
							bg = ansi.XParseColor(parts[1])
						}
					case 4:
						// Response: "4;INDEX;rgb:RRRR/GGGG/BBBB"
						if len(parts) == 3 {
							if idx, err := strconv.Atoi(parts[1]); err == nil && idx >= 0 && idx <= 15 {
								palette[idx] = ansi.XParseColor(parts[2])
							}
						}
					}
				case ansi.HasCsiPrefix(s):
					if pa.Command() == ansi.Command('?', 0, 'c') {
						return bg, fg, pal
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
