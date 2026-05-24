package cloudwatch

import (
	"context"
	"encoding/gob"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	logs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/events"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const groupsPageSize = 50

func init() {
	gob.Register([]LogGroup(nil))
	gob.Register([]LogStream(nil))
}
const groupsCacheTTL = 60 * time.Second
const streamsCacheTTL = 30 * time.Second

type LogGroup struct {
	Name      string
	ARN       string
	Retention string
	Size      int64
}

type LogStream struct {
	Name       string
	LastEvent  time.Time
}

type LogEvent struct {
	Time    time.Time
	Stream  string
	Message string
}

type (
	groupsLoadedMsg  struct {
		items []LogGroup
		token string // next pagination token, empty if no more
	}
	groupsAppendedMsg struct {
		items []LogGroup
		token string
	}
	streamsLoadedMsg struct {
		group string
		items []LogStream
	}
	searchDoneMsg struct {
		events []LogEvent
		more   bool
	}
	errMsg struct{ err error }

	// liveTail messages cross from the pump goroutine into the
	// bubble-tea program via events.Send.
	liveTailStartedMsg struct{}
	liveTailLineMsg    struct{ ev LogEvent }
	liveTailEndedMsg   struct {
		err    error
		reason string
	}
)

// maxTailLines caps the in-memory ring buffer so a long-running tail
// doesn't grow unboundedly. Older lines drop off the front first.
const maxTailLines = 5000

type mode int

const (
	modeGroups mode = iota
	modeStreams
	modeSearch
	modeResults
	modeLiveTail
)

type timeRange int

const (
	range1h timeRange = iota
	range24h
	range7d
)

type Model struct {
	ctx     *awspkg.Context
	mode    mode
	width   int
	height  int

	groups       []LogGroup
	groupsFilt   []LogGroup
	groupsToken  string
	groupsTable  datatable.Model
	groupsLoading bool

	filter      textinput.Model
	filterMode  bool

	target  LogGroup
	streams []LogStream
	stTable datatable.Model

	pattern      textinput.Model
	selectedRange timeRange

	viewport     viewport.Model
	results      []LogEvent
	resultsTitle string
	searchLoading bool

	// live tail state. tailCancel is non-nil only while a pump goroutine
	// is running; calling it tears the stream down. parked holds lines
	// received while the user paused auto-scroll so they replay on
	// resume.
	tailViewport     viewport.Model
	tailLines        []LogEvent
	tailParked       []LogEvent
	tailFilter       *regexp.Regexp
	tailFilterRaw    string
	tailFilterMode   bool
	tailFilterInput  textinput.Model
	tailPaused       bool
	tailStarted      time.Time
	tailReason       string
	tailCancel       context.CancelFunc

	loader     loader.Model
	lastLoaded time.Time

	err    error
	status string
}

func New(ctx *awspkg.Context) Model {
	gCols := []datatable.Column{
		{Title: "Log Group", Flex: true},
		{Title: "Retention"},
		{Title: "Size"},
	}
	gT := datatable.New(gCols)
	gT.SetHeight(15)

	sCols := []datatable.Column{
		{Title: "Stream Name", Flex: true},
		{Title: "Last Event"},
	}
	sT := datatable.New(sCols)
	sT.SetHeight(15)

	flt := textinput.New()
	flt.Placeholder = "filter groups"
	flt.CharLimit = 64
	flt.Prompt = "/ "

	pat := textinput.New()
	pat.Placeholder = "e.g. ERROR or \"timeout\""
	pat.CharLimit = 1024
	pat.Width = 50

	vp := viewport.New(0, 0)
	tailVP := viewport.New(0, 0)
	tailFlt := textinput.New()
	tailFlt.Placeholder = "regex filter"
	tailFlt.CharLimit = 256
	tailFlt.Prompt = "/ "

	return Model{
		ctx:             ctx,
		groupsTable:     gT,
		stTable:         sT,
		filter:          flt,
		pattern:         pat,
		viewport:        vp,
		tailViewport:    tailVP,
		tailFilterInput: tailFlt,
		selectedRange:   range1h,
		loader:          loader.New(),
		groupsLoading:   true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadGroupsCmd("", false))
}

func (m Model) anyLoading() bool {
	return m.groupsLoading || m.searchLoading
}

func (m Model) loadGroupsCmd(token string, force bool) tea.Cmd {
	key := "logs:groups:" + m.ctx.Region
	ctx := m.ctx
	return func() tea.Msg {
		if token == "" && !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]LogGroup); ok {
					return groupsLoadedMsg{items: items}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.Logs()
		input := &logs.DescribeLogGroupsInput{
			Limit: awssdk.Int32(int32(groupsPageSize)),
		}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		out, err := client.DescribeLogGroups(context.Background(), input)
		if err != nil {
			return errMsg{err: err}
		}
		items := make([]LogGroup, 0, len(out.LogGroups))
		for _, g := range out.LogGroups {
			items = append(items, LogGroup{
				Name:      awssdk.ToString(g.LogGroupName),
				ARN:       strings.TrimSuffix(awssdk.ToString(g.Arn), ":*"),
				Retention: retentionLabel(g.RetentionInDays),
				Size:      derefInt64(g.StoredBytes),
			})
		}
		next := awssdk.ToString(out.NextToken)
		if token == "" {
			ctx.Cache.Set(key, items, groupsCacheTTL)
			return groupsLoadedMsg{items: items, token: next}
		}
		return groupsAppendedMsg{items: items, token: next}
	}
}

func (m Model) loadStreamsCmd(group string) tea.Cmd {
	key := "logs:streams:" + group
	ctx := m.ctx
	return func() tea.Msg {
		if cached, ok := ctx.Cache.Get(key); ok {
			if items, ok := cached.([]LogStream); ok {
				return streamsLoadedMsg{group: group, items: items}
			}
		}
		client := ctx.Logs()
		out, err := client.DescribeLogStreams(context.Background(), &logs.DescribeLogStreamsInput{
			LogGroupName: awssdk.String(group),
			OrderBy:      "LastEventTime",
			Descending:   awssdk.Bool(true),
			Limit:        awssdk.Int32(50),
		})
		if err != nil {
			return errMsg{err: err}
		}
		items := make([]LogStream, 0, len(out.LogStreams))
		for _, s := range out.LogStreams {
			ls := LogStream{Name: awssdk.ToString(s.LogStreamName)}
			if s.LastEventTimestamp != nil {
				ls.LastEvent = time.UnixMilli(*s.LastEventTimestamp).Local()
			}
			items = append(items, ls)
		}
		ctx.Cache.Set(key, items, streamsCacheTTL)
		return streamsLoadedMsg{group: group, items: items}
	}
}

func (m Model) searchCmd(group, pattern string, tr timeRange) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.Logs()
		now := time.Now()
		var start time.Time
		switch tr {
		case range1h:
			start = now.Add(-1 * time.Hour)
		case range24h:
			start = now.Add(-24 * time.Hour)
		case range7d:
			start = now.Add(-7 * 24 * time.Hour)
		}
		input := &logs.FilterLogEventsInput{
			LogGroupName: awssdk.String(group),
			StartTime:    awssdk.Int64(start.UnixMilli()),
			EndTime:      awssdk.Int64(now.UnixMilli()),
			Limit:        awssdk.Int32(1000),
		}
		if strings.TrimSpace(pattern) != "" {
			input.FilterPattern = awssdk.String(pattern)
		}
		out, err := client.FilterLogEvents(context.Background(), input)
		if err != nil {
			return errMsg{err: err}
		}
		events := make([]LogEvent, 0, len(out.Events))
		for _, e := range out.Events {
			ts := time.Time{}
			if e.Timestamp != nil {
				ts = time.UnixMilli(*e.Timestamp).Local()
			}
			events = append(events, LogEvent{
				Time:    ts,
				Stream:  awssdk.ToString(e.LogStreamName),
				Message: awssdk.ToString(e.Message),
			})
		}
		sort.Slice(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
		more := awssdk.ToString(out.NextToken) != ""
		return searchDoneMsg{events: events, more: more}
	}
}

// updateLiveTailKeys handles keystrokes while modeLiveTail is active.
func (m Model) updateLiveTailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.tailFilterMode {
		switch msg.String() {
		case "esc":
			m.tailFilterMode = false
			m.tailFilterInput.SetValue("")
			m.tailFilterInput.Blur()
			m.tailFilter = nil
			m.tailFilterRaw = ""
			m.refreshTailViewport(true)
			return m, nil
		case "enter":
			m.tailFilterMode = false
			m.tailFilterInput.Blur()
			raw := m.tailFilterInput.Value()
			if raw == "" {
				m.tailFilter = nil
				m.tailFilterRaw = ""
			} else if re, err := regexp.Compile(raw); err == nil {
				m.tailFilter = re
				m.tailFilterRaw = raw
			} else {
				m.tailReason = "invalid regex: " + err.Error()
			}
			m.refreshTailViewport(true)
			return m, nil
		}
		var cmd tea.Cmd
		m.tailFilterInput, cmd = m.tailFilterInput.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc":
		if m.tailCancel != nil {
			m.tailCancel()
			m.tailCancel = nil
		}
		m.mode = modeStreams
		m.tailReason = ""
		return m, nil
	case "/":
		m.tailFilterMode = true
		m.tailFilterInput.Focus()
		return m, textinput.Blink
	case "p":
		m.tailPaused = !m.tailPaused
		if !m.tailPaused && len(m.tailParked) > 0 {
			m.tailLines = append(m.tailLines, m.tailParked...)
			if len(m.tailLines) > maxTailLines {
				m.tailLines = m.tailLines[len(m.tailLines)-maxTailLines:]
			}
			m.tailParked = nil
			m.refreshTailViewport(true)
		}
		return m, nil
	case "c":
		m.tailLines = nil
		m.tailParked = nil
		m.refreshTailViewport(true)
		return m, nil
	case "y":
		body := m.renderTailLines(m.visibleTailLines())
		if body != "" {
			m.status = doYank(body, "tail buffer")
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.tailViewport, cmd = m.tailViewport.Update(msg)
	return m, cmd
}

// startTailCmd kicks off the StartLiveTail pump goroutine. The goroutine
// owns the SDK stream and forwards each event into the program via
// events.Send; it exits when the context is cancelled (esc / mode change)
// or the server times out.
func (m *Model) startTailCmd(arn, name string) tea.Cmd {
	if arn == "" {
		// ARN may be missing from older cached entries; fall back to
		// constructing it from the name + region + account context.
		// Without the account id we can't, so just bail to a useful
		// reason and keep the view alive.
		m.tailReason = "tail unavailable: log group ARN missing (refresh the groups list with 'r')"
		return nil
	}
	if m.tailCancel != nil {
		m.tailCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.tailCancel = cancel
	ctxRef := m.ctx
	return func() tea.Msg {
		go pumpLiveTail(ctx, ctxRef, arn, name)
		return nil
	}
}

// pumpLiveTail runs the StartLiveTail event loop. Errors and the
// "session ended" condition both fire a liveTailEndedMsg back through
// the program; the message handler decides whether to reconnect.
func pumpLiveTail(ctx context.Context, awsCtx *awspkg.Context, arn, name string) {
	if err := awsCtx.Load(ctx); err != nil {
		events.Send(liveTailEndedMsg{err: err})
		return
	}
	client := awsCtx.Logs()
	out, err := client.StartLiveTail(ctx, &logs.StartLiveTailInput{
		LogGroupIdentifiers: []string{arn},
	})
	if err != nil {
		events.Send(liveTailEndedMsg{err: err})
		return
	}
	stream := out.GetStream()
	defer stream.Close()
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case *logstypes.StartLiveTailResponseStreamMemberSessionStart:
			events.Send(liveTailStartedMsg{})
		case *logstypes.StartLiveTailResponseStreamMemberSessionUpdate:
			for _, entry := range e.Value.SessionResults {
				ts := time.Time{}
				if entry.Timestamp != nil {
					ts = time.UnixMilli(*entry.Timestamp).Local()
				}
				events.Send(liveTailLineMsg{ev: LogEvent{
					Time:    ts,
					Stream:  awssdk.ToString(entry.LogStreamName),
					Message: awssdk.ToString(entry.Message),
				}})
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
	// Stream closed. If context is still alive it's a server-side end
	// (typically the 1h session limit); ask the view to reconnect.
	if ctx.Err() == nil {
		events.Send(liveTailEndedMsg{reason: "session limit, reconnecting"})
		return
	}
	events.Send(liveTailEndedMsg{err: stream.Err()})
}

// visibleTailLines returns the slice of tailLines that pass the active
// regex filter (or all of them when no filter is set).
func (m Model) visibleTailLines() []LogEvent {
	if m.tailFilter == nil {
		return m.tailLines
	}
	out := make([]LogEvent, 0, len(m.tailLines))
	for _, ev := range m.tailLines {
		if m.tailFilter.MatchString(ev.Message) {
			out = append(out, ev)
		}
	}
	return out
}

// refreshTailViewport rebuilds the viewport text from tailLines, optionally
// snapping to the bottom (auto-follow).
func (m *Model) refreshTailViewport(snapBottom bool) {
	body := m.renderTailLines(m.visibleTailLines())
	m.tailViewport.SetContent(body)
	if snapBottom && !m.tailPaused {
		m.tailViewport.GotoBottom()
	}
}

// renderTailLines formats a slice of events for the viewport. Mirrors
// renderEvents but is a separate fn so we can tweak the live-tail format
// later (eg highlight recent lines) without disturbing the search path.
func (m Model) renderTailLines(lines []LogEvent) string {
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range lines {
		sb.WriteString(fmt.Sprintf("%s [%s] %s\n",
			e.Time.Format("15:04:05"), shortStream(e.Stream), e.Message))
	}
	return sb.String()
}

func (m Model) tailCmd(group string) tea.Cmd {
	cmd := exec.Command("aws", "logs", "tail",
		"--profile", m.ctx.Profile,
		"--region", m.ctx.Region,
		"--follow",
		"--format", "short",
		group,
	)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return errMsg{err: fmt.Errorf("aws logs tail failed: %w", err)}
		}
		return nil
	})
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
		m.groupsTable.SetHeight(h)
		m.groupsTable.SetWidth(msg.Width)
		m.stTable.SetHeight(h)
		m.stTable.SetWidth(msg.Width)
		m.viewport.Width = msg.Width
		m.viewport.Height = h
		m.tailViewport.Width = msg.Width
		m.tailViewport.Height = h - 2 // reserve a row for the status banner
		return m, nil

	case groupsLoadedMsg:
		m.groupsLoading = false
		m.groups = msg.items
		m.groupsToken = msg.token
		m.lastLoaded = time.Now()
		m.applyGroupFilter()
		return m, nil

	case groupsAppendedMsg:
		m.groups = append(m.groups, msg.items...)
		m.groupsToken = msg.token
		m.applyGroupFilter()
		return m, nil

	case streamsLoadedMsg:
		m.streams = msg.items
		m.stTable.SetRows(buildStreamRows(m.streams))
		return m, nil

	case liveTailStartedMsg:
		m.tailStarted = time.Now()
		m.tailReason = ""
		return m, nil

	case liveTailLineMsg:
		// Drop oldest if we're at the cap.
		if m.tailPaused {
			m.tailParked = append(m.tailParked, msg.ev)
			if len(m.tailParked) > maxTailLines {
				m.tailParked = m.tailParked[len(m.tailParked)-maxTailLines:]
			}
		} else {
			m.tailLines = append(m.tailLines, msg.ev)
			if len(m.tailLines) > maxTailLines {
				m.tailLines = m.tailLines[len(m.tailLines)-maxTailLines:]
			}
			m.refreshTailViewport(true)
		}
		return m, nil

	case liveTailEndedMsg:
		if msg.err != nil && msg.reason == "" {
			m.tailReason = "stream error: " + msg.err.Error()
		} else if msg.reason != "" {
			m.tailReason = msg.reason
		}
		// If the user is still in modeLiveTail when this fires, restart
		// the pump (covers the 1h session limit reconnect).
		if m.mode == modeLiveTail && msg.reason == "session limit, reconnecting" {
			return m, m.startTailCmd(m.target.ARN, m.target.Name)
		}
		return m, nil

	case searchDoneMsg:
		m.searchLoading = false
		m.results = msg.events
		m.viewport.SetContent(renderEvents(m.results))
		m.viewport.GotoTop()
		more := ""
		if msg.more {
			more = " (more available - narrow time/pattern)"
		}
		m.resultsTitle = fmt.Sprintf("%s · %s · %d matches%s",
			m.target.Name, rangeLabel(m.selectedRange), len(m.results), more)
		return m, nil

	case errMsg:
		m.groupsLoading = false
		m.searchLoading = false
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if !m.anyLoading() {
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
	if m.filterMode && m.mode == modeGroups {
		switch msg.String() {
		case "esc":
			m.filterMode = false
			m.filter.SetValue("")
			m.filter.Blur()
			m.applyGroupFilter()
			return m, nil
		case "enter":
			m.filterMode = false
			m.filter.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyGroupFilter()
		return m, cmd
	}

	switch m.mode {
	case modeStreams:
		switch msg.String() {
		case "esc":
			m.mode = modeGroups
			return m, nil
		case "r":
			m.ctx.Cache.Invalidate("logs:streams:" + m.target.Name)
			return m, m.loadStreamsCmd(m.target.Name)
		}
		var cmd tea.Cmd
		m.stTable, cmd = m.stTable.Update(msg)
		return m, cmd

	case modeSearch:
		switch msg.String() {
		case "esc":
			m.mode = modeGroups
			m.pattern.Blur()
			return m, nil
		case "enter":
			m.mode = modeResults
			m.results = nil
			m.searchLoading = true
			m.viewport.SetContent("")
			return m, tea.Batch(m.loader.Tick(), m.searchCmd(m.target.Name, m.pattern.Value(), m.selectedRange))
		case "tab":
			m.selectedRange = (m.selectedRange + 1) % 3
			return m, nil
		case "shift+tab":
			m.selectedRange = (m.selectedRange + 2) % 3
			return m, nil
		}
		var cmd tea.Cmd
		m.pattern, cmd = m.pattern.Update(msg)
		return m, cmd

	case modeResults:
		switch msg.String() {
		case "esc":
			m.mode = modeSearch
			return m, nil
		case "y":
			if len(m.results) > 0 {
				m.status = doYank(renderEvents(m.results), "all matches")
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case modeLiveTail:
		return m.updateLiveTailKeys(msg)

	default: // modeGroups
		switch msg.String() {
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "r":
			m.ctx.Cache.Invalidate("logs:groups:" + m.ctx.Region)
			m.groupsLoading = true
			return m, tea.Batch(m.loader.Tick(), m.loadGroupsCmd("", true))
		case "m":
			if m.groupsToken != "" {
				return m, m.loadGroupsCmd(m.groupsToken, true)
			}
		case "enter":
			if g := m.selectedGroup(); g != nil {
				m.target = *g
				m.mode = modeStreams
				return m, m.loadStreamsCmd(g.Name)
			}
		case "t":
			if g := m.selectedGroup(); g != nil {
				m.target = *g
				m.mode = modeLiveTail
				m.tailLines = nil
				m.tailParked = nil
				m.tailFilter = nil
				m.tailFilterRaw = ""
				m.tailReason = "connecting..."
				m.tailViewport.SetContent("")
				return m, m.startTailCmd(g.ARN, g.Name)
			}
		case "T":
			// Fallback shell-out to `aws logs tail --follow` for sessions
			// longer than the in-TUI 1h window can handle.
			if g := m.selectedGroup(); g != nil {
				m.target = *g
				return m, m.tailCmd(g.Name)
			}
		case "s":
			if g := m.selectedGroup(); g != nil {
				m.target = *g
				m.mode = modeSearch
				m.pattern.SetValue("")
				m.pattern.Focus()
				return m, textinput.Blink
			}
		}
		var cmd tea.Cmd
		m.groupsTable, cmd = m.groupsTable.Update(msg)
		return m, cmd
	}
}

func (m Model) CapturingInput() bool {
	if m.filterMode && m.mode == modeGroups {
		return true
	}
	if m.mode == modeLiveTail && m.tailFilterMode {
		return true
	}
	return m.mode == modeSearch
}

func (m Model) InSubnav() bool {
	return m.mode != modeGroups
}

func (m Model) BookmarkCurrent() (string, string, bool) {
	g := m.selectedGroup()
	if g == nil {
		return "", "", false
	}
	return g.Name, g.Name, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	m.applyGroupFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	items := len(m.groupsFilt)
	switch m.mode {
	case modeStreams:
		items = len(m.streams)
	case modeResults:
		items = len(m.results)
	}
	return statusbar.Snapshot{
		Items:      items,
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	switch m.mode {
	case modeStreams:
		return []help.Section{{
			Title: "CloudWatch · streams",
			Items: []help.Item{
				{Keys: "r", Desc: "refresh"},
				{Keys: "esc", Desc: "back to log groups"},
			},
		}}
	case modeSearch:
		return []help.Section{{
			Title: "CloudWatch · search",
			Items: []help.Item{
				{Keys: "type", Desc: "FilterLogEvents pattern"},
				{Keys: "tab / S-tab", Desc: "change time range"},
				{Keys: "enter", Desc: "run search"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	case modeResults:
		return []help.Section{{
			Title: "CloudWatch · search results",
			Items: []help.Item{
				{Keys: "↑/↓", Desc: "scroll"},
				{Keys: "y", Desc: "yank all matches"},
				{Keys: "esc", Desc: "back to search"},
			},
		}}
	case modeLiveTail:
		return []help.Section{{
			Title: "CloudWatch · live tail",
			Items: []help.Item{
				{Keys: "/", Desc: "regex filter (local; stream keeps streaming)"},
				{Keys: "p", Desc: "pause / resume auto-scroll"},
				{Keys: "c", Desc: "clear visible buffer"},
				{Keys: "y", Desc: "yank visible (filtered) lines"},
				{Keys: "esc", Desc: "stop and back to streams"},
			},
		}}
	}
	if m.filterMode {
		return []help.Section{{
			Title: "CloudWatch · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match log group name"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "CloudWatch log groups",
		Items: []help.Item{
			{Keys: "enter", Desc: "open streams"},
			{Keys: "t", Desc: "tail (shells out to aws logs tail)"},
			{Keys: "s", Desc: "search via FilterLogEvents"},
			{Keys: "m", Desc: "load next 50 groups"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
		},
	}}
}

func (m *Model) applyGroupFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.groupsFilt = m.groups
	} else {
		out := make([]LogGroup, 0, len(m.groups))
		for _, g := range m.groups {
			if strings.Contains(strings.ToLower(g.Name), q) {
				out = append(out, g)
			}
		}
		m.groupsFilt = out
	}
	m.groupsTable.SetRows(buildGroupRows(m.groupsFilt))
}

func (m Model) selectedGroup() *LogGroup {
	if m.groupsLoading || len(m.groupsFilt) == 0 {
		return nil
	}
	idx := m.groupsTable.Cursor()
	if idx < 0 || idx >= len(m.groupsFilt) {
		return nil
	}
	return &m.groupsFilt[idx]
}

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	switch m.mode {
	case modeStreams:
		title := headerStyle.Render("Streams: " + m.target.Name)
		help := mutedStyle.Render("r: refresh · esc: back")
		body := m.stTable.View()
		if len(m.streams) == 0 {
			body = mutedStyle.Render("(no streams)")
		}
		return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)
	case modeSearch:
		title := headerStyle.Render("Search: " + m.target.Name)
		ranges := renderRanges(m.selectedRange)
		help := mutedStyle.Render("tab: change range · enter: search · esc: cancel")
		return lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			"Pattern:", m.pattern.View(),
			"",
			"Time range: "+ranges,
			"",
			help,
		)
	case modeResults:
		if m.searchLoading {
			title := headerStyle.Render(m.target.Name)
			return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("searching..."))
		}
		title := headerStyle.Render(m.resultsTitle)
		help := mutedStyle.Render("↑/↓ scroll · y: yank all · esc: back")
		status := ""
		if m.status != "" {
			status = mutedStyle.Render(m.status)
		}
		return lipgloss.JoinVertical(lipgloss.Left, title, "", m.viewport.View(), status, help)

	case modeLiveTail:
		title := headerStyle.Render("Tail: " + m.target.Name)
		// Status banner: live / paused, line count, started-at, optional reason
		state := "live"
		if m.tailPaused {
			state = "paused"
		}
		startedAt := "—"
		if !m.tailStarted.IsZero() {
			startedAt = m.tailStarted.Format("15:04:05")
		}
		bannerParts := []string{state, fmt.Sprintf("%d lines", len(m.tailLines)), "started " + startedAt}
		if m.tailFilterRaw != "" {
			bannerParts = append(bannerParts, "filter "+m.tailFilterRaw)
		}
		if m.tailReason != "" {
			bannerParts = append(bannerParts, m.tailReason)
		}
		banner := mutedStyle.Render(strings.Join(bannerParts, " · "))
		var filterRow string
		if m.tailFilterMode {
			filterRow = m.tailFilterInput.View()
		}
		help := mutedStyle.Render("/: filter (regex) · p: pause/resume · c: clear · y: yank visible · esc: stop")
		body := m.tailViewport.View()
		if len(m.tailLines) == 0 {
			body = mutedStyle.Render("(waiting for events...)")
		}
		parts := []string{title, banner}
		if filterRow != "" {
			parts = append(parts, filterRow)
		}
		parts = append(parts, body, help)
		if m.status != "" {
			parts = append(parts, mutedStyle.Render(m.status))
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}

	if m.groupsLoading {
		return m.loader.Render("loading log groups...")
	}
	if len(m.groups) == 0 {
		return mutedStyle.Render("No log groups in this region.")
	}
	header := headerStyle.Render(fmt.Sprintf("CloudWatch Log Groups (%d loaded)", len(m.groups)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.groupsTable.View()
	if len(m.groupsFilt) == 0 {
		body = mutedStyle.Render("No groups match filter.")
	}
	help := mutedStyle.Render("enter: streams · t: tail (shells out) · s: search · /: filter · r: refresh")
	parts := []string{header, filterLine, body, help}
	if m.groupsToken != "" {
		parts = append(parts, mutedStyle.Render("m: load next 50"))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func buildGroupRows(items []LogGroup) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, g := range items {
		rows[i] = datatable.Row{g.Name, g.Retention, humanBytes(g.Size)}
	}
	return rows
}

func buildStreamRows(items []LogStream) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, s := range items {
		last := "-"
		if !s.LastEvent.IsZero() {
			last = s.LastEvent.Format("2006-01-02 15:04:05")
		}
		rows[i] = datatable.Row{s.Name, last}
	}
	return rows
}

func renderEvents(events []LogEvent) string {
	if len(events) == 0 {
		return mutedStyle.Render("(no matches)")
	}
	var sb strings.Builder
	for _, e := range events {
		sb.WriteString(fmt.Sprintf("%s [%s] %s\n",
			e.Time.Format("15:04:05"), shortStream(e.Stream), e.Message))
	}
	return sb.String()
}

func shortStream(s string) string {
	if len(s) > 14 {
		return s[:14]
	}
	return s
}

func renderRanges(active timeRange) string {
	labels := []string{"last 1h", "last 24h", "last 7d"}
	var parts []string
	for i, l := range labels {
		if timeRange(i) == active {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Underline(true).Render("["+l+"]"))
		} else {
			parts = append(parts, mutedStyle.Render(" "+l+" "))
		}
	}
	return strings.Join(parts, "  ")
}

func rangeLabel(tr timeRange) string {
	switch tr {
	case range1h:
		return "last 1h"
	case range24h:
		return "last 24h"
	case range7d:
		return "last 7d"
	}
	return ""
}

func doYank(value, label string) string {
	if err := clipboard.WriteAll(value); err != nil {
		return "clipboard error: " + err.Error()
	}
	return "copied " + label
}

func retentionLabel(days *int32) string {
	if days == nil {
		return "Never"
	}
	if *days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", *days)
}

func humanBytes(b int64) string {
	if b == 0 {
		return "—"
	}
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

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
