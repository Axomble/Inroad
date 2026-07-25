// Package auth handles registration, login, password hashing, and JWT sessions.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func CheckPassword(encoded, pw string) bool {
	h, err := decodeArgon(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), h.salt, h.t, h.m, h.p, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(got, h.key) == 1
}

// argonHash holds the parsed components of an encoded argon2id password hash.
type argonHash struct {
	salt, key []byte
	t, m      uint32
	p         uint8
}

func decodeArgon(encoded string) (argonHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argonHash{}, errors.New("bad argon2 hash")
	}
	var (
		h       argonHash
		version int
		err     error
	)
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argonHash{}, err
	}
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.m, &h.t, &h.p); err != nil {
		return argonHash{}, err
	}
	if h.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return argonHash{}, err
	}
	if h.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return argonHash{}, err
	}
	return h, nil
}
