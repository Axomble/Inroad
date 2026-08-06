package db

import "strings"

// likeEscaper neutralises the LIKE metacharacters so a caller's literal "%"
// or "_" is matched as itself, not as a wildcard. Backslash is LIKE's default
// escape character, so no ESCAPE clause is needed on the query side.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike returns s with every LIKE metacharacter neutralised, safe to
// drop into a `'%' || EscapeLike(s) || '%'` pattern without a caller's
// literal "%" or "_" keystroke being treated as a wildcard operator. Shared
// by every app/* domain that builds a LIKE/ILIKE substring search (currently
// contact and inbox) — it carries no domain knowledge of its own, so it
// belongs here rather than being duplicated per domain (app/* packages never
// import each other, but every one of them may import platform/*).
func EscapeLike(s string) string { return likeEscaper.Replace(s) }
