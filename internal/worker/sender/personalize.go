package sender

import (
	"html"
	"log/slog"
	"regexp"
	"strings"
)

// unknownPlaceholderRE matches leftover placeholders of the form {{name}} that
// were not substituted. Emitted as a warn log so operators can spot template
// mistakes without breaking sends.
var unknownPlaceholderRE = regexp.MustCompile(`\{\{[a-zA-Z_]+\}\}`)

// personalizeText substitutes {{first_name}} and {{email}} for a plain-text
// body. Values are inserted verbatim (no escaping). An empty first name falls
// back to "there" so greetings read naturally.
func personalizeText(tmpl, firstName, email string) string {
	name := firstName
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	r := strings.NewReplacer("{{first_name}}", name, "{{email}}", email)
	out := r.Replace(tmpl)
	warnUnknownPlaceholders(out)
	return out
}

// personalizeHTML substitutes {{first_name}} and {{email}} for an HTML body,
// running html.EscapeString on each substituted value first. Without this a
// hostile contact name or address could inject a <script> tag into the
// rendered message.
func personalizeHTML(tmpl, firstName, email string) string {
	name := firstName
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	r := strings.NewReplacer(
		"{{first_name}}", html.EscapeString(name),
		"{{email}}", html.EscapeString(email),
	)
	out := r.Replace(tmpl)
	warnUnknownPlaceholders(out)
	return out
}

func warnUnknownPlaceholders(s string) {
	for _, m := range unknownPlaceholderRE.FindAllString(s, -1) {
		slog.Warn("unknown template placeholder", "placeholder", m)
	}
}
