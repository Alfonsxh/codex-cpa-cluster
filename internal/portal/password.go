package portal

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/scrypt"
)

const (
	PasswordMinimumLength = 8
	PasswordMaximumLength = 128
	legacyDefaultPassword = "123456"
	scryptN               = 16_384
	scryptR               = 8
	scryptP               = 1
	scryptKeyLength       = 32
)

var dummyPasswordHash = mustHashPasswordWithSalt("invalid-portal-user", make([]byte, 16))

func HashPassword(password string) (string, error) {
	if err := validatePasswordShape(password, true); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate portal password salt: %w", err)
	}
	return hashPasswordWithSalt(password, salt)
}

func VerifyPassword(password string, encoded string) bool {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) == 6 && parts[0] == "scrypt" {
		n, nError := strconv.Atoi(parts[1])
		r, rError := strconv.Atoi(parts[2])
		p, pError := strconv.Atoi(parts[3])
		if nError != nil || rError != nil || pError != nil || n != scryptN || r != scryptR || p != scryptP {
			return false
		}
		salt, saltError := hex.DecodeString(parts[4])
		expected, expectedError := hex.DecodeString(parts[5])
		if saltError != nil || expectedError != nil || len(salt) != 16 || len(expected) != scryptKeyLength {
			return false
		}
		actual, err := scrypt.Key([]byte(password), salt, n, r, p, len(expected))
		return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
	}
	if len(parts) == 4 && parts[0] == "pbkdf2_sha256" {
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations < 100_000 || iterations > 1_000_000 {
			return false
		}
		salt, saltError := hex.DecodeString(parts[2])
		expected, expectedError := hex.DecodeString(parts[3])
		if saltError != nil || expectedError != nil || len(salt) == 0 || len(expected) == 0 {
			return false
		}
		actual := pbkdf2.Key([]byte(password), salt, iterations, len(expected), sha256.New)
		return subtle.ConstantTimeCompare(actual, expected) == 1
	}
	return false
}

func ValidateCurrentPassword(password string) error {
	return validatePasswordShape(password, false)
}

func ValidateNewPassword(password string) error {
	return validatePasswordShape(password, true)
}

func DummyPasswordHash() string {
	return dummyPasswordHash
}

func validatePasswordShape(password string, requireMinimum bool) error {
	if password == "" || len(password) > PasswordMaximumLength {
		return errors.New("密码格式无效")
	}
	if requireMinimum && len(password) < PasswordMinimumLength {
		return fmt.Errorf("新密码至少需要 %d 位", PasswordMinimumLength)
	}
	if requireMinimum && subtle.ConstantTimeCompare([]byte(password), []byte(legacyDefaultPassword)) == 1 {
		return errors.New("不能使用已停用的历史默认密码")
	}
	return nil
}

func hashPasswordWithSalt(password string, salt []byte) (string, error) {
	if len(salt) != 16 {
		return "", errors.New("portal password salt must contain 16 bytes")
	}
	digest, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLength)
	if err != nil {
		return "", fmt.Errorf("hash portal password: %w", err)
	}
	return fmt.Sprintf(
		"scrypt$%d$%d$%d$%s$%s",
		scryptN, scryptR, scryptP, hex.EncodeToString(salt), hex.EncodeToString(digest),
	), nil
}

func mustHashPasswordWithSalt(password string, salt []byte) string {
	encoded, err := hashPasswordWithSalt(password, salt)
	if err != nil {
		panic(err)
	}
	return encoded
}
