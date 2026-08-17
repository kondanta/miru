// Package auth provides local (bcrypt + JWT) and OIDC authentication.
// Note: app OIDC login and YouTube API OAuth are independent flows.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// Claims are the JWT payload fields issued on login.
type Claims struct {
	Username     string `json:"username"`
	IsAdmin      bool   `json:"is_admin"`
	TokenVersion int    `json:"token_version"`
	jwt.RegisteredClaims
}

// SignToken creates a signed JWT string for the given claims. The caller is
// responsible for setting claims.Subject (user ID); SignToken only sets the
// time fields.
func SignToken(claims Claims, secret []byte, ttl time.Duration) (string, error) {
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(ttl))
	claims.IssuedAt = jwt.NewNumericDate(time.Now())
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// ParseToken verifies the signature and returns the claims.
// Returns ErrTokenInvalid for any verification failure.
func ParseToken(tokenStr string, secret []byte) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

// HashPassword hashes password with bcrypt at cost 12.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword returns nil if password matches hash, ErrWrongPassword otherwise.
func CheckPassword(hash, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return ErrWrongPassword
	}
	if err != nil {
		return fmt.Errorf("check password: %w", err)
	}
	return nil
}

// Sentinel errors.
var (
	ErrTokenInvalid  = errors.New("invalid token")
	ErrWrongPassword = errors.New("wrong password")
)
