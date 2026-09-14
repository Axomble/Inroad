package main

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// describeCreateUserError turns a raw create-user/create-member error into an
// operator-facing message. identity.Service.Register/CreateMember pass a
// duplicate-email unique-violation straight through unmapped (the same shape
// identity/handler.go's own isUniqueViolation already checks for on the HTTP
// path — see its comment), so this is the CLI's side of that same contract
// rather than a new error shape.
func describeCreateUserError(email string, err error) error {
	if isUniqueViolation(err) {
		return errors.New("a user with email " + email + " already exists")
	}
	return err
}

// isUniqueViolation reports whether err is a Postgres unique-key violation
// (SQLSTATE 23505). Typed pgconn.PgError only, matching identity/handler.go's
// helper of the same name and reasoning: a substring check would fire on any
// error whose message happened to contain "23505".
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
