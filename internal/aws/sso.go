package aws

import (
	"fmt"
	"os/exec"
	"strings"
)

type SSOExpiredError struct {
	Profile    string
	Underlying error
}

func (e *SSOExpiredError) Error() string {
	return fmt.Sprintf("SSO session expired for profile %q: %v", e.Profile, e.Underlying)
}

// isSSOExpired heuristically detects SSO expiry from SDK errors. The SDK
// does not expose a typed error for this, so we string-match.
func isSSOExpired(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token is expired") ||
		strings.Contains(msg, "sso session has expired") ||
		strings.Contains(msg, "ssooidc") && strings.Contains(msg, "expired")
}

// RefreshSSO runs `aws sso login --profile X` and waits for it to finish.
// The TUI flow should prefer tea.ExecProcess so stdio is wired correctly;
// this helper exists for non-TUI callers.
func RefreshSSO(profile string) error {
	cmd := exec.Command("aws", "sso", "login", "--profile", profile)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
