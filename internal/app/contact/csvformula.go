package contact

import "strings"

// This file owns BOTH directions of one rule, deliberately together: the export
// escape and the import un-escape are a single bijection, and splitting them
// across export.go and import.go is how a round trip silently stops round
// tripping.
//
// The problem: Excel, LibreOffice and Google Sheets evaluate a cell whose first
// character is one of = + - @ TAB CR as a formula. Every cell this domain writes
// to CSV — email, first/last name, company, and every custom-field value — is
// attacker-influenceable through POST /contacts/import or the contacts API, so a
// contact called `=HYPERLINK("http://evil","click")` executes when an operator
// opens their own export. That is CSV injection, and there was no escaping
// anywhere in this repo.
//
// The fix cannot be naive, because the trigger set overlaps real data: a phone
// number in a custom field starts with "+", a negative number with "-", and
// GET /contacts.csv's own OpenAPI description promises that "a file this
// endpoint produces re-imports unchanged". So the escape is UNDONE on import.

// formulaTriggers are the characters a spreadsheet treats as the start of a
// formula. TAB and CR are included because both are stripped or re-interpreted
// on paste, which puts the following character first.
const formulaTriggers = "=+-@\t\r"

// formulaEscape is the prefix both Excel and Google Sheets read as "the rest of
// this cell is text". One apostrophe, not a space or a leading tab: those change
// the value, and this one is removable again from the exact same information.
const formulaEscape = '\''

// escapeFormula returns v with a single apostrophe prefixed if a spreadsheet
// would otherwise execute it.
//
// PRECEDENCE, because a leading apostrophe can also be genuine data: the escape
// marker wins. A value that would READ BACK as an escape gets one more
// apostrophe than it started with, so a stored value of apostrophe-then-"=1+1"
// exports with TWO apostrophes and unescapeFormula returns the one. A value
// whose apostrophes guard ordinary text (`'tis`) is not an escape at all and is
// left exactly as it is. The pair is a bijection over every string, which is
// what the round-trip promise requires — asserted case by case in
// exportsafety_test.go.
func escapeFormula(v string) string {
	if !wouldBeEscaped(v) {
		return v
	}
	return string(formulaEscape) + v
}

// unescapeFormula removes the ONE apostrophe escapeFormula would have added,
// and only when it is really there.
//
// A file from another system is not one of ours: a bare `=1+1` was never
// escaped, so nothing is stripped and the formula is stored as the text it is;
// and an apostrophe over ordinary text is data, so `'tis` stays `'tis`.
func unescapeFormula(v string) string {
	rest, ok := strings.CutPrefix(v, string(formulaEscape))
	if ok && wouldBeEscaped(rest) {
		return rest
	}
	return v
}

// wouldBeEscaped reports whether v is a value escapeFormula prefixes: one whose
// first character after any run of apostrophes is a trigger.
//
// The apostrophe run is what makes the pair invertible. Without it, `'=1+1`
// would export unchanged (its first character is not a trigger) and then import
// as `=1+1` — a silent one-character data loss on exactly the values this guard
// exists for.
func wouldBeEscaped(v string) bool {
	i := 0
	for i < len(v) && v[i] == formulaEscape {
		i++
	}
	return i < len(v) && strings.IndexByte(formulaTriggers, v[i]) >= 0
}
