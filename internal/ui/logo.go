package ui

import "strings"

// logoWord is the name in one row, for a header too short or narrow for
// logoRows: bold, in the same colour.
const logoWord = "stui"

// logoTall is the shortest terminal that gives the header the logo's rows:
// 9 for the header (the logo and the version) and 5 for the prompt, frame,
// column names and footer leave 6 rows of table, k9s's own minimum.
const logoTall = 20

// logoRows is stui's logo: eight rows, 34 columns.
var logoRows = []string{
	`             /$$               /$$`,
	`            | $$              |__/`,
	`  /$$$$$$$ /$$$$$$   /$$   /$$ /$$`,
	` /$$_____/|_  $$_/  | $$  | $$| $$`,
	`|  $$$$$$   | $$    | $$  | $$| $$`,
	` \____  $$  | $$ /$$| $$  | $$| $$`,
	` /$$$$$$$/  |  $$$$/|  $$$$$$/| $$`,
	`|_______/    \___/   \______/ |__/`,
}

// logoLines is the logo for a header of n rows with room columns left
// beside the keys: the whole logo where it fits, else the word, else nil --
// the logo is never cut. Under it, this stui's version when a row is left.
// Each line is as wide as the logo.
func logoLines(version string, room, n int) (lines []string, width int) {
	rows := logoRows
	if n < len(logoRows) || room < Cells(logoRows[0]) {
		rows = []string{logoWord}
	}
	width = Cells(rows[0])
	if room < width || n == 0 {
		return nil, 0
	}
	c, v := cTeal, cGray
	if len(rows) == 1 {
		c += bold
	}
	// The version may be wider than the word: then both are right-aligned
	// in its width, if the room takes it, else it is left out.
	if version != "" && n > len(rows) && room >= Cells(version) {
		width = max(width, Cells(version))
	} else {
		version = ""
	}
	for _, l := range rows {
		lines = append(lines, strings.Repeat(" ", width-Cells(l))+c+l+reset)
	}
	if version != "" {
		lines = append(lines, strings.Repeat(" ", width-Cells(version))+v+version+reset)
	}
	return lines, width
}
