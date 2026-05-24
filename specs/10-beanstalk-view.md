# 10 — Elastic Beanstalk View

List environments with health colour coding, view recent events, see deployed version. Deploy a different version label.

## Layout — environment list

```
┌─ Elastic Beanstalk Environments ──────────────────────────────────┐
│   App / Environment              Health      Status       Version │
│ > app-prod / app-prod-web        ● Green     Ready        v1.42.0 │
│   app-prod / app-prod-worker     ● Green     Ready        v1.42.0 │
│   app-staging / app-staging-web  ● Yellow    Updating     v1.43.0 │
│   client-x / client-x-prod       ● Red       Ready        v2.1.4  │
│                                                                   │
│ enter: details · e: events · d: deploy · r: refresh               │
└───────────────────────────────────────────────────────────────────┘
```

Health dot colours: Green / Yellow / Red / Grey (Unknown) / Grey (NoData) mapped to lipgloss colours.

## Layout — environment details

```
┌─ app-prod-web ────────────────────────────────────────────────────┐
│  Application:        app-prod                                      │
│  Environment ID:     e-abc123def4                                  │
│  CNAME:              app-prod-web.eba-xxxx.ap-southeast-2.eb...   │
│  Status:             Ready                                         │
│  Health:             ● Green   (Ok)                                │
│  Version:            v1.42.0                                       │
│  Platform:           PHP 8.3 running on 64bit Amazon Linux 2023    │
│  Tier:               WebServer                                     │
│  Instances:          3 healthy, 0 unhealthy                        │
│  Last updated:       2026-05-22 09:14:32 UTC                       │
│                                                                    │
│  Recent events (last 5):                                           │
│    10:14:32  INFO   Environment update completed successfully.     │
│    10:12:14  INFO   New application version was deployed to...     │
│    10:11:02  INFO   Created RDS database security group...         │
│    ...                                                             │
│                                                                    │
│  e: full events · d: deploy · h: health detail · esc: back         │
└────────────────────────────────────────────────────────────────────┘
```

## Layout — events tail

```
┌─ Events: app-prod-web ────────────────────────────────────────────┐
│ 10:14:32  INFO     Environment update completed successfully.     │
│ 10:12:14  INFO     New application version was deployed to        │
│                    running EC2 instances.                          │
│ 10:11:02  INFO     Updating environment app-prod-web's            │
│                    configuration settings.                         │
│ 10:10:55  INFO     Application available at app-prod-web.eba...   │
│ 09:45:12  WARN     Environment health transitioned from Ok to    │
│                    Severe. ELB health: 50% of requests failing.   │
│ 09:44:01  INFO     Successfully launched environment.             │
│                                                                    │
│ ↑/↓ scroll · r: refresh · esc: back                               │
└────────────────────────────────────────────────────────────────────┘
```

Severity colour: INFO grey, WARN yellow, ERROR red.

## Layout — deploy modal

```
┌─ Deploy to app-prod-web ──────────────────────────────────────────┐
│                                                                   │
│  Available versions:                                              │
│  > v1.43.0   2026-05-22  Active     "Hotfix: session expiry"    │
│    v1.42.0   2026-05-21  Active     "Q2 invoice export"          │
│    v1.41.0   2026-05-19  Active     "Cron job retry fix"         │
│    v1.40.0   2026-05-15  Active     "Initial Q2 release"         │
│                                                                   │
│  Currently deployed: v1.42.0                                      │
│                                                                   │
│  enter: deploy selected · esc: cancel                             │
└───────────────────────────────────────────────────────────────────┘
```

After enter: confirmation modal with the diff (currently deployed vs selected). For prod profiles, require typing the environment name as confirmation.

## Implementation notes (internal/views/beanstalk/beanstalk.go)

API calls needed:

```go
import eb "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"

// List environments across all applications
client.DescribeEnvironments(ctx, &eb.DescribeEnvironmentsInput{
    IncludeDeleted: aws.Bool(false),
})

// Health (more detailed than DescribeEnvironments returns)
client.DescribeEnvironmentHealth(ctx, &eb.DescribeEnvironmentHealthInput{
    EnvironmentName: aws.String(envName),
    AttributeNames:  []ebtypes.EnvironmentHealthAttribute{
        "All",
    },
})

// Events
client.DescribeEvents(ctx, &eb.DescribeEventsInput{
    EnvironmentName: aws.String(envName),
    MaxRecords:      aws.Int32(50),
})

// Versions
client.DescribeApplicationVersions(ctx, &eb.DescribeApplicationVersionsInput{
    ApplicationName: aws.String(appName),
})

// Deploy
client.UpdateEnvironment(ctx, &eb.UpdateEnvironmentInput{
    EnvironmentName: aws.String(envName),
    VersionLabel:    aws.String(versionLabel),
})
```

## Health colour mapping

```go
func healthDot(color string) string {
	style := lipgloss.NewStyle()
	switch color {
	case "Green":
		style = style.Foreground(lipgloss.Color("34"))
	case "Yellow":
		style = style.Foreground(lipgloss.Color("214"))
	case "Red":
		style = style.Foreground(lipgloss.Color("160"))
	default:
		style = style.Foreground(lipgloss.Color("245"))
	}
	return style.Render("●") + " " + color
}
```

## Auto-refresh while updating

If any environment has status `Updating` or `Launching`, auto-tick every 10 seconds to refresh. Same pattern as CloudFront invalidations. Stop when no environments are in transitional states.

## Caching

Environment list: 60s cache.
Events: never cache (always fresh — but limit to last 50).
Versions: 5 minute cache.

## Acceptance criteria

- Environment list groups by application, shows health colour dot.
- Environments in transitional state (Updating, Launching, Terminating) trigger auto-poll.
- Events view shows colour-coded severity, scrollable, with timestamps in local time.
- Deploy modal lists application versions with descriptions.
- Prod environment deploys require typing the environment name to confirm.
- `r` refreshes the current view.
