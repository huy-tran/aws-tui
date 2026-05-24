package s3

import (
	"context"
	"encoding/gob"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
)

const objectsCacheTTL = 30 * time.Second

func init() { gob.Register(prefixLoadedMsg{}) }
const objectsPageMax = 1000

type Object struct {
	Key      string
	Size     int64
	Modified string
}

type entry struct {
	name     string
	isFolder bool
	size     int64
	modified string
}

type (
	prefixLoadedMsg struct {
		prefix    string
		folders   []string
		objects   []Object
		truncated bool
	}
)

type browserModel struct {
	ctx    *awspkg.Context
	store  *state.Store
	bucket Bucket
	region string

	prefix     string
	prefixHist []string // stack of prefixes for going "up"
	entries    []entry
	truncated  bool
	loading    bool
	err        error
	status     string
	yankPending bool
	table      datatable.Model
	loader     loader.Model
}

func newBrowser(ctx *awspkg.Context, store *state.Store, bucket Bucket) browserModel {
	region := bucket.Region
	if region == "" {
		region = ctx.Region
	}

	cols := []datatable.Column{
		{Title: "Name", Flex: true},
		{Title: "Size"},
		{Title: "Modified"},
	}
	t := datatable.New(cols)
	t.SetHeight(20)

	return browserModel{
		ctx:    ctx,
		store:  store,
		bucket: bucket,
		region: region,
		table:  t,
		loader: loader.New(),
		loading: true,
	}
}

func (m browserModel) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.listPrefix(m.prefix, false))
}

func (m browserModel) listPrefix(prefix string, force bool) tea.Cmd {
	key := "s3:objects:" + m.bucket.Name + ":" + prefix
	ctx := m.ctx
	region := m.region
	bucket := m.bucket.Name
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if msg, ok := cached.(prefixLoadedMsg); ok {
					return msg
				}
			}
		}
		client := ctx.S3InRegion(region)
		paginator := s3sdk.NewListObjectsV2Paginator(client, &s3sdk.ListObjectsV2Input{
			Bucket:    awssdk.String(bucket),
			Prefix:    awssdk.String(prefix),
			Delimiter: awssdk.String("/"),
			MaxKeys:   awssdk.Int32(int32(objectsPageMax)),
		})
		var folders []string
		var objects []Object
		truncated := false
		// only load first page to enforce the 1000-key cap on v1
		if paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			for _, p := range page.CommonPrefixes {
				folders = append(folders, awssdk.ToString(p.Prefix))
			}
			for _, o := range page.Contents {
				objects = append(objects, Object{
					Key:      awssdk.ToString(o.Key),
					Size:     derefInt64(o.Size),
					Modified: formatTime(o.LastModified),
				})
			}
			if paginator.HasMorePages() {
				truncated = true
			}
		}
		msg := prefixLoadedMsg{prefix: prefix, folders: folders, objects: objects, truncated: truncated}
		ctx.Cache.Set(key, msg, objectsCacheTTL)
		return msg
	}
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func (m browserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h := msg.Height - 4
		if h < 3 {
			h = 3
		}
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		return m, nil

	case prefixLoadedMsg:
		m.loading = false
		m.prefix = msg.prefix
		m.truncated = msg.truncated
		m.entries = mergeEntries(msg.folders, msg.objects)
		m.table.SetRows(buildEntryRows(m.entries))
		m.table.GotoTop()
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			return m, nil
		}
		var cmd tea.Cmd
		m.loader, cmd = m.loader.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.updateKeys(msg)
	}
	return m, nil
}

func (m browserModel) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.yankPending {
		m.yankPending = false
		e := m.selected()
		if e == nil {
			m.status = "no entry selected"
			return m, nil
		}
		key := m.prefix + strings.TrimPrefix(e.name, m.prefix)
		if e.isFolder {
			// e.name already includes full prefix for CommonPrefixes
			key = e.name
		} else {
			key = e.name
		}
		switch msg.String() {
		case "u":
			m.status = doYank(fmt.Sprintf("s3://%s/%s", m.bucket.Name, key), "s3:// URI")
		case "h":
			m.status = doYank(
				fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", m.bucket.Name, m.region, key),
				"https URL")
		case "k":
			m.status = doYank(key, "key")
		case "b":
			m.status = doYank(m.bucket.Name, "bucket name")
		default:
			m.status = ""
		}
		return m, nil
	}

	switch msg.String() {
	case "esc":
		if m.prefix != "" && len(m.prefixHist) > 0 {
			parent := m.prefixHist[len(m.prefixHist)-1]
			m.prefixHist = m.prefixHist[:len(m.prefixHist)-1]
			m.loading = true
			return m, tea.Batch(m.loader.Tick(), m.listPrefix(parent, false))
		}
		return m, nav.PopView()
	case "r":
		m.ctx.Cache.Invalidate("s3:objects:" + m.bucket.Name + ":" + m.prefix)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.listPrefix(m.prefix, true))
	case "l":
		// "load more" — currently the v1 cap is one page; this is a no-op
		// telling the user to refine the prefix or filter instead.
		if m.truncated {
			m.status = "v1 cap: refine the prefix to see more keys"
		}
		return m, nil
	case "y":
		m.yankPending = true
		m.status = "yank: u=s3://, h=https, k=key, b=bucket"
		return m, nil
	case "enter":
		e := m.selected()
		if e == nil {
			return m, nil
		}
		if e.isFolder {
			m.prefixHist = append(m.prefixHist, m.prefix)
			m.loading = true
			return m, tea.Batch(m.loader.Tick(), m.listPrefix(e.name, false))
		}
		return m, nav.PushView(newDownload(m.ctx, m.region, m.bucket.Name, *e))
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m browserModel) CapturingInput() bool {
	return m.yankPending
}

func (m browserModel) HelpItems() []help.Section {
	return []help.Section{{
		Title: "S3 · object browser",
		Items: []help.Item{
			{Keys: "enter", Desc: "open folder / download file"},
			{Keys: "esc", Desc: "go up one prefix (or back to bucket list)"},
			{Keys: "y", Desc: "yank menu: u=s3://, h=https, k=key, b=bucket"},
			{Keys: "r", Desc: "refresh prefix"},
			{Keys: "↑/↓ j/k", Desc: "move cursor"},
		},
	}}
}

func (m browserModel) selected() *entry {
	if m.loading || len(m.entries) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.entries) {
		return nil
	}
	return &m.entries[idx]
}

func (m browserModel) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\nesc to back · r to retry", m.err))
	}
	title := headerStyle.Render(fmt.Sprintf("s3://%s/%s", m.bucket.Name, m.prefix))
	if m.loading {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("loading..."))
	}
	body := m.table.View()
	if len(m.entries) == 0 {
		body = mutedStyle.Render("(empty)")
	}
	notes := []string{}
	if m.truncated {
		notes = append(notes, mutedStyle.Render(fmt.Sprintf("showing first %d entries; refine the prefix to see more", objectsPageMax)))
	}
	if m.status != "" {
		notes = append(notes, mutedStyle.Render(m.status))
	}
	help := mutedStyle.Render("enter: open/download · y u/h/k/b: yank · r: refresh · esc: up/back")

	parts := []string{title, ""}
	if len(m.prefixHist) > 0 || m.prefix != "" {
		parts = append(parts, mutedStyle.Render("esc: go up"), "")
	}
	parts = append(parts, body, "")
	parts = append(parts, notes...)
	parts = append(parts, help)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func mergeEntries(folders []string, objects []Object) []entry {
	out := make([]entry, 0, len(folders)+len(objects))
	for _, f := range folders {
		out = append(out, entry{name: f, isFolder: true})
	}
	for _, o := range objects {
		out = append(out, entry{name: o.Key, isFolder: false, size: o.Size, modified: o.Modified})
	}
	return out
}

func buildEntryRows(entries []entry) []datatable.Row {
	rows := make([]datatable.Row, len(entries))
	for i, e := range entries {
		name := e.name
		size := "-"
		modified := "-"
		if e.isFolder {
			size = "<prefix>"
		} else {
			size = humanBytes(e.size)
			modified = e.modified
		}
		rows[i] = datatable.Row{name, size, modified}
	}
	return rows
}

func humanBytes(b int64) string {
	if b == 0 {
		return "0 B"
	}
	const k = 1024
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	f := float64(b)
	for f >= k && i < len(units)-1 {
		f /= k
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", b, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// silence unused-import warning when removing the dependency would be nicer
// but keeping types as-is is the lower-friction path.
var _ = s3types.Object{}
