package sender

import "strings"

// personalize substitutes {{first_name}} and {{email}}. An empty first name
// falls back to "there" so greetings read naturally.
func personalize(tmpl, firstName, email string) string {
	name := firstName
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	r := strings.NewReplacer("{{first_name}}", name, "{{email}}", email)
	return r.Replace(tmpl)
}
