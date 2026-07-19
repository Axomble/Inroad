package workspace

import (
	"context"
	"errors"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrInvalidCredentials is returned when email/password authentication fails.
var ErrInvalidCredentials = errors.New("invalid credentials")

type Service struct {
	store *Store
}

func NewService(store *Store) *Service { return &Service{store: store} }

type RegisterInput struct {
	WorkspaceName string
	Email         string
	Password      string
}

type RegisterResult struct {
	WorkspaceID string
	UserID      string
}

// Register creates a workspace and its first (owner) user atomically enough for v1.
func (s *Service) Register(ctx context.Context, in RegisterInput) (RegisterResult, error) {
	ws, err := s.store.CreateWorkspace(ctx, in.WorkspaceName)
	if err != nil {
		return RegisterResult{}, err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return RegisterResult{}, err
	}
	user, err := s.store.CreateUser(ctx, gen.CreateUserParams{
		WorkspaceID:  ws.ID,
		Email:        in.Email,
		PasswordHash: hash,
		Role:         "owner",
	})
	if err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{
		WorkspaceID: ws.ID.String(),
		UserID:      user.ID.String(),
	}, nil
}

// Authenticate verifies credentials and returns the user and workspace ids.
func (s *Service) Authenticate(ctx context.Context, email, password string) (string, string, error) {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}
	if !auth.CheckPassword(user.PasswordHash, password) {
		return "", "", ErrInvalidCredentials
	}
	return user.ID.String(), user.WorkspaceID.String(), nil
}
