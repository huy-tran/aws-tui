# 13 — Build and Distribute

## Local development build

```bash
go build -o bin/aws-tui ./cmd/aws-tui
./bin/aws-tui
```

For rapid iteration, add a `Makefile` or `Taskfile`:

```makefile
.PHONY: run build test clean

run:
	go run ./cmd/aws-tui

build:
	go build -ldflags="-s -w" -o bin/aws-tui ./cmd/aws-tui

test:
	go test ./...

clean:
	rm -rf bin/
```

`-s -w` strips debug symbols, cutting binary size ~30%.

## Cross-platform builds

The user is on Windows 11 primarily but the tool should also work on Linux/macOS for SSH sessions and CI.

```bash
# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/aws-tui-windows-amd64.exe ./cmd/aws-tui

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/aws-tui-darwin-amd64 ./cmd/aws-tui

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/aws-tui-darwin-arm64 ./cmd/aws-tui

# Linux
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/aws-tui-linux-amd64 ./cmd/aws-tui
```

## Version info

Embed version via ldflags so `aws-tui --version` works:

```go
// cmd/aws-tui/main.go
var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)

func main() {
    if len(os.Args) > 1 && os.Args[1] == "--version" {
        fmt.Printf("aws-tui %s (%s, built %s)\n", version, commit, date)
        return
    }
    // ...
}
```

Build with:

```bash
go build -ldflags="-X main.version=$(git describe --tags) -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/aws-tui
```

## Goreleaser (recommended)

For tagged releases, use [goreleaser](https://goreleaser.com/):

```yaml
# .goreleaser.yaml
version: 2

builds:
  - id: aws-tui
    main: ./cmd/aws-tui
    binary: aws-tui
    env:
      - CGO_ENABLED=0
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.ShortCommit}}
      - -X main.date={{.Date}}

archives:
  - format: tar.gz
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    format_overrides:
      - goos: windows
        format: zip

checksum:
  name_template: 'checksums.txt'

changelog:
  sort: asc
  filters:
    exclude:
      - '^docs:'
      - '^test:'
      - '^chore:'
```

Run:

```bash
git tag v0.1.0
git push --tags
goreleaser release --clean
```

Creates GitHub release with binaries for all platforms.

## Install on Windows

Since the user's primary machine is Windows 11, ensure these work:

```powershell
# Manual: download from GitHub releases, extract to a folder on PATH
# e.g. C:\Users\Alex\bin\

# Or: scoop (if they use it)
# scoop install aws-tui   (would require a manifest)

# Or: go install directly (requires Go installed)
go install github.com/YOUR_USERNAME/aws-tui/cmd/aws-tui@latest
```

The `go install` path puts the binary in `$(go env GOPATH)/bin`. On Windows that's typically `%USERPROFILE%\go\bin`. Ensure that's on PATH.

## Terminal requirements

Bubble Tea uses ANSI escape sequences. On Windows 11 these work fine in:

- Windows Terminal (recommended)
- PowerShell 7+
- WSL (any distro)

Avoid:
- Legacy `cmd.exe` (works but limited colours)
- Git Bash terminal (mintty has some quirks with alt-screen mode)

Document the requirement in the README: "Windows Terminal or PowerShell 7+ recommended."

## Required external tools

The TUI shells out to:

- `aws` CLI (v2) — for SSM sessions and `aws logs tail`
- `session-manager-plugin` — required by `aws ssm start-session`

Check for these on first launch and warn if missing:

```go
func checkPrerequisites() []string {
    var missing []string
    if _, err := exec.LookPath("aws"); err != nil {
        missing = append(missing, "aws CLI")
    }
    if _, err := exec.LookPath("session-manager-plugin"); err != nil {
        // On Windows, plugin lives at a fixed path, not on PATH
        if !sessionPluginInstalledWindows() {
            missing = append(missing, "session-manager-plugin")
        }
    }
    return missing
}
```

Show a non-fatal warning banner: "session-manager-plugin not found, SSM sessions will fail. Install: https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html"

## CI (GitHub Actions)

```yaml
# .github/workflows/ci.yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go test ./...
      - run: go vet ./...
      - run: go build ./...

  release:
    if: startsWith(github.ref, 'refs/tags/')
    needs: test
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

## README essentials

The project README should cover:

- What it is and the screenshot/gif
- Installation (per platform)
- Prerequisites (aws CLI, session-manager-plugin)
- Quick start (`aws-tui` and the first-run flow)
- Keybindings reference
- Config file location
- Troubleshooting (SSO refresh, terminal compatibility)
- Contributing

A short asciinema cast or GIF (recorded with `vhs`) is worth the effort for the README. Charm makes [`vhs`](https://github.com/charmbracelet/vhs) specifically for this.

## Acceptance criteria

- `go build` produces a working binary on Windows, macOS, Linux.
- `aws-tui --version` prints version/commit/date.
- `goreleaser release` produces archived binaries for all platforms.
- First launch detects missing `aws` CLI and `session-manager-plugin`, shows a non-fatal warning.
- README covers install on Windows specifically since that's the primary target.
