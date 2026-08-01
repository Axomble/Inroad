package contact

import (
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// The keyset search SQL is assembled here rather than written out as sqlc
// queries because the access paths multiply: three sorts x two travel
// directions x optional text filter x optional list filter is two dozen
// near-identical statements. Every optional clause is therefore OMITTED when
// unused rather than neutralised with a "$n IS NULL OR ..." guard — a guard
// like that survives into the plan and stops Postgres from turning the
// predicate into an index condition, which is the entire point of this change.
//
// Nothing caller-controlled is ever concatenated: every fragment below comes
// from a closed set of constants, and every value travels as a placeholder.
// searchSQL/countSQL are pure string builders so the generated statements are
// asserted directly in search_test.go.

// searchColumns is the projection both the page query and the row scan agree
// on. lower(email) is selected rather than lower-cased in Go so the cursor key
// is byte-identical to the value idx_contacts_ws_email_id is built on.
const searchColumns = `c.id, c.email, c.first_name, c.created_at, lower(c.email) AS sort_email`

// sortKey is the indexed expression a sort orders by.
func sortKey(s cursor.Sort) string {
	if s == cursor.SortEmail {
		return "lower(c.email)"
	}
	return "c.created_at"
}

// descending reports whether the sort's natural (first-page) travel is from
// high keys to low. Only "newest" reads backwards through time.
func descending(s cursor.Sort) bool { return s == cursor.SortNewest }

// scanDescending resolves the direction the index is actually walked in.
// Travelling Before a cursor reverses the sort's natural direction; the rows
// come back reversed and the service flips them into display order.
func scanDescending(s cursor.Sort, dir cursor.Direction) bool {
	if dir == cursor.Before {
		return !descending(s)
	}
	return descending(s)
}

// likeEscaper neutralises the LIKE metacharacters. A user typing "%" means the
// literal character, not "match anything" — left unescaped it would turn a
// keystroke into a pattern the trigram index cannot serve, which is a latency
// footgun as much as a correctness one. Backslash is LIKE's default escape
// character, so no ESCAPE clause is needed.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(s string) string { return likeEscaper.Replace(s) }

// argList accumulates positional placeholders so a clause never has to know
// which $n it is.
type argList struct{ args []any }

func (a *argList) add(v any) string {
	a.args = append(a.args, v)
	return "$" + strconv.Itoa(len(a.args))
}

// where builds the shared predicate: the workspace pin (always first, always
// from the JWT), then the optional text and list filters.
func (f SearchFilter) where(ws uuid.UUID, a *argList) []string {
	conds := []string{"c.workspace_id = " + a.add(ws)}
	if f.Query != "" {
		// Substring match against the generated, already-lower-cased column;
		// this is what idx_contacts_search serves.
		conds = append(conds, "c.search_text LIKE '%' || "+a.add(escapeLike(f.Query))+" || '%'")
	}
	if f.ListID != nil {
		// A semi-join rather than a JOIN: EXISTS cannot duplicate a contact row
		// and leaves contacts as the driving table for the ordering index.
		conds = append(conds, "EXISTS (SELECT 1 FROM list_members lm WHERE lm.contact_id = c.id AND lm.list_id = "+a.add(*f.ListID)+")")
	}
	return conds
}

// searchSQL builds one page query and its arguments. cur is nil for the first
// page, in which case no keyset comparison is emitted at all and the scan
// starts at the index's edge.
func searchSQL(ws uuid.UUID, f SearchFilter, sort cursor.Sort, cur *cursor.Cursor, limit int) (string, []any) {
	a := &argList{}
	conds := f.where(ws, a)

	desc := scanDescending(sort, cursor.After)
	if cur != nil {
		desc = scanDescending(sort, cur.Direction)
		// A row comparison, not two ORed predicates: (key, id) < (k, i) is a
		// single index condition, so the scan seeks straight to the cursor
		// instead of walking and discarding rows the way OFFSET does.
		cmp := ">"
		if desc {
			cmp = "<"
		}
		conds = append(conds,
			"("+sortKey(sort)+", c.id) "+cmp+" ("+a.add(cur.Key())+", "+a.add(cur.ID)+")")
	}

	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	// id repeats the direction so the ordering is total: a bulk import gives
	// thousands of contacts the same created_at, and without the tiebreak a
	// page could repeat or skip them.
	order := sortKey(sort) + " " + dir + ", c.id " + dir

	return "SELECT " + searchColumns + "\nFROM contacts c\nWHERE " +
		strings.Join(conds, "\n  AND ") +
		"\nORDER BY " + order +
		"\nLIMIT " + a.add(int32(limit)), a.args
}

// countSQL builds the bounded match count. Counting through a subquery capped
// at capAt+1 rows means the cost of the number under a search box is bounded by
// construction; the caller reports anything at or above capAt as capped.
func countSQL(ws uuid.UUID, f SearchFilter, capAt int) (string, []any) {
	a := &argList{}
	conds := f.where(ws, a)
	return "SELECT count(*) FROM (\n  SELECT 1 FROM contacts c\n  WHERE " +
		strings.Join(conds, "\n    AND ") +
		"\n  LIMIT " + a.add(int32(capAt+1)) + "\n) t", a.args
}
