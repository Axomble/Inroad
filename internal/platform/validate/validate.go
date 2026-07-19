// Package validate wraps go-playground/validator with a single shared instance
// and a field-keyed error type handlers can map to HTTP 400.
package validate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

var v = validator.New(validator.WithRequiredStructEnabled())

// Error is a validation failure keyed by struct field name.
type Error struct{ Fields map[string]string }

func (e *Error) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for f, m := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f, m))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Struct validates s by its `validate` tags. Returns nil or *Error.
func Struct(s any) error {
	err := v.Struct(s)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return err
	}
	fields := make(map[string]string, len(verrs))
	for _, fe := range verrs {
		fields[fe.Field()] = fe.Tag()
	}
	return &Error{Fields: fields}
}

// IsValidationError reports whether err is a *Error.
func IsValidationError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
