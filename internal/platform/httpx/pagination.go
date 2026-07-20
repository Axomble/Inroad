package httpx

import (
	"net/http"
	"strconv"
)

// LimitOffset parses ?limit= and ?offset= from r.URL, clamping limit into
// [1, maxLimit] and floor'ing offset at 0. defaultLimit applies when the
// param is absent or unparseable. Handlers share this one implementation
// instead of hand-rolling clamp/max0/atoiDefault helpers per package.
func LimitOffset(r *http.Request, defaultLimit, maxLimit int) (limit, offset int) {
	limit = clamp(atoiDefault(r.URL.Query().Get("limit"), defaultLimit), 1, maxLimit)
	offset = max0(atoiDefault(r.URL.Query().Get("offset"), 0))
	return
}

func atoiDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
