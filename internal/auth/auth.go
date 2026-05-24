// Package auth gates aws-tui launch behind a TOTP code. After a
// successful prompt the unlock persists for cfg.TTL (default 4h) via a
// small marker file under ~/.aws-tui/.
//
// The threat model is "passive bystander walks up to my unattended
// terminal" - this is friction, not crypto. Anyone with shell access to
// ~/.aws-tui/ can read totp.secret or rm the marker, and they could also
// just run `aws` directly. The gate exists so destructive aws-tui
// actions aren't a one-keystroke walk-up.
package auth

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/pquerna/otp/totp"
)

// Config is supplied by the caller (typically cmd/aws-tui).
type Config struct {
	Issuer      string
	AccountName string
	TTL         time.Duration
}

// DefaultTTL is the 4-hour session window agreed in spec 24.
const DefaultTTL = 4 * time.Hour

// Authenticate runs the launch gate. Writes prompts to stdout, reads
// from stdin. Returns nil on success.
func Authenticate(cfg Config) error {
	if cfg.TTL == 0 {
		cfg.TTL = DefaultTTL
	}
	dir, err := tuiDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if !secretExists(dir) {
		if err := firstRunSetup(dir, cfg); err != nil {
			return err
		}
		// The setup verified a code; treat as fresh unlock.
		return writeMarker(dir, time.Now().Add(cfg.TTL))
	}

	if fresh, err := markerFresh(dir); err == nil && fresh {
		return nil
	}
	return promptUnlock(dir, cfg)
}

// Lock removes the unlock marker so the next launch re-prompts.
// Idempotent.
func Lock() error {
	dir, err := tuiDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "unlock.marker")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ResetTOTP wipes the TOTP secret, backup codes, and marker so the next
// launch runs first-run setup. Confirms via stdin first.
func ResetTOTP() error {
	dir, err := tuiDir()
	if err != nil {
		return err
	}
	fmt.Print("Reset TOTP secret? This will require re-enrolling in your authenticator app. Type 'yes' to confirm: ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		fmt.Println("aborted.")
		return nil
	}
	for _, name := range []string{"totp.secret", "backup.codes", "unlock.marker"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	fmt.Println("TOTP reset. Next launch will run setup.")
	return nil
}

// --- internals -----------------------------------------------------------

func tuiDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("empty home directory")
	}
	return filepath.Join(home, ".aws-tui"), nil
}

func secretExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "totp.secret"))
	return err == nil
}

func loadSecret(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "totp.secret"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func saveSecret(dir, secret string) error {
	return os.WriteFile(filepath.Join(dir, "totp.secret"), []byte(secret), 0o600)
}

// markerFresh returns true when an unexpired unlock.marker exists.
func markerFresh(dir string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "unlock.marker"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return false, nil
	}
	return time.Now().Before(t), nil
}

func writeMarker(dir string, expiry time.Time) error {
	return os.WriteFile(
		filepath.Join(dir, "unlock.marker"),
		[]byte(expiry.UTC().Format(time.RFC3339)),
		0o600,
	)
}

// --- setup ---------------------------------------------------------------

func firstRunSetup(dir string, cfg Config) error {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      cfg.Issuer,
		AccountName: cfg.AccountName,
	})
	if err != nil {
		return fmt.Errorf("totp.Generate: %w", err)
	}

	fmt.Println()
	fmt.Println("aws-tui: first-time setup")
	fmt.Println()
	fmt.Println("Scan this QR code with Google Authenticator / 1Password / Authy:")
	fmt.Println()
	qrterminal.GenerateHalfBlock(key.URL(), qrterminal.L, os.Stdout)
	fmt.Println()
	fmt.Println("Or enter this secret manually:")
	fmt.Println("  " + key.Secret())
	fmt.Println()
	fmt.Printf("Account: %s    Issuer: %s\n", cfg.AccountName, cfg.Issuer)
	fmt.Println()

	r := bufio.NewReader(os.Stdin)
	for tries := 0; tries < 3; tries++ {
		fmt.Print("Enter the current 6-digit code to confirm: ")
		code, _ := r.ReadString('\n')
		code = strings.TrimSpace(code)
		if totp.Validate(code, key.Secret()) {
			if err := saveSecret(dir, key.Secret()); err != nil {
				return err
			}
			codes, err := generateAndSaveBackupCodes(dir)
			if err != nil {
				return err
			}
			printBackupCodes(codes)
			fmt.Print("Press enter to continue: ")
			_, _ = r.ReadString('\n')
			return nil
		}
		fmt.Println("code did not match; try again.")
	}
	return errors.New("setup aborted after 3 invalid codes")
}

// --- unlock prompt -------------------------------------------------------

func promptUnlock(dir string, cfg Config) error {
	secret, err := loadSecret(dir)
	if err != nil {
		return fmt.Errorf("read totp.secret: %w", err)
	}
	r := bufio.NewReader(os.Stdin)
	backoff := 0 * time.Second
	for {
		if backoff > 0 {
			fmt.Printf("(wait %s before next attempt)\n", backoff)
			time.Sleep(backoff)
		}
		fmt.Print("aws-tui is locked. Enter TOTP code (or backup code): ")
		input, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("auth cancelled")
			}
			return err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if isBackupCodeFormat(input) {
			ok, err := consumeBackupCode(dir, input)
			if err != nil {
				return err
			}
			if ok {
				return writeMarker(dir, time.Now().Add(cfg.TTL))
			}
		} else if totp.Validate(input, secret) {
			return writeMarker(dir, time.Now().Add(cfg.TTL))
		}
		fmt.Println("invalid code.")
		backoff = nextBackoff(backoff)
	}
}

// nextBackoff doubles the previous wait, starting at 2s, capped at 30s.
// Returns 2s when previous was zero.
func nextBackoff(prev time.Duration) time.Duration {
	if prev == 0 {
		return 2 * time.Second
	}
	next := prev * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}

// --- backup codes --------------------------------------------------------

// alphabet for backup codes: A-Z + 2-9, dropping 0/1/I/O/L to avoid ambiguity.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func generateAndSaveBackupCodes(dir string) ([]string, error) {
	const count = 10
	codes := make([]string, count)
	hashes := make([]string, count)
	for i := 0; i < count; i++ {
		c, err := randomCode()
		if err != nil {
			return nil, err
		}
		codes[i] = c
		hashes[i] = "sha256:" + hashCode(c)
	}
	payload, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "backup.codes"), payload, 0o600); err != nil {
		return nil, err
	}
	return codes, nil
}

func randomCode() (string, error) {
	// 10 chars total: 5-5 with a dash.
	buf := make([]byte, 10)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			return "", err
		}
		buf[i] = codeAlphabet[n.Int64()]
	}
	return string(buf[:5]) + "-" + string(buf[5:]), nil
}

func hashCode(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

func isBackupCodeFormat(s string) bool {
	s = strings.ToUpper(s)
	if len(s) != 11 || s[5] != '-' {
		return false
	}
	return true
}

// consumeBackupCode validates s against the saved hashes; on a match,
// removes that hash from the list and rewrites the file.
func consumeBackupCode(dir, s string) (bool, error) {
	path := filepath.Join(dir, "backup.codes")
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var hashes []string
	if err := json.Unmarshal(b, &hashes); err != nil {
		return false, err
	}
	target := "sha256:" + hashCode(s)
	for i, h := range hashes {
		if h == target {
			hashes = append(hashes[:i], hashes[i+1:]...)
			payload, err := json.Marshal(hashes)
			if err != nil {
				return true, err
			}
			return true, os.WriteFile(path, payload, 0o600)
		}
	}
	return false, nil
}

func printBackupCodes(codes []string) {
	fmt.Println()
	fmt.Println("Save these one-time backup codes somewhere safe (you won't see")
	fmt.Println("them again). Each works once if you lose access to your app:")
	fmt.Println()
	for _, c := range codes {
		fmt.Println("  " + c)
	}
	fmt.Println()
}
