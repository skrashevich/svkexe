package llm

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ParseRetryAfter parses a Retry-After header value (RFC 7231 §7.1.3).
// It accepts either delta-seconds or an HTTP-date. Returns 0 if the header
// is missing or unparseable.
func ParseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Truncate clips s to at most n runes, appending an ellipsis when truncated.
// Whitespace runs are collapsed to single spaces so it renders well in a
// single-line log field. n counts runes (not bytes) and the cut always lands
// on a rune boundary, so multi-byte UTF-8 sequences are never split.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s[:i] + "…"
}
