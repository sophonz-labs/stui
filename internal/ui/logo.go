package ui

import "strings"

// logoWord is the name in one row, for a header too short or narrow for
// logoRows: bold, in the same colour.
const logoWord = "stui"

// logoTall is the shortest terminal that gives the header the logo's rows:
// 9 for the header (the logo, the version on its last row) and 5 for the
// prompt, frame, column names and footer leave 6 rows of table, k9s's own
// minimum.
const logoTall = 20

// logoRows is the sophonz wordmark st prints when it starts (st's
// internal/wordmark): "sophon" in half blocks and the z as a >_ prompt.
// Nine rows, 56 columns; the prompt is the last logoPromptW of each.
var logoRows = []string{
	`                       ▄▄                               `,
	`                       ██                               `,
	` ▄▄▄▄▄   ▄▄▄   ▄▄▄▄▄   ██▄▄▄     ▄▄▄   ▄▄▄▄▄   ▄▄       `,
	`██▀▀▀▀ ▄█▀▀▀█▄ ██▀▀▀█▄ ██▀▀▀█▄ ▄█▀▀▀█▄ ██▀▀▀█▄ ▀██▄     `,
	`▀████▄ ██   ██ ██   ██ ██   ██ ██   ██ ██   ██   ▀██    `,
	`▄▄▄▄██ ▀█▄▄▄█▀ ██▄▄▄█▀ ██   ██ ▀█▄▄▄█▀ ██   ██ ▄██▀ ▄▄▄▄`,
	`▀▀▀▀▀    ▀▀▀   ██▀▀▀   ▀▀   ▀▀   ▀▀▀   ▀▀   ▀▀ ▀▀   ▀▀▀▀`,
	`               ██                                       `,
	`               ▀▀                                       `,
}

// logoPromptW is the >_'s columns at the end of each row, drawn in
// cPrompt (#7dd3fc, as st draws it); the letters keep the default colour.
const logoPromptW = 9

const cPrompt = "\x1b[38;5;117m"

// logoLines is the logo for a header of n rows with room columns left
// beside the keys: the whole logo where it fits, else the word, else nil --
// the logo is never cut. This stui's version goes right-aligned on the
// logo's last row, which is blank but for the p's descender, or under the
// word when a row is left. Each line is as wide as the logo.
func logoLines(version string, room, n int) (lines []string, width int) {
	if n >= len(logoRows) && room >= Cells(logoRows[0]) {
		width = Cells(logoRows[0])
		for _, l := range logoRows {
			r := []rune(l)
			cut := len(r) - logoPromptW
			lines = append(lines, string(r[:cut])+cPrompt+string(r[cut:])+reset)
		}
		last := strings.TrimRight(logoRows[len(logoRows)-1], " ")
		if version != "" && Cells(last)+1+Cells(version) <= width {
			lines[len(lines)-1] = last + strings.Repeat(" ", width-Cells(last)-Cells(version)) + cGray + version + reset
		}
		return lines, width
	}
	width = Cells(logoWord)
	if room < width || n == 0 {
		return nil, 0
	}
	// The version may be wider than the word: then both are right-aligned
	// in its width, if the room takes it, else it is left out.
	if version != "" && n > 1 && room >= Cells(version) {
		width = max(width, Cells(version))
	} else {
		version = ""
	}
	lines = append(lines, strings.Repeat(" ", width-Cells(logoWord))+cTeal+bold+logoWord+reset)
	if version != "" {
		lines = append(lines, strings.Repeat(" ", width-Cells(version))+cGray+version+reset)
	}
	return lines, width
}
