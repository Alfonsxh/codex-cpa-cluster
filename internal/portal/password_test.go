package portal

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func TestPasswordHashMatchesStableScryptContract(t *testing.T) {
	encoded, err := hashPasswordWithSalt("test-secret-123", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("hashPasswordWithSalt: %v", err)
	}
	if encoded != "scrypt$16384$8$1$30313233343536373839616263646566$87f7da1ccf1be3f4a4bbbe36dc37f432ffd2e3374099425c9bd560ccf3d50ead" {
		t.Fatalf("scrypt compatibility vector = %q", encoded)
	}
	if !VerifyPassword("test-secret-123", encoded) || VerifyPassword("wrong", encoded) {
		t.Fatal("scrypt password verification mismatch")
	}
}

func TestPasswordVerifierAcceptsLegacyPBKDF2AndRejectsUnsafeParameters(t *testing.T) {
	salt := []byte("compatibility-salt")
	digest := pbkdf2.Key([]byte("legacy-secret"), salt, 310_000, 32, sha256.New)
	encoded := "pbkdf2_sha256$310000$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(digest)
	if !VerifyPassword("legacy-secret", encoded) || VerifyPassword("wrong", encoded) {
		t.Fatal("PBKDF2 compatibility verification mismatch")
	}
	if VerifyPassword("legacy-secret", "pbkdf2_sha256$1$"+hex.EncodeToString(salt)+"$"+hex.EncodeToString(digest)) {
		t.Fatal("unsafe PBKDF2 iteration count was accepted")
	}
}

func TestNewPasswordPolicyRejectsLegacyDefault(t *testing.T) {
	for _, password := range []string{"", "short", "123456"} {
		if err := ValidateNewPassword(password); err == nil {
			t.Fatalf("invalid new password %q was accepted", password)
		}
	}
	if err := ValidateNewPassword("secure-password"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}
