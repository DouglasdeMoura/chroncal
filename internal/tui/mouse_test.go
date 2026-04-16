package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Lexer tests
// ---------------------------------------------------------------------------

func TestAnsiLexer(t *testing.T) {
	t.Run("PlainText", func(t *testing.T) {
		l := newAnsiLexer("Hello World")

		tok := l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "Hello World", tok.text)

		tok = l.next()
		assert.Equal(t, ansiEOF, tok.kind)
	})

	t.Run("TextWithCSI", func(t *testing.T) {
		l := newAnsiLexer("Hello\x1b[31mRed\x1b[0mWorld")

		tok := l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "Hello", tok.text)

		tok = l.next()
		assert.Equal(t, ansiCSI, tok.kind)
		assert.Equal(t, "\x1b[31m", tok.text)
		assert.Equal(t, "31", tok.params)
		assert.Equal(t, byte('m'), tok.final)

		tok = l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "Red", tok.text)

		tok = l.next()
		assert.Equal(t, ansiCSI, tok.kind)
		assert.Equal(t, "\x1b[0m", tok.text)
		assert.Equal(t, "0", tok.params)
		assert.Equal(t, byte('m'), tok.final)

		tok = l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "World", tok.text)

		tok = l.next()
		assert.Equal(t, ansiEOF, tok.kind)
	})

	t.Run("MultipleCSISequences", func(t *testing.T) {
		l := newAnsiLexer("\x1b[1;31m\x1b[44mText")

		tok := l.next()
		assert.Equal(t, ansiCSI, tok.kind)
		assert.Equal(t, "1;31", tok.params)
		assert.Equal(t, byte('m'), tok.final)

		tok = l.next()
		assert.Equal(t, ansiCSI, tok.kind)
		assert.Equal(t, "44", tok.params)

		tok = l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "Text", tok.text)
	})

	t.Run("EmptyInput", func(t *testing.T) {
		l := newAnsiLexer("")
		tok := l.next()
		assert.Equal(t, ansiEOF, tok.kind)
	})

	t.Run("OnlyCSI", func(t *testing.T) {
		l := newAnsiLexer("\x1b[2J")

		tok := l.next()
		assert.Equal(t, ansiCSI, tok.kind)
		assert.Equal(t, "2", tok.params)
		assert.Equal(t, byte('J'), tok.final)

		tok = l.next()
		assert.Equal(t, ansiEOF, tok.kind)
	})

	t.Run("CSINoParams", func(t *testing.T) {
		l := newAnsiLexer("\x1b[m")

		tok := l.next()
		assert.Equal(t, ansiCSI, tok.kind)
		assert.Equal(t, "", tok.params)
		assert.Equal(t, byte('m'), tok.final)
	})

	t.Run("OSCWithBEL", func(t *testing.T) {
		l := newAnsiLexer("\x1b]8;;https://example.com\x07Link\x1b]8;;\x07")

		tok := l.next()
		assert.Equal(t, ansiOSC, tok.kind)

		tok = l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "Link", tok.text)

		tok = l.next()
		assert.Equal(t, ansiOSC, tok.kind)

		tok = l.next()
		assert.Equal(t, ansiEOF, tok.kind)
	})

	t.Run("OSCWithST", func(t *testing.T) {
		l := newAnsiLexer("\x1b]0;Window Title\x1b\\Text")

		tok := l.next()
		assert.Equal(t, ansiOSC, tok.kind)

		tok = l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "Text", tok.text)
	})

	t.Run("ESCOnly", func(t *testing.T) {
		l := newAnsiLexer("\x1b")

		tok := l.next()
		assert.Equal(t, ansiText, tok.kind)
		assert.Equal(t, "\x1b", tok.text)
	})

	t.Run("ESCWithNonCSI", func(t *testing.T) {
		l := newAnsiLexer("\x1bM")

		tok := l.next()
		assert.Equal(t, ansiESC, tok.kind)
		assert.Equal(t, byte('M'), tok.final)
	})
}

func TestParseCSIParam(t *testing.T) {
	assert.Equal(t, 0, parseCSIParam(""))
	assert.Equal(t, 42, parseCSIParam("42"))
	assert.Equal(t, 123, parseCSIParam("123"))
}

// ---------------------------------------------------------------------------
// Tracker tests
// ---------------------------------------------------------------------------

func TestMouseMark(t *testing.T) {
	mt := &mouseTracker{}

	result := mt.mark("btn", "Click")
	assert.Equal(t, "\x1b[0zClick\x1b[0z", result)

	result = mt.mark("link", "Here")
	assert.Equal(t, "\x1b[1zHere\x1b[1z", result)
}

func TestMouseSweep_StripsMarkers(t *testing.T) {
	mt := &mouseTracker{}
	content := mt.mark("btn", "Click me")
	cleaned := mt.sweep(content)
	assert.Equal(t, "Click me", cleaned)
}

func TestMouseSweep_PreservesANSI(t *testing.T) {
	mt := &mouseTracker{}
	content := mt.mark("btn", "\x1b[31mRed\x1b[0m")
	cleaned := mt.sweep(content)
	assert.Equal(t, "\x1b[31mRed\x1b[0m", cleaned)
}

func TestMouseSweep_NoMarkers(t *testing.T) {
	mt := &mouseTracker{}
	content := "plain text\nline two"
	cleaned := mt.sweep(content)
	assert.Equal(t, content, cleaned)
}

func TestMouseSweep_SingleTarget(t *testing.T) {
	mt := &mouseTracker{}
	content := "Hello " + mt.mark("btn", "World")
	cleaned := mt.sweep(content)
	assert.Equal(t, "Hello World", cleaned)

	require.Len(t, mt.zones, 1)
	z := mt.zones[0]
	assert.Equal(t, "btn", z.name)
	assert.Equal(t, 6, z.startX)
	assert.Equal(t, 0, z.startY)
	assert.Equal(t, 10, z.endX)
	assert.Equal(t, 0, z.endY)
}

func TestMouseSweep_MultipleTargetsSameLine(t *testing.T) {
	mt := &mouseTracker{}
	content := mt.mark("a", "AA") + " " + mt.mark("b", "BB")
	cleaned := mt.sweep(content)
	assert.Equal(t, "AA BB", cleaned)

	require.Len(t, mt.zones, 2)
	assert.Equal(t, "a", mt.zones[0].name)
	assert.Equal(t, 0, mt.zones[0].startX)
	assert.Equal(t, 1, mt.zones[0].endX)
	assert.Equal(t, "b", mt.zones[1].name)
	assert.Equal(t, 3, mt.zones[1].startX)
	assert.Equal(t, 4, mt.zones[1].endX)
}

func TestMouseSweep_MultiLineTarget(t *testing.T) {
	mt := &mouseTracker{}
	content := mt.mark("block", "line1\nline2\nline3")
	cleaned := mt.sweep(content)
	assert.Equal(t, "line1\nline2\nline3", cleaned)

	require.Len(t, mt.zones, 1)
	z := mt.zones[0]
	assert.Equal(t, 0, z.startX)
	assert.Equal(t, 0, z.startY)
	assert.Equal(t, 4, z.endX)
	assert.Equal(t, 2, z.endY)
}

func TestMouseSweep_WideCharacters(t *testing.T) {
	mt := &mouseTracker{}
	content := mt.mark("wide", "中文")
	cleaned := mt.sweep(content)
	assert.Equal(t, "中文", cleaned)

	require.Len(t, mt.zones, 1)
	z := mt.zones[0]
	assert.Equal(t, 0, z.startX)
	assert.Equal(t, 3, z.endX)
}

func TestMouseSweep_NestedTargets(t *testing.T) {
	mt := &mouseTracker{}
	inner := mt.mark("inner", "click")
	outer := mt.mark("outer", "before "+inner+" after")
	cleaned := mt.sweep(outer)
	assert.Equal(t, "before click after", cleaned)

	require.Len(t, mt.zones, 2)
	assert.Equal(t, "inner", mt.zones[0].name)
	assert.Equal(t, 7, mt.zones[0].startX)
	assert.Equal(t, 11, mt.zones[0].endX)

	assert.Equal(t, "outer", mt.zones[1].name)
	assert.Equal(t, 0, mt.zones[1].startX)
	assert.Equal(t, 17, mt.zones[1].endX)
}

func TestMouseSweep_FrameReset(t *testing.T) {
	mt := &mouseTracker{}

	mt.mark("a", "first")
	mt.sweep(mt.mark("a", "first"))

	result := mt.mark("b", "second")
	assert.Equal(t, "\x1b[0zsecond\x1b[0z", result)
}

func TestMouseResolve_Hit(t *testing.T) {
	mt := &mouseTracker{}
	content := "Hello " + mt.mark("btn", "World")
	mt.sweep(content)

	assert.Equal(t, "btn", mt.resolve(6, 0))
	assert.Equal(t, "btn", mt.resolve(10, 0))
}

func TestMouseResolve_Miss(t *testing.T) {
	mt := &mouseTracker{}
	content := "Hello " + mt.mark("btn", "World")
	mt.sweep(content)

	assert.Equal(t, "", mt.resolve(0, 0))
	assert.Equal(t, "", mt.resolve(11, 0))
	assert.Equal(t, "", mt.resolve(6, 1))
}

func TestMouseResolve_Nested(t *testing.T) {
	mt := &mouseTracker{}
	inner := mt.mark("inner", "click")
	outer := mt.mark("outer", "before "+inner+" after")
	mt.sweep(outer)

	assert.Equal(t, "inner", mt.resolve(7, 0))
	assert.Equal(t, "inner", mt.resolve(11, 0))

	assert.Equal(t, "outer", mt.resolve(0, 0))
	assert.Equal(t, "outer", mt.resolve(17, 0))
}

func TestMouseResolve_MultiLineHit(t *testing.T) {
	mt := &mouseTracker{}
	content := "pre " + mt.mark("block", "line1\nline2\nline3") + " post"
	mt.sweep(content)

	assert.Equal(t, "", mt.resolve(3, 0))
	assert.Equal(t, "block", mt.resolve(4, 0))
	assert.Equal(t, "block", mt.resolve(4, 1))
	assert.Equal(t, "block", mt.resolve(4, 2))
	assert.Equal(t, "", mt.resolve(5, 2))
	assert.Equal(t, "", mt.resolve(4, 3))
}

func TestMouseZoneContains(t *testing.T) {
	singleLine := mouseZone{name: "s", startX: 3, startY: 0, endX: 7, endY: 0}
	assert.True(t, singleLine.contains(3, 0))
	assert.True(t, singleLine.contains(5, 0))
	assert.True(t, singleLine.contains(7, 0))
	assert.False(t, singleLine.contains(2, 0))
	assert.False(t, singleLine.contains(8, 0))
	assert.False(t, singleLine.contains(5, 1))

	multiLine := mouseZone{name: "m", startX: 5, startY: 0, endX: 15, endY: 2}
	assert.True(t, multiLine.contains(5, 0))
	assert.True(t, multiLine.contains(10, 1))
	assert.True(t, multiLine.contains(15, 2))
	assert.False(t, multiLine.contains(4, 1))
	assert.False(t, multiLine.contains(16, 1))
	assert.False(t, multiLine.contains(10, 3))
}

func TestMouseZoneContains_EndAtNewLine(t *testing.T) {
	z := mouseZone{name: "z", startX: 0, startY: 0, endX: -1, endY: 1}
	assert.False(t, z.contains(0, 0))
	assert.False(t, z.contains(0, 1))
}

func TestMouseMark_DefaultTracker(t *testing.T) {
	saved := *defaultMouseTracker
	defer func() { *defaultMouseTracker = saved }()
	*defaultMouseTracker = mouseTracker{}

	content := "Click " + mouseMark("link", "here") + " for more"
	cleaned := defaultMouseTracker.sweep(content)
	assert.Equal(t, "Click here for more", cleaned)
	assert.Equal(t, "link", defaultMouseTracker.resolve(6, 0))
	assert.Equal(t, "link", defaultMouseTracker.resolve(9, 0))
	assert.Equal(t, "", defaultMouseTracker.resolve(5, 0))
	assert.Equal(t, "", defaultMouseTracker.resolve(10, 0))
}

func TestMouseMark_MarkerFormat(t *testing.T) {
	mt := &mouseTracker{}
	for i := range 5 {
		result := mt.mark(fmt.Sprintf("zone%d", i), "x")
		expected := fmt.Sprintf("\x1b[%dzx\x1b[%dz", i, i)
		assert.Equal(t, expected, result)
	}
}

func TestMouseSweep_MarkerAtLineStart(t *testing.T) {
	mt := &mouseTracker{}
	content := "first\n" + mt.mark("second", "second line")
	cleaned := mt.sweep(content)
	assert.Equal(t, "first\nsecond line", cleaned)

	require.Len(t, mt.zones, 1)
	z := mt.zones[0]
	assert.Equal(t, 0, z.startX)
	assert.Equal(t, 1, z.startY)
	assert.Equal(t, 10, z.endX)
	assert.Equal(t, 1, z.endY)
}

func TestMouseSweep_PreservesNonMarkerCSI(t *testing.T) {
	mt := &mouseTracker{}
	content := mt.mark("btn", "text") + "\x1b[2J"
	cleaned := mt.sweep(content)
	assert.Equal(t, "text\x1b[2J", cleaned)
}

func TestMouseSweep_OSCDoesNotAffectZoneCoordinates(t *testing.T) {
	mt := &mouseTracker{}
	osc := "\x1b]8;;https://example.com\x07Link\x1b]8;;\x07"
	content := osc + " " + mt.mark("btn", "Click")
	cleaned := mt.sweep(content)
	assert.Equal(t, osc+" Click", cleaned)

	require.Len(t, mt.zones, 1)
	z := mt.zones[0]
	assert.Equal(t, 5, z.startX)
	assert.Equal(t, 9, z.endX)
}

func TestMouseResolve_SideBySideRectangular(t *testing.T) {
	mt := &mouseTracker{}

	left := mt.mark("submit", "+---------+\n| Submit  |\n+---------+")
	right := mt.mark("cancel", "+--------+\n| Cancel |\n+--------+")

	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")
	combined := make([]string, len(leftLines))
	for i := range leftLines {
		combined[i] = leftLines[i] + " " + rightLines[i]
	}
	content := strings.Join(combined, "\n")

	mt.sweep(content)

	require.Len(t, mt.zones, 2)

	assert.Equal(t, "submit", mt.resolve(5, 1))
	assert.Equal(t, "cancel", mt.resolve(16, 1))
	assert.Equal(t, "", mt.resolve(11, 1))
}
