package proc

import "testing"

func TestZombieStat(t *testing.T) {
	for stat, want := range map[string]bool{
		"8979 (stui) Z 1 8979 8979 0 -1":        true,
		"8979 (stui) X 1 8979 8979 0 -1":        true,
		"8979 (stui) S 1 8979 8979 0 -1":        false,
		"8979 (a) Z (b)) S 1 8979 8979 0 -1":    false,
		"8979 (zsh (login)) Z 1 8979 8979 0 -1": true,
		"":                                      false,
	} {
		if got := zombieStat(stat); got != want {
			t.Errorf("%q: %v, want %v", stat, got, want)
		}
	}
}
