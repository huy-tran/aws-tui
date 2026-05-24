package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/huy-tran/aws-tui/internal/app"
	"github.com/huy-tran/aws-tui/internal/audit"
	"github.com/huy-tran/aws-tui/internal/auth"
	"github.com/huy-tran/aws-tui/internal/events"
	"github.com/huy-tran/aws-tui/internal/ui/theme"
)

// Populated at build time via -ldflags="-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	dryRun := false
	ttl := auth.DefaultTTL
	themeName := os.Getenv("AWS_TUI_THEME")
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--version" || a == "-v":
			fmt.Printf("aws-tui %s (%s, built %s)\n", version, commit, date)
			return
		case a == "--help" || a == "-h":
			printHelp()
			return
		case a == "--dry-run":
			dryRun = true
		case strings.HasPrefix(a, "--theme="):
			themeName = strings.TrimPrefix(a, "--theme=")
		case a == "--lock":
			if err := auth.Lock(); err != nil {
				fmt.Fprintf(os.Stderr, "lock failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("locked.")
			return
		case a == "--reset-totp":
			if err := auth.ResetTOTP(); err != nil {
				fmt.Fprintf(os.Stderr, "reset failed: %v\n", err)
				os.Exit(1)
			}
			return
		case strings.HasPrefix(a, "--totp-ttl="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--totp-ttl="))
			if err != nil {
				fmt.Fprintf(os.Stderr, "bad --totp-ttl: %v\n", err)
				os.Exit(2)
			}
			ttl = d
		}
	}
	if v := os.Getenv("AWS_TUI_TOTP_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		}
	}
	if os.Getenv("AWS_TUI_DRY_RUN") != "" {
		dryRun = true
	}
	audit.SetMode(audit.Mode{DryRun: dryRun})
	if err := theme.SetByName(themeName); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	// Expose the build-time version to the app layer for the title bar.
	app.Version = version

	// Launch gate. Failing this halts before any AWS plumbing wakes up.
	if err := auth.Authenticate(auth.Config{
		Issuer:      "aws-tui",
		AccountName: tuiAccountName(),
		TTL:         ttl,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
	}

	if missing := checkPrerequisites(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "warning: missing required tools: %s\n", strings.Join(missing, ", "))
		fmt.Fprintln(os.Stderr, "  SSM sessions and `aws logs tail` will fail until these are installed.")
		fmt.Fprintln(os.Stderr, "  See: https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html")
		fmt.Fprintln(os.Stderr, "")
	}

	model, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init error: %v\n", err)
		os.Exit(1)
	}

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	events.SetProgram(p)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "run error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println("aws-tui - terminal UI for browsing AWS resources")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  aws-tui              launch the TUI (prompts for TOTP if not unlocked)")
	fmt.Println("  aws-tui --dry-run    intercept every destructive call; log intent to")
	fmt.Println("                       ~/.aws-tui/audit.log without hitting AWS")
	fmt.Println("                       (or set AWS_TUI_DRY_RUN=1)")
	fmt.Println("  aws-tui --lock       wipe the unlock marker; next launch prompts")
	fmt.Println("  aws-tui --reset-totp wipe TOTP secret + backup codes; next launch re-enrolls")
	fmt.Println("  aws-tui --totp-ttl=Nh override the 4h unlock window for this launch")
	fmt.Println("                       (or set AWS_TUI_TOTP_TTL=Nh; 0 = always prompt)")
	fmt.Println("  aws-tui --theme=X    pick a colour theme: dark / light / auto")
	fmt.Println("                       (or set AWS_TUI_THEME; default auto via terminal probe)")
	fmt.Println("  aws-tui --version    print version info")
	fmt.Println("  aws-tui --help       show this message")
	fmt.Println("")
	fmt.Println("Global keys (inside the TUI):")
	fmt.Println("  ctrl+c   quit")
	fmt.Println("  ctrl+p   jump to profile picker")
	fmt.Println("  ctrl+r   jump to region picker")
	fmt.Println("  esc      go back")
}

func checkPrerequisites() []string {
	var missing []string
	if _, err := exec.LookPath("aws"); err != nil {
		missing = append(missing, "aws CLI v2")
	}
	if _, err := exec.LookPath("session-manager-plugin"); err != nil {
		if !sessionPluginInstalledWindows() {
			missing = append(missing, "session-manager-plugin")
		}
	}
	return missing
}

// tuiAccountName returns "aws-tui:<system username>" for the TOTP enrollment
// label. Falls back to a bare "aws-tui" if the username can't be resolved.
func tuiAccountName() string {
	if u := os.Getenv("USER"); u != "" {
		return "aws-tui:" + u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return "aws-tui:" + u
	}
	return "aws-tui"
}

// sessionPluginInstalledWindows checks the default install path used by the
// AWS-published MSI on Windows, since the plugin is not added to PATH by
// the installer.
func sessionPluginInstalledWindows() bool {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Amazon", "SessionManagerPlugin", "bin", "session-manager-plugin.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Amazon", "SessionManagerPlugin", "bin", "session-manager-plugin.exe"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
