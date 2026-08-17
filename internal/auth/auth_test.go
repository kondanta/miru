package auth

import (
	"testing"
	"time"
)

var testSecret = []byte("test-secret-that-is-long-enough!!")

func TestSignAndParseToken(t *testing.T) {
	tests := []struct {
		name   string
		claims Claims
		ttl    time.Duration
	}{
		{
			name: "valid token round-trips",
			claims: Claims{
				Username:     "alice",
				IsAdmin:      false,
				TokenVersion: 1,
			},
			ttl: time.Hour,
		},
		{
			name: "admin claim preserved",
			claims: Claims{
				Username:     "bob",
				IsAdmin:      true,
				TokenVersion: 3,
			},
			ttl: time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signed, err := SignToken(tt.claims, testSecret, tt.ttl)
			if err != nil {
				t.Fatalf("SignToken: %v", err)
			}

			got, err := ParseToken(signed, testSecret)
			if err != nil {
				t.Fatalf("ParseToken: %v", err)
			}

			if got.Username != tt.claims.Username {
				t.Errorf("username: got %q, want %q", got.Username, tt.claims.Username)
			}
			if got.IsAdmin != tt.claims.IsAdmin {
				t.Errorf("is_admin: got %v, want %v", got.IsAdmin, tt.claims.IsAdmin)
			}
			if got.TokenVersion != tt.claims.TokenVersion {
				t.Errorf("token_version: got %d, want %d", got.TokenVersion, tt.claims.TokenVersion)
			}
		})
	}
}

func TestParseToken_Expired(t *testing.T) {
	signed, err := SignToken(Claims{Username: "alice"}, testSecret, -time.Second)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	_, err = ParseToken(signed, testSecret)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestParseToken_WrongSecret(t *testing.T) {
	signed, err := SignToken(Claims{Username: "alice"}, testSecret, time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	_, err = ParseToken(signed, []byte("different-secret-also-long-enough"))
	if err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
}

func TestParseToken_Tampered(t *testing.T) {
	signed, err := SignToken(Claims{Username: "alice"}, testSecret, time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	tampered := signed[:len(signed)-4] + "xxxx"
	_, err = ParseToken(tampered, testSecret)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestHashAndCheckPassword(t *testing.T) {
	tests := []struct {
		name      string
		password  string
		checkWith string
		wantErr   bool
	}{
		{name: "correct password", password: "hunter2", checkWith: "hunter2", wantErr: false},
		{name: "wrong password", password: "hunter2", checkWith: "wrong", wantErr: true},
		{name: "empty password", password: "", checkWith: "", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := HashPassword(tt.password)
			if err != nil {
				t.Fatalf("HashPassword: %v", err)
			}

			err = CheckPassword(hash, tt.checkWith)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckPassword: got err=%v, wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr && err != ErrWrongPassword {
				t.Errorf("expected ErrWrongPassword, got %v", err)
			}
		})
	}
}
