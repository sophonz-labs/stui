package ui

import (
	"os"
	"strings"
)

// logo is stui's name, top right of the header as k9s puts its own.
var logo = []string{
	` _ _|_     o`,
	`_>  |_ |_| |`,
}

const logoWidth = 12

// logoMin is the narrowest terminal that shows the logo: below it the
// columns of keys need the room more.
const logoMin = 96

// logoLines is the logo and the version under it, each logoWidth columns,
// or nil when the terminal is too narrow. NO_COLOR gets plain text.
func logoLines(version string, w int) []string {
	if w < logoMin {
		return nil
	}
	c, v := cTeal+bold, cGray
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		c, v = "", ""
	}
	out := make([]string, 0, 3)
	for _, l := range logo {
		out = append(out, c+fit(l, logoWidth)+reset)
	}
	if version != "" {
		pad := max(0, logoWidth-Cells(version))
		out = append(out, strings.Repeat(" ", pad)+v+fit(version, logoWidth-pad)+reset)
	}
	return out
}
