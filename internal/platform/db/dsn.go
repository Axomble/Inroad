package db

import "strings"

// poolParamPrefix marks the DSN keys that belong to pgxpool rather than to
// Postgres: pool_max_conns, pool_min_conns, pool_max_conn_lifetime, and friends.
// pgxpool.ParseConfig consumes them, but every other pgx entry point —
// pgx.Connect, and golang-migrate's pgx5 driver — treats an unknown key as a
// server configuration parameter and the startup packet is then rejected with
// `unrecognized configuration parameter "pool_max_conns"`.
const poolParamPrefix = "pool_"

// WithoutPoolParams strips the pgxpool-only keys from a URL-form DSN so the same
// string can size a pool AND be handed to a non-pool consumer. That is what makes
// pinning pool_max_conns safe: the integration suite pins it to keep its pools
// small, then passes the very same DSN to Migrate.
//
// A returned DSN is byte-identical to the input when there is nothing to strip,
// so a DSN that never had pool keys cannot be reshaped by round-tripping.
// Keyword/value DSNs ("host=… pool_max_conns=…") are not handled; this repo
// resolves DSNs from URLs only.
func WithoutPoolParams(dsn string) string {
	base, rawQuery, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn
	}
	params := strings.Split(rawQuery, "&")
	kept := make([]string, 0, len(params))
	for _, param := range params {
		key, _, _ := strings.Cut(param, "=")
		if strings.HasPrefix(key, poolParamPrefix) {
			continue
		}
		kept = append(kept, param)
	}
	if len(kept) == len(params) {
		return dsn
	}
	if len(kept) == 0 {
		return base
	}
	return base + "?" + strings.Join(kept, "&")
}

// pinsPoolParam reports whether the DSN sets the given pgxpool key as a query
// parameter. Connect uses it to tell "the caller chose a pool size" from "nobody
// said", so it must match on the key and not merely on the substring — a
// credential or database name containing the text is not a choice.
func pinsPoolParam(dsn, key string) bool {
	_, rawQuery, ok := strings.Cut(dsn, "?")
	if !ok {
		return false
	}
	for _, param := range strings.Split(rawQuery, "&") {
		if name, _, _ := strings.Cut(param, "="); name == key {
			return true
		}
	}
	return false
}
