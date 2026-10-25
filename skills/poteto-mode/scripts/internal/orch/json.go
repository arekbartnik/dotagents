package orch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

const maxSafeInteger = 1<<53 - 1

// encoding/json always escapes U+2028 and U+2029, but the store's files and
// output have always carried them raw. Matching `\\` first keeps an escaped
// backslash followed by "u2028" intact.
var lineSeparators = strings.NewReplacer(`\\`, `\\`, `\u2028`, "\u2028", `\u2029`, "\u2029")

// marshalJSON encodes value compactly, or indented by two spaces, without
// HTML escaping and without a trailing newline.
func marshalJSON(value any, indent bool) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(value); err != nil {
		panic(err)
	}
	return []byte(lineSeparators.Replace(strings.TrimSuffix(buf.String(), "\n")))
}

// decodeJSON parses exactly one JSON value. Numbers stay json.Number so an
// out-of-range number is a bad value rather than invalid JSON.
func decodeJSON(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return value, nil
}

func jsonNumber(value any) (float64, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	return f, err == nil || errors.Is(err, strconv.ErrRange)
}

func jsonSafeInteger(value any) (int64, bool) {
	f, ok := jsonNumber(value)
	if !ok || f != math.Trunc(f) || math.Abs(f) > maxSafeInteger {
		return 0, false
	}
	return int64(f), true
}

// formatNumber prints a number read back from status.md the way the change
// line always has: plain decimals, exponents only outside [1e-6, 1e21).
func formatNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	if a := math.Abs(f); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	return strings.NewReplacer("e-0", "e-", "e+0", "e+").Replace(s)
}
