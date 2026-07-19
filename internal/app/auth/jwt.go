package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the application claims embedded in a session token.
type Claims struct {
	UserID      string
	WorkspaceID string
	Role        string
	SessionID   string
}

type jwtClaims struct {
	WorkspaceID string `json:"wid"`
	Role        string `json:"role"`
	SessionID   string `json:"sid"`
	jwt.RegisteredClaims
}

func IssueToken(secret []byte, c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwtClaims{
		WorkspaceID: c.WorkspaceID,
		Role:        c.Role,
		SessionID:   c.SessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.UserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseToken(secret []byte, token string) (Claims, error) {
	var c jwtClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	return Claims{UserID: c.Subject, WorkspaceID: c.WorkspaceID, Role: c.Role, SessionID: c.SessionID}, nil
}
