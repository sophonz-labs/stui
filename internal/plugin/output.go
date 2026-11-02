package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Record is one row of a view's output: its fields by name. A JSON
// object's nested fields are joined with dots ("author.login").
type Record map[string]string

// ParseOutput reads a view command's stdout. json is an array of
// objects or one object per line (docker --format json); tsv is a header
// line of field names and then rows; lines is each line as "line".
// fields is the field names in the output's own order (tsv's header,
// json's sorted keys), for views without columns.
func ParseOutput(format string, out []byte) (recs []Record, fields []string, err error) {
	switch format {
	case "lines":
		for _, l := range lines(out) {
			recs = append(recs, Record{"line": l})
		}
		return recs, []string{"line"}, nil
	case "tsv":
		ls := lines(out)
		if len(ls) == 0 {
			return nil, nil, nil
		}
		fields = strings.Split(ls[0], "\t")
		for _, l := range ls[1:] {
			cells := strings.Split(l, "\t")
			r := Record{}
			for i, f := range fields {
				if i < len(cells) {
					r[f] = cells[i]
				} else {
					r[f] = ""
				}
			}
			recs = append(recs, r)
		}
		return recs, fields, nil
	case "json":
		return parseJSON(out)
	}
	return nil, nil, fmt.Errorf("format %q", format)
}

func lines(out []byte) []string {
	var ls []string
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

func parseJSON(out []byte) ([]Record, []string, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil, nil
	}
	var objs []any
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber() // 1234 stays 1234, not 1.234e+03
	if out[0] == '[' {
		if err := dec.Decode(&objs); err != nil {
			return nil, nil, err
		}
	} else {
		for {
			var o any
			if err := dec.Decode(&o); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, nil, err
			}
			objs = append(objs, o)
		}
	}
	var recs []Record
	seen := map[string]bool{}
	var fields []string
	for i, o := range objs {
		m, ok := o.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("record %d is not an object", i+1)
		}
		r := Record{}
		flatten(r, "", m)
		for k := range r {
			if !seen[k] {
				seen[k] = true
				fields = append(fields, k)
			}
		}
		recs = append(recs, r)
	}
	sort.Strings(fields)
	return recs, fields, nil
}

func flatten(r Record, prefix string, m map[string]any) {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			flatten(r, prefix+k+".", sub)
			r[prefix+k] = text(v)
			continue
		}
		r[prefix+k] = text(v)
	}
}

// text is a JSON value as a cell: strings as they are, null as nothing,
// arrays as their items with commas, objects as compact JSON.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = text(e)
		}
		return strings.Join(parts, ", ")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Capped is a writer that keeps the first N bytes and drops the rest, so
// a command that prints forever cannot fill memory.
type Capped struct {
	N   int
	buf bytes.Buffer
	Cut bool
}

func (c *Capped) Write(p []byte) (int, error) {
	if room := c.N - c.buf.Len(); room < len(p) {
		c.Cut = true
		c.buf.Write(p[:max(0, room)])
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

func (c *Capped) Bytes() []byte { return c.buf.Bytes() }
