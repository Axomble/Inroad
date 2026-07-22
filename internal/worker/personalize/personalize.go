// Package personalize substitutes {{...}} template placeholders in email
// subjects and bodies. Shared by the direct sender and the sequence advance
// handler so both apply identical rules. Pure functions, no data access.
package personalize

import (
	"html"
	"log/slog"
	"regexp"
	"strings"
)

// Vars are the values available to a template. Custom holds arbitrary
// per-contact fields, addressed as {{custom.<key>}}.
type Vars struct {
	FirstName string
	LastName  string
	Email     string
	Company   string
	Custom    map[string]string
}

// leftoverRE matches any placeholder still present after substitution
// (including dotted custom keys). Warned so operators spot template typos
// without breaking sends.
var leftoverRE = regexp.MustCompile(`\{\{[a-zA-Z_.]+\}\}`)

// customRE captures {{custom.<key>}} where key is [a-zA-Z0-9_].
var customRE = regexp.MustCompile(`\{\{custom\.([a-zA-Z0-9_]+)\}\}`)

// Text substitutes placeholders for a plain-text body (values inserted
// verbatim, no escaping — a header/text context).
func Text(tmpl string, v Vars) string { return substitute(tmpl, v, false) }

// HTML substitutes placeholders for an HTML body, escaping every substituted
// value so a hostile contact field can't inject markup.
func HTML(tmpl string, v Vars) string { return substitute(tmpl, v, true) }

func substitute(tmpl string, v Vars, escape bool) string {
	name := v.FirstName
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	enc := func(s string) string {
		if escape {
			return html.EscapeString(s)
		}
		return s
	}
	// Custom fields first: a fixed key ("first_name" etc.) can never collide
	// with the dotted custom.* namespace, so order only matters for clarity.
	out := customRE.ReplaceAllStringFunc(tmpl, func(m string) string {
		key := customRE.FindStringSubmatch(m)[1]
		return enc(v.Custom[key])
	})
	r := strings.NewReplacer(
		"{{first_name}}", enc(name),
		"{{last_name}}", enc(v.LastName),
		"{{email}}", enc(v.Email),
		"{{company}}", enc(v.Company),
	)
	out = r.Replace(out)
	for _, m := range leftoverRE.FindAllString(out, -1) {
		slog.Warn("unknown template placeholder", "placeholder", m)
	}
	return out
}
