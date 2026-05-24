# 11 — CloudWatch Logs View

List log groups, drill into log streams, tail logs. For real-time tail we shell out to `aws logs tail` or `saw` if installed, since reimplementing log streaming UX inside the TUI is significant scope.

## Layout — log groups

```
┌─ CloudWatch Log Groups ───────────────────────────────────────────┐
│ Filter: ___________                                                │
│                                                                    │
│   Log Group                                       Retention   Size │
│ > /aws/elasticbeanstalk/app-prod-web/var/log     14 days    1.2 G │
│   /aws/elasticbeanstalk/app-prod-web/var/log/...  14 days    342 M │
│   /aws/lambda/image-processor                     30 days    24 M  │
│   /aws/rds/instance/app-prod-db/error             7 days     8.4 G │
│   /aws/cloudtrail/management                      Never      —     │
│                                                                    │
│ enter: streams · t: tail · s: search · r: refresh                  │
└────────────────────────────────────────────────────────────────────┘
```

## Layout — log streams

```
┌─ Streams: /aws/elasticbeanstalk/app-prod-web/var/log ─────────────┐
│   Stream Name                              Last Event             │
│ > i-0abc123def456789                       2026-05-22 10:15:02    │
│   i-0def456abc789012                       2026-05-22 10:14:58    │
│   i-0aaa111bbb222ccc                       2026-05-22 10:14:55    │
│                                                                    │
│ enter: tail · v: view recent · esc: back                          │
└────────────────────────────────────────────────────────────────────┘
```

## Layout — search

```
┌─ Search: /aws/lambda/image-processor ─────────────────────────────┐
│                                                                    │
│  Pattern: ERROR _________________________                         │
│                                                                    │
│  Time range: [last 1h] [last 24h] [last 7d] [custom...]            │
│                                                                    │
│  enter: search · esc: cancel                                       │
└────────────────────────────────────────────────────────────────────┘
```

Uses `FilterLogEvents` with the pattern. Pattern format follows CloudWatch's filter syntax.

## Layout — log viewer (after tail or search)

```
┌─ /aws/lambda/image-processor · last 1h · "ERROR" (24 matches) ────┐
│ 09:14:32 [i-0abc] ERROR Failed to process image: timeout         │
│ 09:18:11 [i-0abc] ERROR Failed to process image: invalid format  │
│ 09:42:55 [i-0def] ERROR Database connection lost, retrying...    │
│ ...                                                                │
│                                                                    │
│ /: filter · y: yank line · esc: back · ↑/↓ scroll                 │
└────────────────────────────────────────────────────────────────────┘
```

## Implementation notes

### Listing log groups

```go
import logs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

client.DescribeLogGroups(ctx, &logs.DescribeLogGroupsInput{
    Limit: aws.Int32(50),
})
```

Pagination: use the paginator and load 50 at a time. For accounts with hundreds of log groups, **don't load them all up front**. Show first 50, let user filter or load more with `m`.

### Tail with `aws logs tail`

The cleanest approach. Suspend TUI, run:

```go
cmd := exec.Command("aws", "logs", "tail",
    "--profile", m.ctx.Profile,
    "--region", m.ctx.Region,
    "--follow",
    "--format", "short",
    groupName,
)
return tea.ExecProcess(cmd, ...)
```

User exits with `Ctrl+C` to return to TUI. Add a tip on first launch: "Use `Ctrl+C` to exit the tail and return."

### Tail inside the TUI (optional, more work)

If you want it in-TUI, use `StartLiveTail` (the new streaming API):

```go
client.StartLiveTail(ctx, &logs.StartLiveTailInput{
    LogGroupIdentifiers: []string{groupArn},
})
```

This returns an event stream. Read events in a goroutine, send `logLineMsg` to the model. Complex to get right (backpressure, reconnects), so v1 should shell out.

### Search via FilterLogEvents

```go
out, err := client.FilterLogEvents(ctx, &logs.FilterLogEventsInput{
    LogGroupName:  aws.String(groupName),
    FilterPattern: aws.String(pattern),
    StartTime:     aws.Int64(startMs),
    EndTime:       aws.Int64(endMs),
    Limit:         aws.Int32(1000),
})
```

Show up to 1000 results in a scrollable viewport. Add "load more" if `NextToken` is present.

### Size and retention display

```go
func humanBytes(b int64) string {
    if b == 0 { return "—" }
    const k = 1024
    units := []string{"B", "K", "M", "G", "T"}
    i := 0
    f := float64(b)
    for f >= k && i < len(units)-1 {
        f /= k
        i++
    }
    return fmt.Sprintf("%.1f %s", f, units[i])
}

func retentionLabel(days *int32) string {
    if days == nil { return "Never" }
    if *days == 1 { return "1 day" }
    return fmt.Sprintf("%d days", *days)
}
```

## Acceptance criteria

- Log groups list paginates 50 at a time with manual "load more".
- Each group shows retention and storage size.
- `t` on a group runs `aws logs tail --follow` via `tea.ExecProcess`.
- `s` opens search modal with pattern + time range.
- Search results are scrollable with line-yank support.
- `enter` on a group shows its streams sorted by most recent event.
- `r` refreshes the log groups list (invalidates cache).
