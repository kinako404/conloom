package panel

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
)

var hashFormat = regexp.MustCompile(`^pbkdf2-sha256:600000:[A-Za-z0-9_-]{22}:[A-Za-z0-9_-]{43}$`)

func TestHashPassword_RoundTripAndFormat(t *testing.T) {
	t.Parallel() // PBKDF2 at DefaultIterations is slow under -race
	h, err := HashPassword("correct horse", DefaultIterations)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !hashFormat.MatchString(h) {
		t.Fatalf("hash %q does not match %s", h, hashFormat)
	}
	if ok, err := VerifyPassword(h, "correct horse"); err != nil || !ok {
		t.Errorf("VerifyPassword(right) = %v, %v; want true", ok, err)
	}
	if ok, err := VerifyPassword(h, "correct horse!"); err != nil || ok {
		t.Errorf("VerifyPassword(wrong) = %v, %v; want false", ok, err)
	}
	again, err := HashPassword("correct horse", DefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	if again == h {
		t.Error("two hashes of the same password are equal; want a fresh salt each time")
	}
	if n, err := ParseHash(h); err != nil || n != DefaultIterations {
		t.Errorf("ParseHash = %d, %v; want %d", n, err, DefaultIterations)
	}
	if _, err := HashPassword("x", MinIterations-1); err == nil {
		t.Error("HashPassword below MinIterations succeeded")
	}
}

func TestParseHash_Rejects(t *testing.T) {
	salt := strings.Repeat("A", 22)
	key := strings.Repeat("B", 42) + "A"
	cases := map[string]string{
		"too few iterations": "pbkdf2-sha256:99999:" + salt + ":" + key,
		"bad iterations":     "pbkdf2-sha256:many:" + salt + ":" + key,
		"too few parts":      "pbkdf2-sha256:600000:" + salt,
		"too many parts":     "pbkdf2-sha256:600000:" + salt + ":" + key + ":x",
		"wrong algorithm":    "pbkdf2-sha1:600000:" + salt + ":" + key,
		"salt not base64url": "pbkdf2-sha256:600000:" + strings.Repeat("+", 22) + ":" + key,
		"key not base64url":  "pbkdf2-sha256:600000:" + salt + ":" + strings.Repeat("/", 43),
		"padded salt":        "pbkdf2-sha256:600000:" + salt + "==:" + key,
		"short salt":         "pbkdf2-sha256:600000:AAAA:" + key,
		"short key":          "pbkdf2-sha256:600000:" + salt + ":AAAA",
		"empty":              "",
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHash(h); err == nil {
				t.Fatalf("ParseHash(%q) succeeded", h)
			} else if h != "" && strings.Contains(err.Error(), h) {
				t.Errorf("error %q quotes the hash", err)
			}
			if _, err := VerifyPassword(h, "x"); err == nil {
				t.Errorf("VerifyPassword(%q) succeeded", h)
			}
		})
	}
	if _, err := ParseHash("pbkdf2-sha256:100000:" + salt + ":" + key); err != nil {
		t.Errorf("ParseHash at MinIterations: %v", err)
	}
}

func TestCredFingerprint(t *testing.T) {
	a, b := CredFingerprint("pbkdf2-sha256:1:a:b"), CredFingerprint("pbkdf2-sha256:1:a:c")
	if len(a) != 16 || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(a) {
		t.Errorf("fingerprint %q is not 16 hex chars", a)
	}
	if a == b {
		t.Error("different hashes have the same fingerprint")
	}
	if a != CredFingerprint("pbkdf2-sha256:1:a:b") {
		t.Error("fingerprint is not deterministic")
	}
}

// The config package checks the hash format without importing this package;
// both must agree.
func TestConfigValidationAgreesWithParseHash(t *testing.T) {
	good := testHash(t)
	salt := strings.Repeat("A", 22)
	key := strings.Repeat("B", 42) + "A"
	for _, h := range []string{
		good,
		"pbkdf2-sha256:100000:" + salt + ":" + key,
		"pbkdf2-sha256:99999:" + salt + ":" + key,
		"pbkdf2-sha256:600000:" + salt,
		"pbkdf2-sha256:600000:" + strings.Repeat("+", 22) + ":" + key,
		"pbkdf2-sha256:600000:" + salt + "==:" + key,
		"pbkdf2-sha256:600000:AAAA:" + key,
		"pbkdf2-sha256:600000:" + salt + ":AAAA",
		"pbkdf2-sha256:+600000:" + salt + ":" + key,
		"pbkdf2-sha256:0600000:" + salt + ":" + key,
		"sha256:600000:" + salt + ":" + key,
	} {
		_, parseErr := ParseHash(h)
		cfg := &config.Config{Panel: &config.PanelConfig{
			Users:       []config.PanelUser{{Name: "admin", PasswordHash: h}},
			SessionTTL:  dur(config.DefaultPanelSessionTTL),
			IdleTimeout: dur(config.DefaultPanelIdleTimeout),
		}}
		validErr := cfg.ValidateServe()
		hashRejected := validErr != nil && strings.Contains(validErr.Error(), "panel.users[0].password_hash")
		if (parseErr != nil) != hashRejected {
			t.Errorf("hash %q: ParseHash error %v, but config validation %v", h, parseErr, validErr)
		}
	}
}
