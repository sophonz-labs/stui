package chat

import (
	"maps"
	"slices"
	"strconv"
	"time"
)

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// timestamp is a record's time in Unix milliseconds, 0 when it has none.
func timestamp(v any) int64 {
	switch t := v.(type) {
	case string:
		if p, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return p.UnixMilli()
		}
	case float64:
		// Seconds or milliseconds.
		if t < 1e11 {
			return int64(t * 1000)
		}
		return int64(t)
	}
	return 0
}

// offsetID names a record that has no id of its own by where it starts:
// stable for as long as the file is, and GET /api/chat/block finds it at once.
func offsetID(off int64) string { return "o" + strconv.FormatInt(off, 10) }

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }
