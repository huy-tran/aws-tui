# 14 — Parameter Store View

List, search, read, and copy SSM Parameter Store parameters. The primary use case is "what's the current value of `/app-prod/db/password` and how do I get it onto my clipboard without bouncing through the AWS console". Editing parameters is in scope; deletion is not (too easy to nuke prod config by accident).

Parameter Store lives under SSM in the AWS SDK (`github.com/aws/aws-sdk-go-v2/service/ssm`), the same client used by EC2 for `start-session`. No new IAM beyond `ssm:DescribeParameters`, `ssm:GetParameter`, `ssm:GetParametersByPath`, `ssm:PutParameter`, and (for SecureString) `kms:Decrypt` on the relevant key.

## Layout — list

```
┌─ Parameter Store (47 parameters) ──────────────────────────────────┐
│ / __________________                                                │
│                                                                     │
│   Name                              Type          Modified  Ver    │
│ > /app-prod/db/host                 String        2026-04-12 3     │
│   /app-prod/db/password             SecureString  2026-04-12 8     │
│   /app-prod/db/username             String        2026-04-12 1     │
│   /app-prod/redis/url               SecureString  2026-04-10 2     │
│   /app-staging/db/host              String        2026-04-09 1     │
│   /shared/api-keys/stripe           SecureString  2026-03-22 5     │
│                                                                     │
│ enter: view · y: yank value · e: edit · n: new · /: filter · r:    │
│ refresh                                                             │
└─────────────────────────────────────────────────────────────────────┘
```

Names are full paths. The list is sorted alphabetically — the hierarchical sort naturally groups `/app-prod/*` and `/app-staging/*` together, which is what you want when comparing environments.

## Layout — value view (after `enter`)

```
┌─ /app-prod/db/password ────────────────────────────────────────────┐
│ Type:        SecureString                                          │
│ Version:     8                                                     │
│ Last mod:    2026-04-12 14:32:08                                   │
│ Mod by:      arn:aws:iam::123:user/jane                            │
│ KMS Key:     alias/aws/ssm                                         │
│ Description: Postgres password for the prod cluster                │
│                                                                     │
│ ┌─ Value (decrypted) ──────────────────────────────────────────┐   │
│ │ s3cr3t-p@ssw0rd-redacted                                      │   │
│ └───────────────────────────────────────────────────────────────┘   │
│                                                                     │
│ y: yank value · h: history · e: edit · esc: back                   │
└─────────────────────────────────────────────────────────────────────┘
```

For non-SecureString parameters the title above the value box says "Value" (no "decrypted" marker). For StringList types the value box renders one entry per line.

## Layout — history (`h`)

```
┌─ History: /app-prod/db/password ──────────────────────────────────┐
│   Version  Modified              Modified by                      │
│ > 8        2026-04-12 14:32:08   user/jane                         │
│   7        2026-02-03 09:11:42   automation-pipeline               │
│   6        2025-11-17 16:04:22   user/aaron                       │
│   5        2025-08-30 13:22:01   user/jane                         │
│   ...                                                              │
│                                                                     │
│ enter: view that version's value · esc: back                       │
└─────────────────────────────────────────────────────────────────────┘
```

`GetParameterHistory` returns up to 50 entries with pagination. Show first page; the older versions are rarely needed.

## Layout — edit (`e`)

```
┌─ Edit /app-prod/db/password ──────────────────────────────────────┐
│ Type:        SecureString (immutable for this name)               │
│ Current ver: 8                                                     │
│                                                                     │
│ New value:                                                         │
│ ┌───────────────────────────────────────────────────────────────┐  │
│ │ █                                                              │  │
│ │                                                                │  │
│ │                                                                │  │
│ └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│ Description (optional):                                            │
│ [_________________________________________________________________]│
│                                                                     │
│ ctrl+s: save (creates version 9) · esc: cancel                     │
└─────────────────────────────────────────────────────────────────────┘
```

After save, the tab re-fetches the parameter and pushes back to the value view showing the new version.

For prod parameters (path starts with `/prod/` or contains `/prod-`), require an extra confirmation step:

```
┌─ CONFIRM PROD WRITE ──────────────────────────────────────────────┐
│ You are about to overwrite a prod parameter:                       │
│   /app-prod/db/password                                            │
│ Type the parameter name to confirm:                                │
│ [____________________________________________________]             │
│                                                                     │
│ enter to save · esc to cancel                                      │
└─────────────────────────────────────────────────────────────────────┘
```

Mirrors the Beanstalk prod-deploy confirm pattern in `10-beanstalk-view.md`.

## Layout — new (`n`)

```
┌─ Create Parameter ────────────────────────────────────────────────┐
│ Name:        [_____________________________________________]       │
│ Type:        [String] [StringList] [SecureString]   ← tab to cycle │
│ KMS Key:     [alias/aws/ssm                                      ] │
│              (only used when Type is SecureString)                 │
│ Value:                                                             │
│ ┌───────────────────────────────────────────────────────────────┐  │
│ │ █                                                              │  │
│ └───────────────────────────────────────────────────────────────┘  │
│ Description: [____________________________________________]        │
│                                                                     │
│ ctrl+s: create · esc: cancel                                       │
└─────────────────────────────────────────────────────────────────────┘
```

Name must begin with `/`. Reject names that already exist (PutParameter without `Overwrite: true` will error — surface that error inline rather than silently overwriting).

## Implementation (internal/views/paramstore/paramstore.go)

```go
package paramstore

import (
    "context"
    "fmt"
    "sort"
    "strings"
    "time"

    awssdk "github.com/aws/aws-sdk-go-v2/aws"
    ssmsdk "github.com/aws/aws-sdk-go-v2/service/ssm"
    ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
    "github.com/charmbracelet/bubbles/textarea"
    "github.com/charmbracelet/bubbles/textinput"
    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"

    awspkg "github.com/huy-tran/aws-tui/internal/aws"
    "github.com/huy-tran/aws-tui/internal/ui/datatable"
)

const parametersCacheTTL = 60 * time.Second

type Parameter struct {
    Name         string
    Type         string // String / StringList / SecureString
    Version      int64
    LastModified string
    ModifiedBy   string
    Description  string
    KMSKeyID     string
    // Value is loaded lazily by GetParameter; the list view never holds
    // decrypted values in memory.
    Value string
}

type (
    parametersLoadedMsg struct{ items []Parameter }
    parameterValueMsg   struct{ p Parameter }
    parameterSavedMsg   struct{ name string; version int64 }
    historyLoadedMsg    struct{ name string; versions []ParameterVersion }
    errMsg              struct{ err error }
)

type ParameterVersion struct {
    Version      int64
    LastModified string
    ModifiedBy   string
    Type         string
}

type mode int

const (
    modeList mode = iota
    modeValue
    modeHistory
    modeEdit
    modeEditConfirmProd
    modeCreate
)

type Model struct {
    ctx    *awspkg.Context
    mode   mode
    width  int
    height int

    table       datatable.Model
    params      []Parameter
    paramsFilt  []Parameter
    filter      textinput.Model
    filterMode  bool
    loading     bool
    err         error
    status      string

    target   Parameter
    histTable datatable.Model
    history   []ParameterVersion

    valueInput   textarea.Model
    descInput    textinput.Model
    nameInput    textinput.Model
    typeChoice   int // 0=String 1=StringList 2=SecureString
    kmsKeyInput  textinput.Model
    confirm      textinput.Model
}
```

### Listing parameters

Use `DescribeParameters` (metadata only, no values). Paginate to handle accounts with hundreds of parameters.

```go
func (m Model) loadParametersCmd(force bool) tea.Cmd {
    key := "ssm:params:" + m.ctx.Region
    ctx := m.ctx
    return func() tea.Msg {
        if !force {
            if cached, ok := ctx.Cache.Get(key); ok {
                if items, ok := cached.([]Parameter); ok {
                    return parametersLoadedMsg{items: items}
                }
            }
        }
        if err := ctx.Load(context.Background()); err != nil {
            return errMsg{err: err}
        }
        client := ctx.SSM()
        var items []Parameter
        paginator := ssmsdk.NewDescribeParametersPaginator(client, &ssmsdk.DescribeParametersInput{
            MaxResults: awssdk.Int32(50),
        })
        for paginator.HasMorePages() {
            page, err := paginator.NextPage(context.Background())
            if err != nil {
                return errMsg{err: err}
            }
            for _, p := range page.Parameters {
                items = append(items, Parameter{
                    Name:         awssdk.ToString(p.Name),
                    Type:         string(p.Type),
                    Version:      p.Version,
                    LastModified: formatTime(p.LastModifiedDate),
                    ModifiedBy:   awssdk.ToString(p.LastModifiedUser),
                    Description:  awssdk.ToString(p.Description),
                    KMSKeyID:     awssdk.ToString(p.KeyId),
                })
            }
        }
        sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
        ctx.Cache.Set(key, items, parametersCacheTTL)
        return parametersLoadedMsg{items: items}
    }
}
```

The 60s cache matches the rest of the app. **Do not** cache decrypted values — fetch on demand each time the user opens a parameter.

### Fetching one value

```go
func (m Model) loadValueCmd(name string) tea.Cmd {
    ctx := m.ctx
    return func() tea.Msg {
        client := ctx.SSM()
        out, err := client.GetParameter(context.Background(), &ssmsdk.GetParameterInput{
            Name:           awssdk.String(name),
            WithDecryption: awssdk.Bool(true),
        })
        if err != nil {
            return errMsg{err: err}
        }
        p := Parameter{
            Name:         awssdk.ToString(out.Parameter.Name),
            Type:         string(out.Parameter.Type),
            Version:      out.Parameter.Version,
            LastModified: formatTime(out.Parameter.LastModifiedDate),
            Value:        awssdk.ToString(out.Parameter.Value),
        }
        return parameterValueMsg{p: p}
    }
}
```

`WithDecryption: true` is always set — if the user pressed enter on a SecureString they want to see it. The IAM error message from a missing `kms:Decrypt` permission is descriptive enough to surface verbatim.

### Yank to clipboard

Same pattern as the rest of the app:

```go
case "y":
    if v := m.target.Value; v != "" {
        m.status = doYank(v, "value")
    }
```

Always copy the **raw value**, never the rendered form (no leading "Value: " etc.).

### Writing

```go
func (m Model) saveCmd(name, value, kmsKey, description string, paramType ssmtypes.ParameterType, overwrite bool) tea.Cmd {
    ctx := m.ctx
    return func() tea.Msg {
        client := ctx.SSM()
        in := &ssmsdk.PutParameterInput{
            Name:      awssdk.String(name),
            Value:     awssdk.String(value),
            Type:      paramType,
            Overwrite: awssdk.Bool(overwrite),
        }
        if description != "" {
            in.Description = awssdk.String(description)
        }
        if paramType == ssmtypes.ParameterTypeSecureString && kmsKey != "" {
            in.KeyId = awssdk.String(kmsKey)
        }
        out, err := client.PutParameter(context.Background(), in)
        if err != nil {
            return errMsg{err: err}
        }
        ctx.Cache.Invalidate("ssm:params:" + ctx.Region)
        return parameterSavedMsg{name: name, version: out.Version}
    }
}
```

`Overwrite: true` for edits, `false` for creates. The list cache is invalidated so the modified date / version refreshes when the user goes back.

### History

```go
func (m Model) loadHistoryCmd(name string) tea.Cmd {
    ctx := m.ctx
    return func() tea.Msg {
        client := ctx.SSM()
        out, err := client.GetParameterHistory(context.Background(), &ssmsdk.GetParameterHistoryInput{
            Name:           awssdk.String(name),
            WithDecryption: awssdk.Bool(false), // metadata only
            MaxResults:     awssdk.Int32(50),
        })
        if err != nil {
            return errMsg{err: err}
        }
        versions := make([]ParameterVersion, 0, len(out.Parameters))
        for _, p := range out.Parameters {
            versions = append(versions, ParameterVersion{
                Version:      p.Version,
                LastModified: formatTime(p.LastModifiedDate),
                ModifiedBy:   awssdk.ToString(p.LastModifiedUser),
                Type:         string(p.Type),
            })
        }
        sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
        return historyLoadedMsg{name: name, versions: versions}
    }
}
```

History view fetches metadata only. If the user presses `enter` on an old version, fire `GetParameter` with `Selector: aws.String(":" + strconv.FormatInt(version, 10))` to decrypt that specific version.

## AWS context wiring

Add an `SSM()` method to `internal/aws/client.go` that returns a cached `*ssm.Client`, mirroring the existing `EC2()`, `S3()`, etc. helpers. Reuse the same SSM client across EC2's `start-session` flows and this view — they hit the same service.

## Dashboard wiring (06-main-dashboard.md)

Add a sixth tab:

```go
const (
    TabEC2 Tab = iota
    TabCloudFront
    TabS3
    TabBeanstalk
    TabLogs
    TabParamStore
)

var tabNames = []string{"EC2", "CloudFront", "S3", "Beanstalk", "Logs", "Parameter Store"}
```

The dashboard already uses arrow / tab keys for navigation (the letter hotkeys were removed in a follow-up), so no per-letter wiring is needed.

The Model's `tabs` slice grows from 5 to 6:

```go
m.tabs = make([]tea.Model, 6)
m.tabs[TabParamStore] = paramstore.New(ctx)
```

Update the `m.tabs [5]tea.Model` array literal to a slice if it isn't already.

## Filter and CapturingInput / InSubnav

The view participates in the same input-capture / subnav interfaces the dashboard uses:

```go
func (m Model) CapturingInput() bool {
    if m.mode == modeList && m.filterMode {
        return true
    }
    return m.mode == modeEdit ||
        m.mode == modeEditConfirmProd ||
        m.mode == modeCreate
}

func (m Model) InSubnav() bool {
    return m.mode != modeList
}
```

So pressing `e` in modeList doesn't get hijacked by anything, and arrows in modeValue / modeEdit / modeHistory stay local instead of cycling tabs.

## Safety rails

- **Decrypted values are never cached.** `GetParameter` is called every time the user opens a SecureString. The 60s list cache only stores metadata.
- **Decrypted values are never written to disk.** No state file changes for this view. Clipboard yank is fine — that's exactly what the user asked for.
- **Prod-name confirmation.** Edit and Save (PutParameter) require typing the parameter name if the name path contains `/prod` (substring match, case-insensitive). Same pattern as `10-beanstalk-view.md`.
- **No delete operation in v1.** Even with a confirmation step it's too easy to lose a value with no recovery path. Add later if needed, but `DeleteParameter` is irreversible (history is wiped with the parameter).
- **SecureString value display.** Show plainly inside a bordered box — no asterisk masking, no "press space to reveal". The user opened it intentionally; obscuring it just makes them yank-paste into another buffer to read it.

## Acceptance criteria

- `DescribeParameters` runs paginated on `Init()`; list shows Name / Type / Modified / Version.
- `/` filters by name substring (case-insensitive).
- `enter` opens the value view, fetching the decrypted value on demand.
- `y` on the value view yanks the raw value to the clipboard.
- `h` opens history with all versions; `enter` on a version loads its value.
- `e` opens edit; `ctrl+s` calls PutParameter with `Overwrite: true`.
- `n` opens create with type cycling via tab; rejects names without leading `/`.
- Names containing `/prod` (case-insensitive) trigger a name-typed confirmation before save.
- 60s cache on the list; manual `r` refresh invalidates and reloads.
- Decrypted values never appear in cache or state files.
- SSO expiry surfaces with the existing inline-retry pattern from `03-aws-client.md`.
