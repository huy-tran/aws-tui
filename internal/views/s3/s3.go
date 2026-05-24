package s3

import (
	"context"
	"encoding/gob"
	"fmt"
	"strings"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const bucketsCacheTTL = 5 * time.Minute

func init() { gob.Register([]Bucket(nil)) }
const regionResolveConcurrency = 10

type Bucket struct {
	Name    string
	Region  string
	Created string
}

type (
	bucketsLoadedMsg    struct{ items []Bucket }
	bucketRegionsMsg    struct{ regions map[string]string }
	errMsg              struct{ err error }
)

type Model struct {
	ctx     *awspkg.Context
	store   *state.Store
	table   datatable.Model
	filter  textinput.Model
	filterMode bool
	buckets   []Bucket
	displayed []Bucket
	loading   bool
	err       error
	status    string
	yankPending bool

	loader     loader.Model
	lastLoaded time.Time

	width, height int
}

func New(ctx *awspkg.Context, store *state.Store) Model {
	cols := []datatable.Column{
		{Title: "Bucket", Flex: true},
		{Title: "Region"},
		{Title: "Created"},
	}
	t := datatable.New(cols)
	t.SetHeight(20)

	ti := textinput.New()
	ti.Placeholder = "filter buckets"
	ti.CharLimit = 64
	ti.Prompt = "/ "

	return Model{ctx: ctx, store: store, table: t, filter: ti, loader: loader.New(), loading: true}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadCmd(false))
}

func (m Model) loadCmd(force bool) tea.Cmd {
	const key = "s3:buckets"
	ctx := m.ctx
	store := m.store
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]Bucket); ok {
					return bucketsLoadedMsg{items: items}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.S3()
		out, err := client.ListBuckets(context.Background(), &s3sdk.ListBucketsInput{})
		if err != nil {
			return errMsg{err: err}
		}
		items := make([]Bucket, 0, len(out.Buckets))
		for _, b := range out.Buckets {
			name := awssdk.ToString(b.Name)
			created := ""
			if b.CreationDate != nil {
				created = b.CreationDate.Format("2006-01-02")
			}
			items = append(items, Bucket{
				Name:    name,
				Region:  store.GetBucketRegion(name),
				Created: created,
			})
		}
		ctx.Cache.Set(key, items, bucketsCacheTTL)
		return bucketsLoadedMsg{items: items}
	}
}

// loadRegionsCmd resolves bucket regions for any buckets we don't already
// have a region for (state cache miss). Up to N parallel GetBucketLocation
// calls; result merged in via bucketRegionsMsg.
func (m Model) loadRegionsCmd() tea.Cmd {
	var needed []string
	for _, b := range m.buckets {
		if b.Region == "" {
			needed = append(needed, b.Name)
		}
	}
	if len(needed) == 0 {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.S3()
		out := make(map[string]string, len(needed))
		sem := make(chan struct{}, regionResolveConcurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, name := range needed {
			wg.Add(1)
			sem <- struct{}{}
			go func(n string) {
				defer wg.Done()
				defer func() { <-sem }()
				loc, err := client.GetBucketLocation(context.Background(),
					&s3sdk.GetBucketLocationInput{Bucket: awssdk.String(n)})
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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 4
		if h < 3 {
			h = 3
		}
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		return m, nil

	case bucketsLoadedMsg:
		m.loading = false
		m.buckets = msg.items
		m.lastLoaded = time.Now()
		m.applyFilter()
		return m, m.loadRegionsCmd()

	case bucketRegionsMsg:
		for i := range m.buckets {
			if r, ok := msg.regions[m.buckets[i].Name]; ok {
				m.buckets[i].Region = r
				m.store.SetBucketRegion(m.buckets[i].Name, r)
			}
		}
		_ = m.store.Save()
		m.applyFilter()
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

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filterMode {
		switch msg.String() {
		case "esc":
			m.filterMode = false
			m.filter.SetValue("")
			m.filter.Blur()
			m.applyFilter()
			return m, nil
		case "enter":
			m.filterMode = false
			m.filter.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
		return m, cmd
	}

	if m.yankPending {
		m.yankPending = false
		b := m.selected()
		if b == nil {
			m.status = "no bucket selected"
			return m, nil
		}
		switch msg.String() {
		case "b":
			m.status = doYank(b.Name, "bucket name")
		case "u":
			m.status = doYank("s3://"+b.Name+"/", "s3:// URI")
		case "h":
			region := b.Region
			if region == "" {
				region = m.ctx.Region
			}
			m.status = doYank(fmt.Sprintf("https://%s.s3.%s.amazonaws.com/", b.Name, region), "https URL")
		default:
			m.status = ""
		}
		return m, nil
	}

	switch msg.String() {
	case "r":
		m.ctx.Cache.Invalidate("s3:buckets")
		m.loading = true
		m.err = nil
		return m, tea.Batch(m.loader.Tick(), m.loadCmd(true))
	case "/":
		m.filterMode = true
		m.filter.Focus()
		return m, textinput.Blink
	case "y":
		m.yankPending = true
		m.status = "yank: b=bucket · u=s3:// URI · h=https URL"
		return m, nil
	case "enter":
		if b := m.selected(); b != nil {
			return m, nav.PushView(newBrowser(m.ctx, m.store, *b))
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.displayed = m.buckets
	} else {
		out := make([]Bucket, 0, len(m.buckets))
		for _, b := range m.buckets {
			if strings.Contains(strings.ToLower(b.Name), q) {
				out = append(out, b)
			}
		}
		m.displayed = out
	}
	m.table.SetRows(buildBucketRows(m.displayed))
}

func (m Model) CapturingInput() bool {
	return m.filterMode || m.yankPending
}

func (m Model) BookmarkCurrent() (string, string, bool) {
	b := m.selected()
	if b == nil {
		return "", "", false
	}
	return b.Name, b.Name, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	m.applyFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	return statusbar.Snapshot{
		Items:      len(m.displayed),
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	if m.filterMode {
		return []help.Section{{
			Title: "S3 · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match bucket name"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "S3 buckets",
		Items: []help.Item{
			{Keys: "enter", Desc: "open bucket browser"},
			{Keys: "y", Desc: "yank menu: b=name, u=s3:// URI, h=https URL"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
			{Keys: "↑/↓ j/k", Desc: "move cursor"},
		},
	}}
}

func (m Model) selected() *Bucket {
	if m.loading || len(m.displayed) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.displayed) {
		return nil
	}
	return &m.displayed[idx]
}

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	if m.loading {
		return m.loader.Render("loading buckets...")
	}
	if len(m.buckets) == 0 {
		return mutedStyle.Render("No S3 buckets in this account.")
	}

	header := headerStyle.Render(fmt.Sprintf("S3 Buckets (%d)", len(m.buckets)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}

	body := m.table.View()
	if len(m.displayed) == 0 {
		body = mutedStyle.Render("No buckets match filter.")
	}

	help := mutedStyle.Render("enter: open · y u/h/b: yank · /: filter · r: refresh")
	parts := []string{header, filterLine, body, help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func buildBucketRows(items []Bucket) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, b := range items {
		region := b.Region
		if region == "" {
			region = "…"
		}
		rows[i] = datatable.Row{b.Name, region, b.Created}
	}
	return rows
}

func doYank(value, label string) string {
	if value == "" {
		return "no " + label + " to copy"
	}
	if err := clipboard.WriteAll(value); err != nil {
		return "clipboard error: " + err.Error()
	}
	return "copied " + label
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
