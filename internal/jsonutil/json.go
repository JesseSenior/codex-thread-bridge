// Package jsonutil preserves arbitrary JSON values and the legacy cursor encoding.
package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

type Object = map[string]any

func Decode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid trailing JSON")
	}
	return nil
}
func Map(v any) Object    { m, _ := v.(map[string]any); return m }
func List(v any) []any    { a, _ := v.([]any); return a }
func String(v any) string { s, _ := v.(string); return s }
func Default(m Object, k string, fallback any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return fallback
}
func Int(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case json.Number:
		i, err := strconv.Atoi(string(n))
		if err == nil {
			return i
		}
		f, _ := n.Float64()
		return int(f)
	case float64:
		return int(n)
	}
	return 0
}
func Equal(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }

// PythonJSON matches json.dumps(sort_keys=True): ASCII escapes and spaced separators.
// This byte representation is part of the version 1 wait cursor contract.
func PythonJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range x {
			switch r {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if r < 32 || r >= 127 {
					if r > 0xffff {
						hi, lo := utf16.EncodeRune(r)
						fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
					} else {
						fmt.Fprintf(&b, `\u%04x`, r)
					}
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
		return b.String()
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, PythonJSON(k)+": "+PythonJSON(x[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, a := range x {
			parts[i] = PythonJSON(a)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case json.Number:
		if !strings.ContainsAny(string(x), ".eE") {
			return string(x)
		}
		f, _ := x.Float64()
		return pythonFloat(f)
	case float64:
		return pythonFloat(x)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		var normalized any
		if err = Decode(data, &normalized); err != nil {
			panic(err)
		}
		return PythonJSON(normalized)
	}
}
func pythonFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	p := strings.Split(s, "e")
	exp, _ := strconv.Atoi(p[1])
	if exp >= -4 && exp < 16 {
		s = strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
	}
	return s
}
