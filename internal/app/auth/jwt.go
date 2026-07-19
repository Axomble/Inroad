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
}

type jwtClaims struct {
	WorkspaceID string `json:"wid"`
	jwt.RegisteredClaims
}

func IssueToken(secret []byte, userID, workspaceID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwtClaims{
		WorkspaceID: workspaceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
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
	return Claims{UserID: c.Subject, WorkspaceID: c.WorkspaceID}, nil
}
