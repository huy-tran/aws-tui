package s3

import (
	"context"
	"encoding/gob"
	"fmt"
	"sync"
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
const deleteConcurrency = 8

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
	deleteDoneMsg struct {
		ok     int
		failed map[string]string // key -> err msg
	}
)

type browserModel struct {
	ctx    *awspkg.Context
	store  *state.Store
	bucket Bucket
	region string

	prefix     string
	prefixHist []string
	entries    []entry
	truncated  bool
	loading    bool
	err        error
	status     string

	selected      map[string]bool
	yankPending   bool
	confirmDelete bool
	deleting      bool

	table  datatable.Model
	loader loader.Model
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
		ctx:      ctx,
		store:    store,
		bucket:   bucket,
		region:   region,
		table:    t,
		loader:   loader.New(),
		loading:  true,
		selected: make(map[string]bool),
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
		// Reserve rows for the title, the "esc: go up" note, the help line
		// and the blank spacer rows the list view inserts around the table.
		h := msg.Height - 6
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
		m.table.SetRows(buildEntryRows(m.entries, m.selected))
		m.table.GotoTop()
		return m, nil

	case refreshPrefixMsg:
		m.ctx.Cache.Invalidate("s3:objects:" + m.bucket.Name + ":" + m.prefix)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.listPrefix(m.prefix, true))

	case deleteDoneMsg:
		m.deleting = false
		m.selected = make(map[string]bool)
		switch {
		case len(msg.failed) == 0:
			m.status = fmt.Sprintf("deleted %d object(s)", msg.ok)
		case msg.ok == 0:
			m.status = fmt.Sprintf("delete failed for all %d object(s)", len(msg.failed))
		default:
			m.status = fmt.Sprintf("deleted %d, failed %d", msg.ok, len(msg.failed))
		}
		m.ctx.Cache.Invalidate("s3:objects:" + m.bucket.Name + ":" + m.prefix)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.listPrefix(m.prefix, true))

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if !m.loading && !m.deleting {
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
	if m.deleting {
		return m, nil
	}

	if m.confirmDelete {
		switch msg.String() {
		case "y":
			m.confirmDelete = false
			keys := m.pendingDeleteKeys()
			if len(keys) == 0 {
				m.status = "nothing to delete"
				return m, nil
			}
			m.deleting = true
			m.status = ""
			return m, tea.Batch(m.loader.Tick(), m.deleteCmd(keys))
		case "n", "esc":
			m.confirmDelete = false
			m.status = ""
			return m, nil
		}
		return m, nil
	}

	if m.yankPending {
		m.yankPending = false
		e := m.selectedEntry()
		if e == nil {
			m.status = "no entry selected"
			return m, nil
		}
		key := e.name
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
			m.selected = make(map[string]bool)
			m.loading = true
			return m, tea.Batch(m.loader.Tick(), m.listPrefix(parent, false))
		}
		return m, nav.PopView()
	case "r":
		m.ctx.Cache.Invalidate("s3:objects:" + m.bucket.Name + ":" + m.prefix)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.listPrefix(m.prefix, true))
	case "l":
		if m.truncated {
			m.status = "v1 cap: refine the prefix to see more keys"
		}
		return m, nil
	case "y":
		m.yankPending = true
		m.status = "yank: u=s3://, h=https, k=key, b=bucket"
		return m, nil
	case "ctrl+u":
		return m, nav.PushView(newUpload(m.ctx, m.region, m.bucket.Name, m.prefix))
	case " ":
		e := m.selectedEntry()
		if e == nil || e.isFolder {
			return m, nil
		}
		if m.selected[e.name] {
			delete(m.selected, e.name)
		} else {
			m.selected[e.name] = true
		}
		m.table.SetRows(buildEntryRows(m.entries, m.selected))
		m.status = fmt.Sprintf("%d selected", len(m.selected))
		return m, nil
	case "ctrl+d":
		keys := m.pendingDeleteKeys()
		if len(keys) == 0 {
			m.status = "nothing to delete (cursor on a folder?)"
			return m, nil
		}
		m.confirmDelete = true
		if len(keys) == 1 {
			m.status = fmt.Sprintf("delete %s? (y/n)", keys[0])
		} else {
			m.status = fmt.Sprintf("delete %d selected object(s)? (y/n)", len(keys))
		}
		return m, nil
	case "enter":
		e := m.selectedEntry()
		if e == nil {
			return m, nil
		}
		if e.isFolder {
			m.prefixHist = append(m.prefixHist, m.prefix)
			m.selected = make(map[string]bool)
			m.loading = true
			return m, tea.Batch(m.loader.Tick(), m.listPrefix(e.name, false))
		}
		return m, nav.PushView(newDownload(m.ctx, m.region, m.bucket.Name, *e))
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// pendingDeleteKeys returns the keys to delete: all selected keys if any,
// otherwise just the cursor row (if it's a file).
func (m browserModel) pendingDeleteKeys() []string {
	if len(m.selected) > 0 {
		keys := make([]string, 0, len(m.selected))
		for k := range m.selected {
			keys = append(keys, k)
		}
		return keys
	}
	e := m.selectedEntry()
	if e == nil || e.isFolder {
		return nil
	}
	return []string{e.name}
}

func (m browserModel) deleteCmd(keys []string) tea.Cmd {
	ctx := m.ctx
	region := m.region
	bucket := m.bucket.Name
	toDelete := append([]string(nil), keys...)
	return func() tea.Msg {
		client := ctx.S3InRegion(region)
		failed := make(map[string]string)
		ok := 0
		sem := make(chan struct{}, deleteConcurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, k := range toDelete {
			wg.Add(1)
			sem <- struct{}{}
			go func(key string) {
				defer wg.Done()
				defer func() { <-sem }()
				_, err := client.DeleteObject(context.Background(), &s3sdk.DeleteObjectInput{
					Bucket: awssdk.String(bucket),
					Key:    awssdk.String(key),
				})
				mu.Lock()
				if err != nil {
					failed[key] = err.Error()
				} else {
					ok++
				}
				mu.Unlock()
			}(k)
		}
		wg.Wait()
		return deleteDoneMsg{ok: ok, failed: failed}
	}
}

func (m browserModel) CapturingInput() bool {
	return m.yankPending || m.confirmDelete
}

func (m browserModel) HelpItems() []help.Section {
	return []help.Section{{
		Title: "S3 · object browser",
		Items: []help.Item{
			{Keys: "enter", Desc: "open folder / download file"},
			{Keys: "esc", Desc: "go up one prefix (or back to bucket list)"},
			{Keys: "space", Desc: "toggle file selection"},
			{Keys: "ctrl+d", Desc: "delete selected (or cursor file) with y/n confirm"},
			{Keys: "ctrl+u", Desc: "upload files into current prefix"},
			{Keys: "y", Desc: "yank menu: u=s3://, h=https, k=key, b=bucket"},
			{Keys: "r", Desc: "refresh prefix"},
			{Keys: "↑/↓ j/k", Desc: "move cursor"},
		},
	}}
}

func (m browserModel) selectedEntry() *entry {
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
	if m.deleting {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("deleting..."))
	}
	body := m.table.View()
	if len(m.entries) == 0 {
		body = mutedStyle.Render("(empty)")
	}
	notes := []string{}
	if m.truncated {
		notes = append(notes, mutedStyle.Render(fmt.Sprintf("showing first %d entries; refine the prefix to see more", objectsPageMax)))
	}
	if len(m.selected) > 0 && !m.confirmDelete {
		notes = append(notes, mutedStyle.Render(fmt.Sprintf("%d selected (space toggles, ctrl+d deletes)", len(m.selected))))
	}
	if m.status != "" {
		notes = append(notes, mutedStyle.Render(m.status))
	}
	help := mutedStyle.Render("enter: open/download · space: select · ctrl+d: delete · ctrl+u: upload · y u/h/k/b: yank · r: refresh · esc: up/back")

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

func buildEntryRows(entries []entry, selected map[string]bool) []datatable.Row {
	rows := make([]datatable.Row, len(entries))
	for i, e := range entries {
		name := e.name
		size := "-"
		modified := "-"
		if e.isFolder {
			size = "<prefix>"
			name = folderStyle.Render(name)
		} else {
			size = humanBytes(e.size)
			modified = e.modified
			if selected[e.name] {
				name = queuedFileStyle.Render("✓ " + name)
			} else {
				name = "  " + name // keep alignment with selected rows
			}
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

var _ = s3types.Object{}
