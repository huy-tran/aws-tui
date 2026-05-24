# 15 — SecurityHub View

Triage SecurityHub findings via the same Insights → underlying findings flow the console uses. Insights are saved aggregations (managed by AWS or user-defined) that group findings by a chosen field (resource id, severity, product, etc). The TUI exposes them so you can land on something like "AWS resources with the most findings" and drill straight into the rows it summarises, without writing a fresh filter every session.

Read-only in v1. No workflow status updates, no finding suppression, no note posting — those are easier to get wrong in a fast keyboard tool than to do right; the console handles them fine when you actually need them.

## Three-screen flow

```
Insights (top) ──enter──> Insight Results ──enter──> Findings list ──enter──> Finding detail
                              (aggregations)          (filtered to the chosen group)
```

You can also `a` from the Insights list to skip straight to **all** findings in the region (no insight filter applied) — useful when the saved insights don't cover what you're hunting.

## Layout — insights list

```
┌─ SecurityHub Insights (14) ────────────────────────────────────────┐
│ / __________________                                                │
│                                                                     │
│   Name                                              Group By  Source│
│ > AWS resources with the most findings              Resource   AWS  │
│   Resources with the most critical/high findings    Resource   AWS  │
│   AWS principals with suspicious access denied      Principal  AWS  │
│   IAM users involved in suspicious activity         Principal  AWS  │
│   prod-only criticals (custom)                      Severity   You  │
│   ...                                                              │
│                                                                     │
│ enter: open · a: all findings · /: filter · r: refresh             │
└─────────────────────────────────────────────────────────────────────┘
```

`Source` is `AWS` (built-in managed insight) or `You` (insight owned by this account). The distinction matters because user-defined insights tend to encode local convention; AWS ones are generic.

## Layout — insight results (the aggregation)

```
┌─ AWS resources with the most findings ─────────────────────────────┐
│ Grouped by: Resource           Total findings matched: 1247        │
│                                                                     │
│   Group value                                          Count        │
│ > arn:aws:iam::123:role/lambda-default                 423          │
│   arn:aws:ec2:ap-southeast-2:123:instance/i-0abc...    188          │
│   arn:aws:s3:::data-prod-bucket                        142          │
│   arn:aws:ec2:ap-southeast-2:123:security-group/sg...  97           │
│   ...                                                              │
│                                                                     │
│ enter: see findings for this group · esc: back                     │
└─────────────────────────────────────────────────────────────────────┘
```

The `Count` column is the number of active findings (RecordState=ACTIVE) attributed to that group value. Sorted descending. Showing top 50; SecurityHub returns the full set but anything past 50 is rarely actionable in this view.

## Layout — findings list

```
┌─ Findings: Resource = arn:aws:iam::123:role/lambda-default (423) ──┐
│ Sev: [Crit  High  Med  Low  Info]   State: [Active  Suppressed]   │
│                                                                     │
│   Sev   Title                                  Product       Age   │
│ > CRIT  S3.4 S3 buckets should not allow ...   Security Hub  2d    │
│   HIGH  IAM.4 IAM root user access key ...     Security Hub  5d    │
│   HIGH  Possible credential exposure in env... GuardDuty     1d    │
│   MED   Inspector finding: CVE-2026-12345...   Inspector     12h   │
│   ...                                                              │
│                                                                     │
│ enter: details · y a: yank ARN · y t: yank title · /: filter      │
└─────────────────────────────────────────────────────────────────────┘
```

Severity prefix is colour-coded: Critical = red bg, High = orange, Medium = yellow, Low = blue, Informational = grey. `Age` is human-relative ("2d", "12h", "47m") based on the finding's `UpdatedAt`.

The sev/state toggles at the top are local view filters (don't re-query). `1`/`2`/`3`/`4`/`5` toggle severity columns; `s` toggles suppressed visibility.

## Layout — finding detail

```
┌─ S3.4 S3 buckets should not allow public read access ──────────────┐
│ Severity:    CRITICAL                Workflow: NEW                  │
│ Updated:     2026-05-21 14:02:18     State:    ACTIVE               │
│ Product:     Security Hub (FSBP)     Region:   ap-southeast-2       │
│ Account:     123456789012                                           │
│                                                                     │
│ Resources:                                                          │
│   arn:aws:s3:::data-prod-bucket  (AwsS3Bucket)                     │
│                                                                     │
│ Compliance status: FAILED                                           │
│ Standards: AWS Foundational Security Best Practices                 │
│                                                                     │
│ Description:                                                        │
│   This control checks whether your S3 buckets allow public read    │
│   access by evaluating the Block Public Access settings, the       │
│   bucket policy, and the bucket access control list (ACL)...      │
│                                                                     │
│ Remediation:                                                        │
│   To remediate this finding, enable Block Public Access on the     │
│   bucket. See: https://docs.aws.amazon.com/.../s3-4.html           │
│                                                                     │
│ y a: yank ARN · y t: yank title · y l: yank remediation URL · esc │
└─────────────────────────────────────────────────────────────────────┘
```

## Implementation (internal/views/securityhub/securityhub.go)

```go
package securityhub

import (
    "context"
    "fmt"
    "sort"
    "strings"
    "time"

    awssdk "github.com/aws/aws-sdk-go-v2/aws"
    sh "github.com/aws/aws-sdk-go-v2/service/securityhub"
    shtypes "github.com/aws/aws-sdk-go-v2/service/securityhub/types"
    "github.com/charmbracelet/bubbles/textinput"
    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"

    awspkg "github.com/huy-tran/aws-tui/internal/aws"
    "github.com/huy-tran/aws-tui/internal/ui/datatable"
)

const insightsCacheTTL = 10 * time.Minute   // insights rarely change
const findingsCacheTTL = 60 * time.Second   // active findings change with severity sweeps
const maxFindingsFetch = 500                // hard cap per query

type Insight struct {
    ARN     string
    Name    string
    GroupBy string // human-readable: "Resource", "Severity", etc.
    Owner   string // "AWS" or "You"
}

type InsightResult struct {
    GroupValue string
    Count      int64
}

type Finding struct {
    ID          string
    ARN         string
    Title       string
    Description string
    Severity    string // CRITICAL / HIGH / MEDIUM / LOW / INFORMATIONAL
    Workflow    string // NEW / NOTIFIED / RESOLVED / SUPPRESSED
    RecordState string // ACTIVE / ARCHIVED
    ProductName string
    AccountID   string
    Region      string
    UpdatedAt   time.Time
    Resources   []FindingResource
    Compliance  string
    Standards   []string
    Remediation Remediation
}

type FindingResource struct {
    ARN  string
    Type string
}

type Remediation struct {
    Text string
    URL  string
}

type (
    insightsLoadedMsg       struct{ items []Insight }
    insightResultsLoadedMsg struct{ insight Insight; groups []InsightResult }
    findingsLoadedMsg       struct{ scope string; items []Finding }
    errMsg                  struct{ err error }
)

type mode int

const (
    modeInsights mode = iota
    modeInsightResults
    modeFindings
    modeFindingDetail
)

type sevFilter struct {
    critical, high, medium, low, informational bool
}

type stateFilter struct {
    active, suppressed bool
}

type Model struct {
    ctx    *awspkg.Context
    mode   mode
    width  int
    height int

    insights      []Insight
    insightsFilt  []Insight
    insightTable  datatable.Model
    filter        textinput.Model
    filterMode    bool
    loading       bool
    err           error
    status        string

    targetInsight Insight
    results       []InsightResult
    resultTable   datatable.Model

    findingsScope string // human-readable "All findings" or "Resource = ..."
    findings      []Finding
    findingsFilt  []Finding
    findingsTable datatable.Model
    sev           sevFilter
    state         stateFilter

    targetFinding Finding
}
```

### Listing insights

```go
func (m Model) loadInsightsCmd(force bool) tea.Cmd {
    key := "sh:insights:" + m.ctx.Region
    ctx := m.ctx
    return func() tea.Msg {
        if !force {
            if cached, ok := ctx.Cache.Get(key); ok {
                if items, ok := cached.([]Insight); ok {
                    return insightsLoadedMsg{items: items}
                }
            }
        }
        if err := ctx.Load(context.Background()); err != nil {
            return errMsg{err: err}
        }
        client := ctx.SecurityHub()
        var items []Insight
        paginator := sh.NewGetInsightsPaginator(client, &sh.GetInsightsInput{
            MaxResults: awssdk.Int32(100),
        })
        for paginator.HasMorePages() {
            page, err := paginator.NextPage(context.Background())
            if err != nil {
                return errMsg{err: err}
            }
            for _, in := range page.Insights {
                items = append(items, Insight{
                    ARN:     awssdk.ToString(in.InsightArn),
                    Name:    awssdk.ToString(in.Name),
                    GroupBy: humanGroupBy(awssdk.ToString(in.GroupByAttribute)),
                    Owner:   insightOwner(awssdk.ToString(in.InsightArn)),
                })
            }
        }
        sort.SliceStable(items, func(i, j int) bool {
            // user-owned first, then alpha
            if items[i].Owner != items[j].Owner {
                return items[i].Owner == "You"
            }
            return items[i].Name < items[j].Name
        })
        ctx.Cache.Set(key, items, insightsCacheTTL)
        return insightsLoadedMsg{items: items}
    }
}

// insightOwner returns "AWS" for arns under "arn:aws:securityhub:::insight/..."
// (managed by AWS, no account in the ARN) and "You" for everything else.
func insightOwner(arn string) string {
    // Managed insight ARNs look like:
    //   arn:aws:securityhub:::insight/securityhub/<name>/<uuid>
    // Note the empty account field. Account-owned look like:
    //   arn:aws:securityhub:<region>:<account>:insight/<account>/<uuid>
    parts := strings.SplitN(arn, ":", 6)
    if len(parts) >= 5 && parts[4] == "" {
        return "AWS"
    }
    return "You"
}
```

`humanGroupBy` maps the raw `GroupByAttribute` (e.g. `"ResourceId"`, `"Severity.Label"`) to a friendlier string for the table column. Mapping kept inline rather than separate file:

```go
func humanGroupBy(g string) string {
    switch {
    case strings.HasPrefix(g, "ResourceId"):
        return "Resource"
    case strings.HasPrefix(g, "Severity"):
        return "Severity"
    case strings.HasPrefix(g, "AwsAccountId"):
        return "Account"
    case strings.HasPrefix(g, "ProductName"):
        return "Product"
    case strings.HasPrefix(g, "UserName") ||
        strings.Contains(g, "Principal"):
        return "Principal"
    case strings.HasPrefix(g, "Type"):
        return "Type"
    }
    return g
}
```

### Insight results

```go
func (m Model) loadInsightResultsCmd(insight Insight) tea.Cmd {
    ctx := m.ctx
    return func() tea.Msg {
        client := ctx.SecurityHub()
        out, err := client.GetInsightResults(context.Background(), &sh.GetInsightResultsInput{
            InsightArn: awssdk.String(insight.ARN),
        })
        if err != nil {
            return errMsg{err: err}
        }
        results := make([]InsightResult, 0, len(out.InsightResults.ResultValues))
        for _, r := range out.InsightResults.ResultValues {
            results = append(results, InsightResult{
                GroupValue: awssdk.ToString(r.GroupByAttributeValue),
                Count:      r.Count,
            })
        }
        // Already sorted desc by the API, but enforce.
        sort.Slice(results, func(i, j int) bool { return results[i].Count > results[j].Count })
        if len(results) > 50 {
            results = results[:50]
        }
        return insightResultsLoadedMsg{insight: insight, groups: results}
    }
}
```

### Findings with filter

`GetFindings` takes an `AwsSecurityFindingFilters` struct. When drilling in from an insight result, the relevant filter depends on the insight's GroupByAttribute:

```go
func filtersForInsightGroup(insight Insight, groupValue string) shtypes.AwsSecurityFindingFilters {
    eq := []shtypes.StringFilter{{
        Value:      awssdk.String(groupValue),
        Comparison: shtypes.StringFilterComparisonEquals,
    }}
    f := shtypes.AwsSecurityFindingFilters{
        RecordState: []shtypes.StringFilter{{
            Value:      awssdk.String("ACTIVE"),
            Comparison: shtypes.StringFilterComparisonEquals,
        }},
    }
    switch insight.GroupBy {
    case "Resource":
        f.ResourceId = eq
    case "Account":
        f.AwsAccountId = eq
    case "Product":
        f.ProductName = eq
    case "Severity":
        f.SeverityLabel = eq
    case "Principal":
        f.ResourceAwsIamUserUserName = eq
    case "Type":
        f.Type = eq
    }
    return f
}
```

Bare "all findings" entry uses the same shape with no per-group filter, just `RecordState=ACTIVE`.

```go
func (m Model) loadFindingsCmd(filters shtypes.AwsSecurityFindingFilters, scope string) tea.Cmd {
    ctx := m.ctx
    return func() tea.Msg {
        client := ctx.SecurityHub()
        var items []Finding
        paginator := sh.NewGetFindingsPaginator(client, &sh.GetFindingsInput{
            Filters:    &filters,
            MaxResults: awssdk.Int32(100),
        })
        for paginator.HasMorePages() && len(items) < maxFindingsFetch {
            page, err := paginator.NextPage(context.Background())
            if err != nil {
                return errMsg{err: err}
            }
            for _, f := range page.Findings {
                items = append(items, toFinding(f))
            }
        }
        sort.Slice(items, func(i, j int) bool {
            return severityRank(items[i].Severity) > severityRank(items[j].Severity)
        })
        return findingsLoadedMsg{scope: scope, items: items}
    }
}
```

`maxFindingsFetch = 500` keeps the call bounded for accounts with thousands of findings. Show a banner in the findings view if the cap was hit: "showing first 500 — refine via the insight".

### Severity sort

```go
func severityRank(s string) int {
    switch s {
    case "CRITICAL":
        return 5
    case "HIGH":
        return 4
    case "MEDIUM":
        return 3
    case "LOW":
        return 2
    case "INFORMATIONAL":
        return 1
    }
    return 0
}
```

### Field extraction from ASFF

ASFF is verbose. Pull what the UI uses and drop the rest:

```go
func toFinding(f shtypes.AwsSecurityFinding) Finding {
    out := Finding{
        ID:          awssdk.ToString(f.Id),
        ARN:         awssdk.ToString(f.GeneratorId),
        Title:       awssdk.ToString(f.Title),
        Description: awssdk.ToString(f.Description),
        ProductName: awssdk.ToString(f.ProductName),
        AccountID:   awssdk.ToString(f.AwsAccountId),
        Region:      awssdk.ToString(f.Region),
    }
    if f.Severity != nil {
        out.Severity = string(f.Severity.Label)
    }
    if f.Workflow != nil {
        out.Workflow = string(f.Workflow.Status)
    }
    out.RecordState = string(f.RecordState)
    if t, err := time.Parse(time.RFC3339, awssdk.ToString(f.UpdatedAt)); err == nil {
        out.UpdatedAt = t
    }
    for _, r := range f.Resources {
        out.Resources = append(out.Resources, FindingResource{
            ARN:  awssdk.ToString(r.Id),
            Type: awssdk.ToString(r.Type),
        })
    }
    if f.Compliance != nil {
        out.Compliance = string(f.Compliance.Status)
        out.Standards = nil
        for _, sa := range f.Compliance.AssociatedStandards {
            out.Standards = append(out.Standards, awssdk.ToString(sa.StandardsId))
        }
    }
    if f.Remediation != nil && f.Remediation.Recommendation != nil {
        out.Remediation = Remediation{
            Text: awssdk.ToString(f.Remediation.Recommendation.Text),
            URL:  awssdk.ToString(f.Remediation.Recommendation.Url),
        }
    }
    return out
}
```

## AWS context wiring

Add a `SecurityHub()` method to `internal/aws/client.go` returning a cached `*securityhub.Client`. The required IAM is `securityhub:GetInsights`, `securityhub:GetInsightResults`, `securityhub:GetFindings`. No KMS, no writes.

## Dashboard wiring

Tabs become 7 (assuming Parameter Store from `14-parameter-store-view.md` is in):

```go
const (
    TabEC2 Tab = iota
    TabCloudFront
    TabS3
    TabBeanstalk
    TabLogs
    TabParamStore
    TabSecurityHub
)

var tabNames = []string{"EC2", "CloudFront", "S3", "Beanstalk", "Logs", "Parameter Store", "SecurityHub"}
```

## CapturingInput / InSubnav

```go
func (m Model) CapturingInput() bool {
    return m.mode == modeInsights && m.filterMode
}

func (m Model) InSubnav() bool {
    return m.mode != modeInsights
}
```

Letter hotkeys (1/2/3/4/5 for severity toggles, s for suppressed, a for "all findings") are safe everywhere because the dashboard no longer hijacks letters.

## Empty / unsubscribed handling

SecurityHub must be enabled in the region. `GetInsights` returns `InvalidAccessException` if it's not. Catch that specifically:

```go
var notSubscribed *shtypes.InvalidAccessException
if errors.As(err, &notSubscribed) {
    return errMsg{err: fmt.Errorf("SecurityHub is not enabled in %s. Enable it in the console or run: aws securityhub enable-security-hub", m.ctx.Region)}
}
```

Render that with the existing `errorStyle` so the user knows what to do rather than seeing an opaque "access denied".

## What's deliberately out of scope (v1)

- **Workflow updates** (NEW → NOTIFIED → RESOLVED) and notes via `BatchUpdateFindings`. Easy to misclick; the console is fine for that.
- **Custom insight create / edit.** Console.
- **Suppression rules** (automation_rules / insight filters that auto-suppress). Console.
- **Cross-region aggregation.** Same one-region-at-a-time rule as the rest of the app. If you're using SecurityHub's "aggregation region" feature, point aws-tui at the aggregation region and you see the unified view.
- **Integration with GuardDuty / Inspector standalone views.** The Product field already surfaces those — adding dedicated tabs would mostly duplicate this one.

## Acceptance criteria

- `GetInsights` runs paginated on `Init()`; list shows Name / GroupBy / Source.
- User-owned insights sort above AWS-managed.
- `/` filters insights by name (case-insensitive).
- `enter` on an insight loads its aggregation (`GetInsightResults`) and shows top 50 group values.
- `enter` on a result loads findings filtered by that group; banner shows scope.
- `a` from insights list loads all active findings, no group filter.
- Findings list sorted by severity rank, then updated-at desc.
- Severity prefix is colour-coded; `1`/`2`/`3`/`4`/`5` toggle severity rows; `s` toggles suppressed visibility.
- `enter` on a finding shows the detail view.
- `y a` yanks finding ARN; `y t` yanks title; `y l` yanks remediation URL (or empty status if missing).
- 10m cache on insights, 60s cache on findings; `r` invalidates and reloads.
- Region without SecurityHub enabled shows a clear "enable it" error instead of an opaque API failure.
- SSO expiry uses the existing inline-retry pattern from `03-aws-client.md`.
