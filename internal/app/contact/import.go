package contact

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const maxImportRows = 50000

// builtinColumns are the header names that map to real contact columns. Any
// other header is a custom-field candidate, so this set is also what stops a
// workspace defining a custom field called "email" from hijacking the address
// column.
var builtinColumns = []string{"email", "first_name", "last_name", "company"}

// ImportResult summarizes the outcome of a CSV import.
type ImportResult struct {
	Imported   int `json:"imported"`
	Skipped    int `json:"skipped"`
	Duplicates int `json:"duplicates"`
	// MappedFields lists the custom field keys this file actually populated, so
	// the operator can see that their "industry" column landed somewhere rather
	// than inferring it from a contact page.
	MappedFields []string `json:"mapped_fields"`
	// IgnoredColumns lists headers that matched neither a built-in column nor a
	// live custom field. Previously these were dropped silently, which is the
	// specific complaint in issue #62: a column of enrichment data would
	// disappear with no signal at all.
	IgnoredColumns []string `json:"ignored_columns"`
	// InvalidValues counts cells rejected by their field's type (a "next week"
	// in a date column). The ROW still imports — one bad cell should not cost
	// the contact — so this is reported separately from Skipped.
	InvalidValues int `json:"invalid_values"`
}

// customColumn binds a CSV column index to the field definition it feeds.
type customColumn struct {
	idx int
	def FieldDef
}

// importRows parses a headered CSV and upserts each valid row into the list.
// Columns are detected by header name (email required). Unknown headers are
// matched against the workspace's live custom fields; anything left over is
// reported in IgnoredColumns rather than dropped in silence. Invalid emails are
// skipped and counted, never fatal.
func (s *Service) importRows(ctx context.Context, ws, listID uuid.UUID, r io.Reader) (ImportResult, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return ImportResult{}, errors.New("empty or unreadable CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	emailIdx, ok := col["email"]
	if !ok {
		return ImportResult{}, errors.New("CSV must have an 'email' column")
	}
	// Default missing optional columns to -1 so field() doesn't wrongly read
	// column 0 (the Go map zero value for an absent key).
	for _, name := range builtinColumns {
		if _, ok := col[name]; !ok {
			col[name] = -1
		}
	}

	defs, err := s.fields.ListFieldDefs(ctx, ws)
	if err != nil {
		return ImportResult{}, err
	}
	custom, ignored := mapCustomColumns(header, defs)

	res := ImportResult{
		MappedFields:   make([]string, 0, len(custom)),
		IgnoredColumns: ignored,
	}
	for _, c := range custom {
		res.MappedFields = append(res.MappedFields, c.def.Key)
	}

	rows := 0
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.Skipped++
			continue
		}
		rows++
		if rows > maxImportRows {
			return res, errors.New("CSV exceeds 50000 rows")
		}
		email := field(rec, emailIdx)
		if _, perr := mail.ParseAddress(email); perr != nil || email == "" {
			res.Skipped++
			continue
		}
		values, invalid := customValues(rec, custom)
		res.InvalidValues += invalid

		encoded, err := json.Marshal(values)
		if err != nil {
			// Only reachable if a value is not encodable, which a map of
			// strings never is; treating it as a skipped row rather than
			// ignoring it keeps the counts honest if that ever changes.
			res.Skipped++
			continue
		}
		in := UpsertInput{
			Email:        email,
			FirstName:    field(rec, col["first_name"]),
			LastName:     field(rec, col["last_name"]),
			Company:      field(rec, col["company"]),
			CustomFields: encoded,
		}
		id, inserted, err := s.store.Upsert(ctx, ws, in)
		if err != nil {
			res.Skipped++
			continue
		}
		if inserted {
			res.Imported++
		} else {
			res.Duplicates++
		}
		if err := s.store.AddToList(ctx, listID, id); err != nil {
			// membership failure is non-fatal for the row's count
			continue
		}
	}
	return res, nil
}

// mapCustomColumns pairs each non-builtin header with the live custom field of
// the same name, and reports the headers that matched nothing.
//
// Matching is on the lower-cased header because field keys are lower-case by
// construction (migration 000052), so a "Industry" column finds the `industry`
// field — the operator exported that CSV from somewhere else and should not
// have to re-case their headers to make an import work.
//
// A duplicated header maps only its first occurrence: two columns writing one
// key would make the stored value depend on column order, which is not
// something the file communicates.
func mapCustomColumns(header []string, defs []FieldDef) ([]customColumn, []string) {
	live := liveByKey(defs)
	var (
		custom  []customColumn
		ignored []string
		claimed = map[string]struct{}{}
	)
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(h))
		if name == "" || slices.Contains(builtinColumns, name) {
			continue
		}
		def, ok := live[name]
		if !ok {
			ignored = append(ignored, strings.TrimSpace(h))
			continue
		}
		if _, dup := claimed[name]; dup {
			continue
		}
		claimed[name] = struct{}{}
		custom = append(custom, customColumn{idx: i, def: def})
	}
	if ignored == nil {
		ignored = []string{}
	}
	return custom, ignored
}

// customValues coerces one row's custom cells, returning the values to store
// and how many were rejected.
//
// An empty cell is omitted rather than stored as "": UpsertContact MERGES this
// object into whatever the contact already has, so writing "" for a blank
// column would let a partial CSV erase enrichment a previous import supplied.
// A cell the field's type rejects is likewise omitted and counted — importing
// the contact without one bad value beats refusing the contact.
func customValues(rec []string, custom []customColumn) (map[string]string, int) {
	values := make(map[string]string, len(custom))
	invalid := 0
	for _, c := range custom {
		raw := field(rec, c.idx)
		if raw == "" {
			continue
		}
		value, err := c.def.CoerceValue(raw)
		if err != nil {
			invalid++
			continue
		}
		if value != "" {
			values[c.def.Key] = value
		}
	}
	return values, invalid
}

func field(rec []string, idx int) string {
	if idx < 0 || idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[idx])
}
