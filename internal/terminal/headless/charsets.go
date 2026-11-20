package headless

// charset maps a 7-bit character to the one a national or graphics set draws in its place.
type charset map[uint32]uint32

func newCharset(pairs ...string) *charset {
	set := charset{}
	for i := 0; i+1 < len(pairs); i += 2 {
		from := []rune(pairs[i])[0]
		to := []rune(pairs[i+1])[0]
		set[uint32(from)] = uint32(to)
	}
	return &set
}

var (
	charsetSwedish = newCharset("[", "Ä", "\\", "Ö", "]", "Å", "^", "Ü", "`", "é", "{", "ä", "|", "ö", "}", "å", "~", "ü")
	charsetDanish  = newCharset("@", "Ä", "[", "Æ", "\\", "Ø", "]", "Å", "^", "Ü", "`", "ä", "{", "æ", "|", "ø", "}", "å", "~", "ü")
	charsetSwiss   = newCharset("@", "É", "[", "Ä", "\\", "Ö", "]", "Å", "^", "Ü", "`", "é", "{", "ä", "|", "ö", "}", "å", "~", "ü")
)

// charsets are the designations ESC ( and friends select, keyed by final byte. "B" is US ASCII.
var charsets = map[byte]*charset{
	'0': newCharset(
		"`", "◆", "a", "▒", "b", "␉", "c", "␌", "d", "␍", "e", "␊",
		"f", "°", "g", "±", "h", "␤", "i", "␋", "j", "┘", "k", "┐",
		"l", "┌", "m", "└", "n", "┼", "o", "⎺", "p", "⎻", "q", "─",
		"r", "⎼", "s", "⎽", "t", "├", "u", "┤", "v", "┴", "w", "┬",
		"x", "│", "y", "≤", "z", "≥", "{", "π", "|", "≠", "}", "£",
		"~", "·",
	),
	'A': newCharset("#", "£"),
	'B': nil,
	'4': newCharset("#", "£", "@", "¾", "[", "ij", "\\", "½", "]", "|", "{", "¨", "|", "f", "}", "¼", "~", "´"),
	'C': charsetSwedish,
	'5': charsetSwedish,
	'R': newCharset("#", "£", "@", "à", "[", "°", "\\", "ç", "]", "§", "{", "é", "|", "ù", "}", "è", "~", "¨"),
	'Q': newCharset("@", "à", "[", "â", "\\", "ç", "]", "ê", "^", "î", "`", "ô", "{", "é", "|", "ù", "}", "è", "~", "û"),
	'K': newCharset("@", "§", "[", "Ä", "\\", "Ö", "]", "Ü", "{", "ä", "|", "ö", "}", "ü", "~", "ß"),
	'Y': newCharset("#", "£", "@", "§", "[", "°", "\\", "ç", "]", "é", "`", "ù", "{", "à", "|", "ò", "}", "è", "~", "ì"),
	'E': charsetDanish,
	'6': charsetDanish,
	'Z': newCharset("#", "£", "@", "§", "[", "¡", "\\", "Ñ", "]", "¿", "{", "°", "|", "ñ", "}", "ç"),
	'H': charsetSwiss,
	'7': charsetSwiss,
	'=': newCharset("#", "ù", "@", "à", "[", "é", "\\", "ç", "]", "ê", "^", "î", "_", "è", "`", "ô", "{", "ä", "|", "ö", "}", "ü", "~", "û"),
}

var gLevels = map[byte]int{'(': 0, ')': 1, '*': 2, '+': 3, '-': 1, '.': 2}
