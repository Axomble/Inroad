//go:build tools

// Package tools anchors dependencies that parallel scaffold agents import
// before their own code lands, so `go mod tidy` retains them and fully
// populates go.sum. Excluded from normal builds via the `tools` build tag.
// Removed (and go.mod re-tidied) once all scaffold code exists.
package tools

import (
	_ "github.com/go-chi/chi/v5"
	_ "github.com/go-chi/chi/v5/middleware"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/google/uuid"
	_ "github.com/hibiken/asynq"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/vgarvardt/pgx-google-uuid/v5"
	_ "golang.org/x/crypto/bcrypt"
)
