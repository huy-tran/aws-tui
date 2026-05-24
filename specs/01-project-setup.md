# 01 — Project Setup

## Module initialisation

```bash
mkdir aws-tui && cd aws-tui
go mod init github.com/YOUR_USERNAME/aws-tui
```

Replace `YOUR_USERNAME` with the actual GitHub username before publishing.

## Dependencies

Add these to `go.mod` via `go get`:

```bash
# TUI
go get github.com/charmbracelet/bubbletea
go get github.com/charmbracelet/bubbles
go get github.com/charmbracelet/lipgloss

# AWS SDK v2 — core
go get github.com/aws/aws-sdk-go-v2
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/credentials

# AWS SDK v2 — services
go get github.com/aws/aws-sdk-go-v2/service/ec2
go get github.com/aws/aws-sdk-go-v2/service/s3
go get github.com/aws/aws-sdk-go-v2/service/cloudfront
go get github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk
go get github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs
go get github.com/aws/aws-sdk-go-v2/service/sts
go get github.com/aws/aws-sdk-go-v2/service/ssm

# Config + utilities
go get gopkg.in/ini.v1
```

Go version: **1.22 or higher** (for `slices` and `maps` stdlib packages).

## Directory layout

```
aws-tui/
├── cmd/
│   └── aws-tui/
│       └── main.go              # Entry point
├── internal/
│   ├── app/
│   │   ├── app.go               # Root Bubble Tea model
│   │   ├── nav.go               # View stack / navigation
│   │   └── keys.go              # Global keybindings
│   ├── aws/
│   │   ├── client.go            # AWS SDK wrapper
│   │   ├── profiles.go          # Profile discovery from ~/.aws/config
│   │   ├── sso.go               # SSO expiry detection + refresh
│   │   └── cache.go             # In-memory TTL cache
│   ├── views/
│   │   ├── profile/             # Profile picker
│   │   ├── region/              # Region picker
│   │   ├── dashboard/           # Main tabbed view
│   │   ├── ec2/
│   │   ├── cloudfront/
│   │   ├── s3/
│   │   ├── beanstalk/
│   │   └── cloudwatch/
│   ├── ui/
│   │   ├── styles.go            # Lipgloss styles
│   │   ├── header.go            # Persistent context header
│   │   └── help.go              # Footer help/keybindings
│   └── state/
│       └── state.go             # Persisted state (last profile, last region)
├── go.mod
├── go.sum
├── README.md
└── LICENSE
```

## Entry point (cmd/aws-tui/main.go)

```go
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/YOUR_USERNAME/aws-tui/internal/app"
)

func main() {
	model, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init error: %v\n", err)
		os.Exit(1)
	}

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "run error: %v\n", err)
		os.Exit(1)
	}
}
```

## Verification

```bash
go build ./cmd/aws-tui
./aws-tui  # Should launch (empty for now, will exit cleanly with Ctrl+C)
```

If the build succeeds and the program runs without panicking, setup is complete.
