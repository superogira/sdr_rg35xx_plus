// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

// Emoji maps the sender's chosen symbol (table+code) to an emoji for
// displays that can render them (the web panel). Unknown codes fall
// back to the raw symbol character, which is APRS's own notation.
func Emoji(table, sym byte) string {
	if table == '\\' { // alternate table
		switch sym {
		case 'Y':
			return "⛵"
		case 'S':
			return "🛰"
		case 'A':
			return "📦"
		case 'C':
			return "🌊"
		case 'R':
			return "🛥"
		}
	}
	switch sym {
	case '[':
		return "🚶"
	case '>':
		return "🚗"
	case '\'':
		return "✈️"
	case 'b':
		return "🚲"
	case 'O':
		return "🎈"
	case '-':
		return "🏠"
	case '&':
		return "🛩"
	case 'X':
		return "🚁"
	case 'j':
		return "🚙"
	case 'k':
		return "🚚"
	case 's':
		return "🚢"
	case 'u':
		return "🚌"
	case 'v':
		return "🚐"
	case '_':
		return "🌧"
	case '*':
		return "❄️"
	case '#':
		return "📡"
	case 'i':
		return "📦"
	case 'n':
		return "🧭"
	case 'r':
		return "📶"
	case 't':
		return "🌡"
	case 'w':
		return "🌬"
	}
	return string(rune(sym))
}
