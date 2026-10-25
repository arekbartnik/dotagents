package orch

import (
	"bytes"
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
)

// countValue is a tally as the TypeScript store left it in a plain object.
// A name inherited from Object.prototype started from that member's source
// text, so each `+ 1` appended "1". The quirk reaches status.md and --json.
type countValue struct {
	n    int64
	text string
}

func (v countValue) String() string {
	if v.text != "" {
		return v.text
	}
	return strconv.FormatInt(v.n, 10)
}

func (v countValue) MarshalJSON() ([]byte, error) {
	if v.text != "" {
		return marshalJSON(v.text, false), nil
	}
	return strconv.AppendInt(nil, v.n, 10), nil
}

var inheritedMembers = map[string]string{
	"constructor":          "function Object() { [native code] }",
	"__defineGetter__":     "function __defineGetter__() { [native code] }",
	"__defineSetter__":     "function __defineSetter__() { [native code] }",
	"hasOwnProperty":       "function hasOwnProperty() { [native code] }",
	"__lookupGetter__":     "function __lookupGetter__() { [native code] }",
	"__lookupSetter__":     "function __lookupSetter__() { [native code] }",
	"isPrototypeOf":        "function isPrototypeOf() { [native code] }",
	"propertyIsEnumerable": "function propertyIsEnumerable() { [native code] }",
	"toString":             "function toString() { [native code] }",
	"valueOf":              "function valueOf() { [native code] }",
	"toLocaleString":       "function toLocaleString() { [native code] }",
}

// counts is an ordered tally. "__proto__" is never stored, since assigning
// to it on a plain object was a no-op.
type counts struct {
	keys   []string
	values map[string]countValue
}

func countValues(values []string) counts {
	c := counts{values: map[string]countValue{}}
	for _, value := range values {
		if value == "__proto__" {
			continue
		}
		current, ok := c.values[value]
		switch {
		case !ok:
			c.keys = append(c.keys, value)
			if source, inherited := inheritedMembers[value]; inherited {
				current.text = source + "1"
			} else {
				current.n = 1
			}
		case current.text != "":
			current.text += "1"
		default:
			current.n++
		}
		c.values[value] = current
	}
	slices.SortStableFunc(c.keys, localeCompare)
	c.keys = objectKeyOrder(c.keys)
	return c
}

// get is `counts[name] ?? 0`, which also sees inherited members.
func (c counts) get(name string) countValue {
	if value, ok := c.values[name]; ok {
		return value
	}
	return countValue{text: inheritedMembers[name]}
}

func (c counts) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range c.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(marshalJSON(key, false))
		b.WriteByte(':')
		b.Write(marshalJSON(c.values[key], false))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func countLine(c counts) string {
	if len(c.keys) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(c.keys))
	for _, key := range c.keys {
		parts = append(parts, key+"="+c.values[key].String())
	}
	return strings.Join(parts, ", ")
}

// objectKeyOrder moves array-index keys to the front in numeric order, the
// order a JavaScript object enumerated them in.
func objectKeyOrder(keys []string) []string {
	var indexes, rest []string
	for _, key := range keys {
		if _, ok := arrayIndex(key); ok {
			indexes = append(indexes, key)
		} else {
			rest = append(rest, key)
		}
	}
	slices.SortFunc(indexes, func(a, b string) int {
		x, _ := arrayIndex(a)
		y, _ := arrayIndex(b)
		return cmp.Compare(x, y)
	})
	return append(indexes, rest...)
}

func arrayIndex(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < math.MaxUint32 && strconv.FormatUint(n, 10) == key
}

// collationOrder is the ASCII order of the ICU root collation behind
// localeCompare: whitespace, punctuation and symbols, digits, then letters,
// with case only breaking ties. Anything else sorts after, by code point.
const collationOrder = "\t\n\v\f\r _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

func localeCompare(a, b string) int {
	ap, at := collationKey(a)
	bp, bt := collationKey(b)
	return cmp.Or(slices.Compare(ap, bp), slices.Compare(at, bt))
}

func collationKey(s string) (primary, tertiary []int) {
	for _, r := range s {
		upper := 0
		if 'A' <= r && r <= 'Z' {
			r, upper = r+'a'-'A', 1
		}
		weight := strings.IndexRune(collationOrder, r)
		if weight < 0 {
			weight = len(collationOrder) + int(r)
		}
		primary = append(primary, weight)
		tertiary = append(tertiary, upper)
	}
	return primary, tertiary
}
