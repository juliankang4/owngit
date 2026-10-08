package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"owngit/internal/hostmem"
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

func TestPasswordWorkSharesHostMemoryAdmission(t *testing.T) {
	saved := hostmem.Ceiling
	t.Cleanup(func() { hostmem.Ceiling = saved })
	for _, test := range []struct {
		name          string
		ceiling       uint64
		maximum, want int
	}{
		{"unknown", 0, 0, 4},
		{"smallest host", 64 << 20, 0, 1},
		{"256 MiB", 256 << 20, 0, 1},
		{"512 MiB", 512 << 20, 0, 2},
		{"768 MiB", 768 << 20, 0, 3},
		{"1 GiB", 1 << 30, 0, 4},
		{"large host", 8 << 30, 0, 4},
		{"lower explicit limit", 1 << 30, 1, 1},
		{"explicit limit cannot exceed budget", 512 << 20, 8, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			hostmem.Ceiling = func() uint64 { return test.ceiling }
			manager := &Manager{MaximumConcurrentChecks: test.maximum}
			if err := manager.acquireCheck(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := cap(manager.checkSlots); got != test.want {
				t.Fatalf("password slots: %d, want %d", got, test.want)
			}
		})
	}
	hostmem.Ceiling = saved
	const password = "synthetic-password"
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, candidate string
		create, matches bool
	}{{"creation", password, true, true}, {"equal", password, false, true}, {"different", "wrong-password", false, false}} {
		t.Run(test.name, func(t *testing.T) {
			checkPasswordWorkWaits(t, func(manager *Manager, ctx context.Context) error {
				if test.create {
					hash, err := manager.HashPassword(ctx, password)
					if err == nil && (!strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=1,p=4$") || !CheckPassword(hash, password)) {
						t.Error("created password does not verify with the required parameters")
					}
					return err
				}
				matches, err := manager.CheckPassword(ctx, encoded, test.candidate)
				if err == nil && matches != test.matches {
					t.Errorf("password equality: %v, want %v", matches, test.matches)
				}
				return err
			})
		})
	}
}

func checkPasswordWorkWaits(t *testing.T, work func(*Manager, context.Context) error) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		manager := &Manager{MaximumConcurrentChecks: 1}
		if err := manager.acquireCheck(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, cancelled := range []bool{true, false} {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- work(manager, ctx) }()
			synctest.Wait()
			select {
			case err := <-done:
				t.Fatalf("password work bypassed a full gate: %v", err)
			default:
			}
			occupied := 1
			var wantErr error
			if cancelled {
				cancel()
				wantErr = context.Canceled
			} else {
				<-manager.checkSlots
				occupied--
			}
			synctest.Wait()
			if err := <-done; !errors.Is(err, wantErr) {
				t.Fatalf("waiting password work: %v, want %v", err, wantErr)
			}
			if len(manager.checkSlots) != occupied {
				t.Fatal("password work did not leave the expected occupied slots")
			}
		}
	})
}

func TestPasswordPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	for _, test := range []struct {
		password string
		want     error
	}{{"short", ErrPasswordTooShort}, {strings.Repeat("x", 1025), ErrPasswordTooLong}} {
		if err := ValidatePassword(test.password); err != test.want {
			t.Fatalf("password policy: %v, want %v", err, test.want)
		}
		if _, err := new(Manager).HashPassword(ctx, test.password); err != test.want {
			t.Fatalf("password policy before admission: %v, want %v", err, test.want)
		}
	}
	if err := ValidatePassword("twelve-chars!"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}

// Both limits count characters (code points), not bytes.
func TestPasswordLimitsCountCharacters(t *testing.T) {
	for _, test := range []struct {
		name     string
		password string
		want     error
	}{
		{"three Hangul characters are nine bytes", "비밀번", ErrPasswordTooShort},
		{"seven emoji are 28 bytes", strings.Repeat("\U0001F512", 7), ErrPasswordTooShort},
		{"eight Hangul characters", "비밀번호여덟글자", nil},
		{"the longest password in four-byte characters", strings.Repeat("\U0001F512", MaximumPasswordCharacters), nil},
		{"one character too many", strings.Repeat("가", MaximumPasswordCharacters+1), ErrPasswordTooLong},
	} {
		if err := ValidatePassword(test.password); err != test.want {
			t.Errorf("%s: err=%v, want %v", test.name, err, test.want)
		}
	}
	// The longest accepted password also verifies, so no accepted password
	// can lock its owner out.
	longest := strings.Repeat("\U0001F512", MaximumPasswordCharacters)
	encoded, err := HashPassword(longest)
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(encoded, longest) {
		t.Fatal("the longest accepted password does not verify")
	}
}
