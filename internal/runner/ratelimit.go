package runner

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rateLimitMarkers are what a vendor says when it refuses a run for rate or
// usage limits. A failure that matches none of them is an ordinary failure.
var rateLimitMarkers = []string{
	"usage limit", "rate limit", "rate_limit", "too many requests", "overloaded_error", "quota exceeded",
}

// status429 is an HTTP 429 as a word of its own, not part of a number or id.
var status429 = regexp.MustCompile(`\b429\b`)

// resetEpoch is the "|<unix seconds>" suffix claude puts on a usage-limit message.
var resetEpoch = regexp.MustCompile(`\|(\d{9,11})\b`)

// rateLimitText reports whether a vendor failure message is a rate-limit
// refusal, and how long the vendor said to wait, if it did.
func rateLimitText(msg string) (bool, time.Duration) {
	low := strings.ToLower(msg)
	for _, m := range append(rateLimitMarkers, "429") {
		if strings.Contains(low, m) && (m != "429" || status429.MatchString(low)) {
			if g := resetEpoch.FindStringSubmatch(msg); g != nil {
				if n, err := strconv.ParseInt(g[1], 10, 64); err == nil {
					return true, untilEpoch(n)
				}
			}
			return true, 0
		}
	}
	return false, 0
}

// untilEpoch is the time from now until a unix time, never negative.
func untilEpoch(sec int64) time.Duration {
	if d := time.Until(time.Unix(sec, 0)); d > 0 {
		return d
	}
	return 0
}
