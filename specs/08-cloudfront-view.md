# 08 — CloudFront View

List distributions, create invalidations, monitor invalidation status. CloudFront is global so the region doesn't matter for the API calls, but we'll use the configured region anyway (SDK accepts it).

## Layout — distribution list

```
┌─ CloudFront Distributions ────────────────────────────────────────┐
│   ID              Domain                       Origin            Status  │
│ > E1ABC23DEF4GHI  d123abc.cloudfront.net      s3-app-prod       Deployed │
│   E2XYZ45ABC6DEF  d456xyz.cloudfront.net      api-prod-elb      Deployed │
│   E3MMM77NNN8OOO  d789mmm.cloudfront.net      static-assets     Deployed │
│                                                                          │
│ enter: open · i: invalidate · v: view invalidations · r: refresh         │
└──────────────────────────────────────────────────────────────────────────┘
```

## Layout — invalidation modal

```
┌─ Create Invalidation: E1ABC23DEF4GHI ─────────────────────────────┐
│                                                                   │
│  Enter paths (one per line). Use /* for full cache flush.         │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────┐      │
│  │ /*                                                       │      │
│  │                                                          │      │
│  │                                                          │      │
│  └─────────────────────────────────────────────────────────┘      │
│                                                                   │
│  Caller reference: aws-tui-1716345678                             │
│                                                                   │
│  enter: submit · esc: cancel                                      │
└───────────────────────────────────────────────────────────────────┘
```

## Layout — invalidations list (per distribution)

```
┌─ Invalidations: E1ABC23DEF4GHI ───────────────────────────────────┐
│   ID                Status      Created              Paths        │
│ > I1ABC23DEF        InProgress  2026-05-22 10:14:32  /* (1 path)  │
│   I2XYZ45GHI        Completed   2026-05-22 09:02:11  /css/* /js/* │
│   I3MMM77NNN        Completed   2026-05-21 16:45:00  /index.html  │
│                                                                   │
│ r: refresh (polls every 5s while InProgress) · esc: back          │
└───────────────────────────────────────────────────────────────────┘
```

## Implementation outline (internal/views/cloudfront/cloudfront.go)

```go
package cloudfront

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cf "github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/YOUR_USERNAME/aws-tui/internal/aws"
)

type Distribution struct {
	ID         string
	DomainName string
	Origin     string
	Status     string
	Enabled    bool
}

type distributionsLoadedMsg struct{ items []Distribution }
type invalidationCreatedMsg struct{ id string }
type errMsg struct{ err error }

type Model struct {
	ctx        *awspkg.Context
	table      table.Model
	distros    []Distribution
	loading    bool
	err        error
	mode       mode

	// Invalidation modal state
	pathsInput textarea.Model
	targetDist string
}

type mode int

const (
	modeList mode = iota
	modeInvalidate
	modeInvalidations
)

func New(ctx *awspkg.Context) Model {
	cols := []table.Column{
		{Title: "ID", Width: 16},
		{Title: "Domain", Width: 32},
		{Title: "Origin", Width: 24},
		{Title: "Status", Width: 12},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true))

	ta := textarea.New()
	ta.Placeholder = "/*"
	ta.SetHeight(6)

	return Model{ctx: ctx, table: t, pathsInput: ta, loading: true}
}

func (m Model) Init() tea.Cmd {
	return m.loadCmd()
}

func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		if err := m.ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := m.ctx.CloudFront()
		var out []Distribution
		paginator := cf.NewListDistributionsPaginator(client, &cf.ListDistributionsInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			for _, d := range page.DistributionList.Items {
				out = append(out, Distribution{
					ID:         aws.ToString(d.Id),
					DomainName: aws.ToString(d.DomainName),
					Origin:     firstOrigin(d.Origins),
					Status:     aws.ToString(d.Status),
					Enabled:    aws.ToBool(d.Enabled),
				})
			}
		}
		return distributionsLoadedMsg{items: out}
	}
}

func firstOrigin(origins *cftypes.Origins) string {
	if origins == nil || len(origins.Items) == 0 {
		return ""
	}
	return aws.ToString(origins.Items[0].DomainName)
}

func (m Model) createInvalidation(distID string, paths []string) tea.Cmd {
	return func() tea.Msg {
		client := m.ctx.CloudFront()
		ref := fmt.Sprintf("aws-tui-%d", time.Now().Unix())
		items := paths
		input := &cf.CreateInvalidationInput{
			DistributionId: aws.String(distID),
			InvalidationBatch: &cftypes.InvalidationBatch{
				CallerReference: aws.String(ref),
				Paths: &cftypes.Paths{
					Quantity: aws.Int32(int32(len(items))),
					Items:    items,
				},
			},
		}
		out, err := client.CreateInvalidation(context.Background(), input)
		if err != nil {
			return errMsg{err: err}
		}
		return invalidationCreatedMsg{id: aws.ToString(out.Invalidation.Id)}
	}
}

// Handlers for parsing paths
func parsePaths(input string) []string {
	lines := strings.Split(input, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
```

## Confirmation step before invalidation

Before calling `CreateInvalidation`, show a confirmation modal:

```
┌─ Confirm Invalidation ─────────────────────────┐
│                                                │
│  Distribution: E1ABC23DEF4GHI                  │
│  Domain: d123abc.cloudfront.net                │
│  Paths (1):                                    │
│    /*                                          │
│                                                │
│  This will invalidate ALL cached content.      │
│                                                │
│  [y] confirm   [n] cancel                      │
└────────────────────────────────────────────────┘
```

Especially important when the profile is colour-coded red (prod). For `/*` specifically, require a second confirmation typed as `INVALIDATE` to avoid accidental full flushes.

## Polling invalidation status

When viewing the invalidations list, if any row has status `InProgress`, set up a ticker:

```go
func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}
```

On tick, re-issue `ListInvalidations` and refresh. Stop ticking when no InProgress rows remain. 5 second poll interval is appropriate.

## Acceptance criteria

- Lists all distributions for the profile.
- `i` on a distribution opens the invalidation modal.
- Paths are entered as newline-separated values.
- Confirmation modal shown before submit; `/*` requires typing `INVALIDATE`.
- After submission, show toast/status with the invalidation ID, switch to invalidations view.
- `v` from distribution list shows invalidation history for that distribution.
- Auto-poll every 5s while any invalidation is InProgress.
- `r` refreshes the list.
