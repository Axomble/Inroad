package validate

import "testing"

type sample struct {
	Email string `validate:"required,email"`
	Name  string `validate:"required,max=5"`
}

func TestStructReportsFieldErrors(t *testing.T) {
	err := Struct(sample{Email: "nope", Name: "toolong"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := IsValidationError(err)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if _, has := ve.Fields["Email"]; !has {
		t.Errorf("expected Email field error, got %v", ve.Fields)
	}
}

func TestStructPassesValid(t *testing.T) {
	if err := Struct(sample{Email: "a@b.com", Name: "ok"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
