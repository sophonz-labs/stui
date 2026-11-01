package hooks

// Just enough of an order-keeping JSON document to edit someone's
// settings.json without reshuffling it: objects keep their keys in the order
// they were written, numbers keep their spelling, and the indent the file
// used is the indent it is written back with.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type member struct {
	key string
	val any
}

// object is a JSON object in document order. Values are *object, []any,
// string, json.Number, bool or nil.
type object struct{ members []member }

func (o *object) get(key string) (any, bool) {
	for _, m := range o.members {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

func (o *object) set(key string, v any) {
	for i, m := range o.members {
		if m.key == key {
			o.members[i].val = v
			return
		}
	}
	o.members = append(o.members, member{key, v})
}

func (o *object) del(key string) {
	for i, m := range o.members {
		if m.key == key {
			o.members = append(o.members[:i], o.members[i+1:]...)
			return
		}
	}
}

func parseDoc(b []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON document")
	}
	o, ok := v.(*object)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return o, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.members = append(o.members, member{key, v})
			}
			_, err := dec.Token()
			return o, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, errors.New("unexpected delimiter")
	default:
		return t, nil
	}
}

// detectIndent is the whitespace the first indented line starts with, or
// two spaces.
func detectIndent(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

func writeDoc(o *object, indent string, trailingNewline bool) []byte {
	var buf bytes.Buffer
	writeValue(&buf, o, indent, 0)
	if trailingNewline {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func writeString(buf *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	buf.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

func writeValue(buf *bytes.Buffer, v any, indent string, depth int) {
	pad := func(d int) {
		buf.WriteByte('\n')
		buf.WriteString(strings.Repeat(indent, d))
	}
	switch t := v.(type) {
	case *object:
		if len(t.members) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteByte('{')
		for i, m := range t.members {
			if i > 0 {
				buf.WriteByte(',')
			}
			pad(depth + 1)
			writeString(buf, m.key)
			buf.WriteString(": ")
			writeValue(buf, m.val, indent, depth+1)
		}
		pad(depth)
		buf.WriteByte('}')
	case []any:
		if len(t) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			pad(depth + 1)
			writeValue(buf, e, indent, depth+1)
		}
		pad(depth)
		buf.WriteByte(']')
	case string:
		writeString(buf, t)
	case json.Number:
		buf.WriteString(t.String())
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case nil:
		buf.WriteString("null")
	}
}
