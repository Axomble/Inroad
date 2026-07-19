package contact

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/mail"
	"strings"

	"github.com/google/uuid"
)

const maxImportRows = 50000

// ImportResult summarizes the outcome of a CSV import.
type ImportResult struct {
	Imported   int `json:"imported"`
	Skipped    int `json:"skipped"`
	Duplicates int `json:"duplicates"`
}

// importRows parses a headered CSV and upserts each valid row into the list.
// Columns are detected by header name (email required). Invalid emails are
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
	for _, name := range []string{"first_name", "last_name", "company"} {
		if _, ok := col[name]; !ok {
			col[name] = -1
		}
	}

	var res ImportResult
	rows := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
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
		in := UpsertInput{
			Email:     email,
			FirstName: field(rec, col["first_name"]),
			LastName:  field(rec, col["last_name"]),
			Company:   field(rec, col["company"]),
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

func field(rec []string, idx int) string {
	if idx < 0 || idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[idx])
}
