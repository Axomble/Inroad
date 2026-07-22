package personalize

import "testing"

func TestAllKnownFields(t *testing.T) {
	v := Vars{FirstName: "Ada", LastName: "Lovelace", Email: "ada@x.io", Company: "Analytical"}
	got := Text("{{first_name}} {{last_name}} <{{email}}> @ {{company}}", v)
	want := "Ada Lovelace <ada@x.io> @ Analytical"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCustomFieldSubstitution(t *testing.T) {
	v := Vars{FirstName: "Ada", Custom: map[string]string{"city": "London"}}
	if got := Text("from {{custom.city}}", v); got != "from London" {
		t.Fatalf("got %q", got)
	}
}

func TestUnknownCustomFieldIsEmpty(t *testing.T) {
	if got := Text("X{{custom.missing}}Y", Vars{}); got != "XY" {
		t.Fatalf("got %q want %q", got, "XY")
	}
}

func TestHTMLEscapesValues(t *testing.T) {
	got := HTML("Hi {{first_name}}", Vars{FirstName: "<b>Ada</b>"})
	want := "Hi &lt;b&gt;Ada&lt;/b&gt;"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHTMLEscapesCustomValues(t *testing.T) {
	got := HTML("{{custom.note}}", Vars{Custom: map[string]string{"note": "<script>"}})
	if got != "&lt;script&gt;" {
		t.Fatalf("got %q", got)
	}
}

func TestEmptyFirstNameFallsBackToThere(t *testing.T) {
	if got := Text("Hi {{first_name}}", Vars{}); got != "Hi there" {
		t.Fatalf("got %q", got)
	}
}
