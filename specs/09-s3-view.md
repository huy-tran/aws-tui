# 09 — S3 View

Browse buckets and their contents tree-style. Optimised for finding objects and copying S3 URIs rather than bulk operations.

## Layout — bucket list

```
┌─ S3 Buckets ──────────────────────────────────────────────────────┐
│ Filter: ___________                                                │
│                                                                    │
│   Bucket                          Region          Created          │
│ > app-prod-uploads                ap-southeast-2  2023-04-12       │
│   app-prod-static                 ap-southeast-2  2023-04-12       │
│   app-prod-backups                ap-southeast-2  2023-05-01       │
│   client-website-assets           us-east-1       2024-02-19       │
│                                                                    │
│ enter: open · y u: yank s3:// URI · y h: yank https URL · r refresh│
└────────────────────────────────────────────────────────────────────┘
```

## Layout — object browser

```
┌─ s3://app-prod-uploads/users/12345/ ──────────────────────────────┐
│ < ..                                                               │
│   avatars/                                              <prefix>   │
│   documents/                                            <prefix>   │
│ > profile.json                            2.4 KB    2026-05-22     │
│   invoice-2024-q4.pdf                     128 KB    2024-12-31     │
│   notes.md                                  847 B   2026-03-14     │
│                                                                    │
│ enter: open/download · y u: yank URI · y h: yank URL · esc: back  │
└────────────────────────────────────────────────────────────────────┘
```

## Implementation notes

### Listing

`ListBuckets` returns all buckets (no pagination at the SDK call level). The Region field requires a separate `GetBucketLocation` call per bucket, which is slow. Strategies:

1. **On bucket list load**, just show names. Region column shows "loading…" until populated.
2. **Issue `GetBucketLocation` in parallel** for all buckets via goroutines (limit to 10 concurrent). Send incremental update messages.
3. **Cache the region map** in state — buckets rarely change region.

```go
type bucketRegionsMsg struct {
	regions map[string]string
}

func loadBucketRegions(client *s3.Client, names []string) tea.Cmd {
	return func() tea.Msg {
		out := make(map[string]string, len(names))
		sem := make(chan struct{}, 10)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, name := range names {
			wg.Add(1)
			sem <- struct{}{}
			go func(n string) {
				defer wg.Done()
				defer func() { <-sem }()
				loc, err := client.GetBucketLocation(context.Background(),
					&s3.GetBucketLocationInput{Bucket: aws.String(n)})
				if err != nil {
					return
				}
				region := string(loc.LocationConstraint)
				if region == "" {
					region = "us-east-1"
				}
				mu.Lock()
				out[n] = region
				mu.Unlock()
			}(name)
		}
		wg.Wait()
		return bucketRegionsMsg{regions: out}
	}
}
```

### Object listing

Use the `ListObjectsV2` paginator with `Delimiter: "/"` and `Prefix: currentPrefix` to get tree-style browsing:

```go
func listPrefix(client *s3.Client, bucket, prefix string) tea.Cmd {
	return func() tea.Msg {
		paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
			Bucket:    aws.String(bucket),
			Prefix:    aws.String(prefix),
			Delimiter: aws.String("/"),
			MaxKeys:   aws.Int32(1000),
		})
		var folders []string
		var objects []s3types.Object
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			for _, p := range page.CommonPrefixes {
				folders = append(folders, aws.ToString(p.Prefix))
			}
			objects = append(objects, page.Contents...)
		}
		return prefixLoadedMsg{folders: folders, objects: objects, prefix: prefix}
	}
}
```

### Bucket region matters

When browsing a bucket, create the client with that bucket's region, not the dashboard's region:

```go
func (m Model) clientForBucket(bucket string) *s3.Client {
	region := m.bucketRegions[bucket]
	if region == "" {
		region = m.ctx.Region
	}
	cfg := m.ctx.AWSConfig() // expose internal config
	cfg.Region = region
	return s3.NewFromConfig(cfg)
}
```

You'll need to add an `AWSConfig()` accessor to `aws.Context` that returns a copy of the config.

### Pagination safety

For prefixes with > 10,000 objects, do NOT auto-load all pages. Show "Showing first 1000 objects. Press `l` to load more." Avoids accidentally hammering the API on huge buckets.

### Yank operations

| Key | Yanks |
|-----|-------|
| `y u` | `s3://bucket/key` URI |
| `y h` | `https://bucket.s3.region.amazonaws.com/key` URL |
| `y k` | Key only |
| `y b` | Bucket name |

Use `github.com/atotto/clipboard`. Show a brief toast "Copied s3://… to clipboard" for 1.5s.

### Download

`enter` on an object opens a confirmation modal:

```
┌─ Download object ──────────────────────────────────┐
│ Key:  users/12345/profile.json                     │
│ Size: 2.4 KB                                       │
│                                                    │
│ Save to: ~/Downloads/profile.json                  │
│                                                    │
│ [y] download   [n] cancel                          │
└────────────────────────────────────────────────────┘
```

Use `GetObject` and stream to file with a progress indicator for files > 1MB. Don't download files > 100MB without an extra confirmation.

## What we deliberately skip in v1

- Upload (use the AWS CLI for that)
- Multipart upload progress
- Bucket policy editing
- Object metadata editing
- Versioning views (could add later)
- Cross-account bucket access

## Acceptance criteria

- Bucket list loads quickly; regions populate asynchronously.
- Bucket region is cached in state across launches.
- Object browser navigates tree-style with `enter` into prefixes, `..` or `esc` to go up.
- Yank shortcuts work for URI, HTTPS URL, key, bucket.
- Downloads prompt for path, show progress for larger files.
- Buckets with > 1000 objects per prefix show a "load more" hint instead of auto-paging forever.
