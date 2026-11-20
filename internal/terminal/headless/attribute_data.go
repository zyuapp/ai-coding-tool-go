// Package headless is a Go port of the parts of @xterm/headless 6.0 the terminal host relies on:
// the VT500 parser, the input handler, buffers with scrollback, the alternate screen, and reflow
// on resize. Cells use xterm.js's packed layout, so attributes, colours and widths resolve exactly
// as the renderer's xterm.js resolves them. Replies to queries are not produced: the window's
// terminal answers those.
package headless

const (
	contentCodepointMask  = 0x1FFFFF
	contentIsCombinedMask = 0x200000
	contentHasContentMask = 0x3FFFFF
	contentWidthMask      = 0xC00000
	contentWidthShift     = 22

	attrPColorMask = 0xFF
	attrCMMask     = 0x3000000
	attrCMDefault  = 0
	attrCMP16      = 0x1000000
	attrCMP256     = 0x2000000
	attrCMRGB      = 0x3000000
	attrRGBMask    = 0xFFFFFF

	fgInverse       = 0x4000000
	fgBold          = 0x8000000
	fgUnderline     = 0x10000000
	fgBlink         = 0x20000000
	fgInvisible     = 0x40000000
	fgStrikethrough = 0x80000000

	bgItalic      = 0x4000000
	bgDim         = 0x8000000
	bgHasExtended = 0x10000000
	bgProtected   = 0x20000000
	bgOverline    = 0x40000000

	extUnderlineStyle = 0x1C000000

	underlineNone   = 0
	underlineSingle = 1
	underlineDouble = 2
	underlineDashed = 5

	nullCellCode  = 0
	nullCellWidth = 1
)

// Color modes as Cell.FgColorMode / BgColorMode report them.
const (
	ColorModeDefault = attrCMDefault
	ColorModeP16     = attrCMP16
	ColorModeP256    = attrCMP256
	ColorModeRGB     = attrCMRGB
)

type extendedAttrs struct {
	ext   uint32
	urlID uint32
}

func (e extendedAttrs) underlineStyle() uint32 {
	if e.urlID != 0 {
		return underlineDashed
	}
	return (e.ext & extUnderlineStyle) >> 26
}

func (e *extendedAttrs) setUnderlineStyle(value uint32) {
	e.ext &^= extUnderlineStyle
	e.ext |= (value << 26) & extUnderlineStyle
}

func (e extendedAttrs) underlineColor() uint32 { return e.ext & (attrCMMask | attrRGBMask) }

func (e *extendedAttrs) setUnderlineColor(value uint32) {
	e.ext &^= attrCMMask | attrRGBMask
	e.ext |= value & (attrCMMask | attrRGBMask)
}

func (e extendedAttrs) isEmpty() bool { return e.underlineStyle() == underlineNone && e.urlID == 0 }

type attributeData struct {
	fg       uint32
	bg       uint32
	extended extendedAttrs
}

func (a *attributeData) updateExtended() {
	if a.extended.isEmpty() {
		a.bg &^= bgHasExtended
	} else {
		a.bg |= bgHasExtended
	}
}

// Cell is one resolved cell, as xterm.js's IBufferCell exposes it.
type Cell struct {
	content  uint32
	fg       uint32
	bg       uint32
	extended extendedAttrs
	combined string
}

func nullCell(attr attributeData) Cell {
	return Cell{content: nullCellCode | nullCellWidth<<contentWidthShift, fg: attr.fg, bg: attr.bg, extended: attr.extended}
}

func (c *Cell) Width() int { return int(c.content >> contentWidthShift) }

// Chars is the cell's text; empty for a cell nothing was printed in.
func (c *Cell) Chars() string {
	if c.content&contentIsCombinedMask != 0 {
		return c.combined
	}
	if cp := c.content & contentCodepointMask; cp != 0 {
		return string(rune(cp))
	}
	return ""
}

func (c *Cell) IsInverse() bool       { return c.fg&fgInverse != 0 }
func (c *Cell) IsBold() bool          { return c.fg&fgBold != 0 }
func (c *Cell) IsBlink() bool         { return c.fg&fgBlink != 0 }
func (c *Cell) IsInvisible() bool     { return c.fg&fgInvisible != 0 }
func (c *Cell) IsItalic() bool        { return c.bg&bgItalic != 0 }
func (c *Cell) IsDim() bool           { return c.bg&bgDim != 0 }
func (c *Cell) IsStrikethrough() bool { return c.fg&fgStrikethrough != 0 }
func (c *Cell) IsOverline() bool      { return c.bg&bgOverline != 0 }
func (c *Cell) IsUnderline() bool {
	if c.bg&bgHasExtended != 0 && c.extended.underlineStyle() != underlineNone {
		return true
	}
	return c.fg&fgUnderline != 0
}

func (c *Cell) FgColorMode() uint32 { return c.fg & attrCMMask }
func (c *Cell) BgColorMode() uint32 { return c.bg & attrCMMask }
func (c *Cell) IsFgRGB() bool       { return c.fg&attrCMMask == attrCMRGB }
func (c *Cell) IsBgRGB() bool       { return c.bg&attrCMMask == attrCMRGB }
func (c *Cell) IsFgPalette() bool   { m := c.fg & attrCMMask; return m == attrCMP16 || m == attrCMP256 }
func (c *Cell) IsBgPalette() bool   { m := c.bg & attrCMMask; return m == attrCMP16 || m == attrCMP256 }

// IsAttributeDefault is true when the cell carries no colour and no attribute.
func (c *Cell) IsAttributeDefault() bool { return c.fg == 0 && c.bg == 0 }

// FgColor is the palette index or packed RGB, or -1 for the default colour.
func (c *Cell) FgColor() int { return colorOf(c.fg) }

// BgColor is the palette index or packed RGB, or -1 for the default colour.
func (c *Cell) BgColor() int { return colorOf(c.bg) }

func colorOf(value uint32) int {
	switch value & attrCMMask {
	case attrCMP16, attrCMP256:
		return int(value & attrPColorMask)
	case attrCMRGB:
		return int(value & attrRGBMask)
	}
	return -1
}
