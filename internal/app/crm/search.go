package crm

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// minCompanyQueryLen mirrors the contact search's floor: below it a
	// trigram substring match stops being selective, so a one-character query
	// is refused rather than answered with a scan pretending to be a search.
	minCompanyQueryLen = 2
	// maxCompanyQueryLen bounds the pattern. A company name is at most 200
	// characters and a domain at most 253, so nothing longer can match.
	maxCompanyQueryLen = 460
)

// normalizeCompanyFilter returns the canonical query: trimmed and lower-cased,
// because the indexed expression is lower-cased and the cursor binds to the
// exact text searched. Out-of-range input is rejected, never truncated.
func normalizeCompanyFilter(filter CompanyFilter) (CompanyFilter, error) {
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return CompanyFilter{}, nil
	}
	length := utf8.RuneCountInString(query)
	if length < minCompanyQueryLen {
		return CompanyFilter{}, validation(fmt.Sprintf("search query must be at least %d characters", minCompanyQueryLen))
	}
	if length > maxCompanyQueryLen {
		return CompanyFilter{}, validation(fmt.Sprintf("search query must be at most %d characters", maxCompanyQueryLen))
	}
	return CompanyFilter{Query: query}, nil
}
