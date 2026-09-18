package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTripAndBounds(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "correct horse") {
		t.Fatal("encoded hash contains password text")
	}
	if err := ValidatePasswordHash(encoded); err != nil {
		t.Fatalf("generated password hash was rejected: %v", err)
	}
	if !CheckPassword(encoded, "correct horse battery staple") {
		t.Fatal("correct password did not verify")
	}
	if CheckPassword(encoded, "incorrect horse battery staple") {
		t.Fatal("incorrect password verified")
	}
	outOfBounds := "$argon2id$v=19$m=999999999,t=1,p=1$YWJjZGVmZ2hpamtsbW5vcA$YWJjZGVmZ2hpamtsbW5vcA"
	if CheckPassword(outOfBounds, "anything") {
		t.Fatal("out-of-bounds hash parameters were accepted")
	}
	if err := ValidatePasswordHash(outOfBounds); err == nil {
		t.Fatal("out-of-bounds stored hash was accepted")
	}
	if err := ValidatePasswordHash("not-a-password-hash"); err == nil {
		t.Fatal("malformed stored hash was accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("short password was accepted")
	}
	if err := ValidatePassword(strings.Repeat("x", 1025)); err == nil {
		t.Fatal("oversized password was accepted")
	}
	if err := ValidatePassword("twelve-chars!"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}
