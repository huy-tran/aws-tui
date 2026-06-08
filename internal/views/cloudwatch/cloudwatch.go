package cloudwatch

import (
	"context"
	"encoding/gob"
	"encoding/json"
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
	"github.com/charmbracelet/x/ansi"

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
	streamEventsLoadedMsg struct {
		stream string
		events []LogEvent
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
	modeStreamEvents
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

	streamTarget        string
	streamEvents        []LogEvent
	streamEventsLoading bool

	// In-page search state for modeStreamEvents. findMatches stores the
	// visual line offset where each matching event starts in the wrapped
	// viewport content; findCursor is the index into that slice.
	findInput   textinput.Model
	findMode    bool
	findQuery   string
	findMatches []int
	findCursor  int

	pattern      textinput.Model
	selectedRange timeRange

	viewport     viewport.Model
	results      []LogEvent
	resultsTitle string
	searchLoading bool

	// jsonPretty toggles JSON-aware reformatting in stream-events, search
	// results, and live tail rendering. Messages whose trimmed form starts
	// with '{' or '[' get json.MarshalIndent'd; everything else stays as-is.
	jsonPretty bool

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

	findFlt := textinput.New()
	findFlt.Placeholder = "find in events"
	findFlt.CharLimit = 256
	findFlt.Prompt = "/ "

	return Model{
		ctx:             ctx,
		groupsTable:     gT,
		stTable:         sT,
		filter:          flt,
		pattern:         pat,
		viewport:        vp,
		tailViewport:    tailVP,
		tailFilterInput: tailFlt,
		findInput:       findFlt,
		selectedRange:   range1h,
		loader:          loader.New(),
		groupsLoading:   true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadGroupsCmd("", false))
}

func (m Model) anyLoading() bool {
	return m.groupsLoading || m.searchLoading || m.streamEventsLoading
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

// streamEventsWant is how many of the most recent events we try to collect
// for a single stream view.
const streamEventsWant = 1000

func (m Model) loadStreamEventsCmd(group, stream string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.Logs()
		// GetLogEvents can return an empty or only partially full page even
		// when the stream has plenty of events - an empty page does NOT mean
		// pagination is done. We must keep following nextBackwardToken (which
		// walks toward older events) until we've gathered enough or the token
		// stops advancing. Without this, streams whose first page comes back
		// empty render as "0 events / (no matches)" even though the console
		// shows them. See:
		// https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html
		events := make([]LogEvent, 0, streamEventsWant)
		var token *string
		// Bound the loop so a pathological stream (e.g. expired logs that keep
		// returning fresh tokens with no events) can't spin forever.
		for i := 0; i < 100; i++ {
			out, err := client.GetLogEvents(context.Background(), &logs.GetLogEventsInput{
				LogGroupName:  awssdk.String(group),
				LogStreamName: awssdk.String(stream),
				Limit:         awssdk.Int32(int32(streamEventsWant)),
				StartFromHead: awssdk.Bool(false),
				NextToken:     token,
			})
			if err != nil {
				return errMsg{err: err}
			}
			for _, e := range out.Events {
				ts := time.Time{}
				if e.Timestamp != nil {
					ts = time.UnixMilli(*e.Timestamp).Local()
				}
				events = append(events, LogEvent{
					Time:    ts,
					Stream:  stream,
					Message: awssdk.ToString(e.Message),
				})
			}
			if len(events) >= streamEventsWant {
				break
			}
			next := out.NextBackwardToken
			// A token equal to the one we sent (or a nil token) means there
			// are no older events to fetch - we've reached the start.
			if next == nil || (token != nil && awssdk.ToString(next) == awssdk.ToString(token)) {
				break
			}
			token = next
		}
		sort.Slice(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
		return streamEventsLoadedMsg{stream: stream, events: events}
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
	case "J":
		m.jsonPretty = !m.jsonPretty
		m.refreshTailViewport(false)
		return m, nil
	case "y":
		body := m.renderTailLines(m.visibleTailLines(), 0, false)
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
	body := m.renderTailLines(m.visibleTailLines(), m.tailViewport.Width, true)
	m.tailViewport.SetContent(body)
	if snapBottom && !m.tailPaused {
		m.tailViewport.GotoBottom()
	}
}

// renderTailLines formats a slice of events for the viewport. Mirrors
// renderEvents but is a separate fn so we can tweak the live-tail format
// later (eg highlight recent lines) without disturbing the search path.
func (m Model) renderTailLines(lines []LogEvent, width int, colorize bool) string {
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range lines {
		sb.WriteString(wrapLine(formatLogLine(e, colorize, nil, m.jsonPretty), width))
		sb.WriteString("\n")
	}
	return sb.String()
}

// tryPrettyJSON returns s reformatted with json.MarshalIndent if it parses
// as a JSON object or array. Anything else (plain text, partial JSON,
// numeric/string scalars) is returned unchanged. Cheap fail path: we only
// hand the parser strings that begin with '{' or '[' after trimming.
func tryPrettyJSON(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 2 {
		return s
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return s
	}
	var data interface{}
	if err := json.Unmarshal([]byte(trimmed), &data); err != nil {
		return s
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return s
	}
	return string(out)
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
		h := msg.Height - 6
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
		// Re-wrap any displayed log content to the new width.
		switch m.mode {
		case modeStreamEvents:
			m.viewport.Height = m.streamEventsViewportHeight()
			if len(m.streamEvents) > 0 {
				if m.findQuery != "" {
					m.applyStreamFind()
				} else {
					m.viewport.SetContent(renderEvents(m.streamEvents, m.viewport.Width, true, m.jsonPretty))
				}
			}
		case modeResults:
			if len(m.results) > 0 {
				m.viewport.SetContent(renderEvents(m.results, m.viewport.Width, true, m.jsonPretty))
			}
		case modeLiveTail:
			m.refreshTailViewport(false)
		}
		return m, nil

	case groupsLoadedMsg:
		m.groupsLoading = false
		m.groups = msg.items
		m.groupsToken = msg.token
		m.lastLoaded = time.Now()
		m.applyGroupFilter()
		if m.groupsToken != "" {
			return m, m.loadGroupsCmd(m.groupsToken, true)
		}
		m.ctx.Cache.Set("logs:groups:"+m.ctx.Region, m.groups, groupsCacheTTL)
		return m, nil

	case groupsAppendedMsg:
		m.groups = append(m.groups, msg.items...)
		m.groupsToken = msg.token
		m.applyGroupFilter()
		if m.groupsToken != "" {
			return m, m.loadGroupsCmd(m.groupsToken, true)
		}
		m.ctx.Cache.Set("logs:groups:"+m.ctx.Region, m.groups, groupsCacheTTL)
		return m, nil

	case streamEventsLoadedMsg:
		m.streamEventsLoading = false
		m.streamEvents = msg.events
		m.viewport.Height = m.streamEventsViewportHeight()
		m.viewport.SetContent(renderEvents(m.streamEvents, m.viewport.Width, true, m.jsonPretty))
		m.viewport.GotoBottom()
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
		// Results use the generic viewport height; reset it in case we are
		// arriving from the taller stream-events layout.
		if h := m.height - 6; h >= 3 {
			m.viewport.Height = h
		}
		m.viewport.SetContent(renderEvents(m.results, m.viewport.Width, true, m.jsonPretty))
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
		case "enter":
			if s := m.selectedStream(); s != nil {
				m.streamTarget = s.Name
				m.mode = modeStreamEvents
				m.streamEvents = nil
				m.streamEventsLoading = true
				m.viewport.SetContent("")
				m.resetFind()
				return m, tea.Batch(m.loader.Tick(), m.loadStreamEventsCmd(m.target.Name, s.Name))
			}
		}
		var cmd tea.Cmd
		m.stTable, cmd = m.stTable.Update(msg)
		return m, cmd

	case modeStreamEvents:
		if m.findMode {
			switch msg.String() {
			case "esc":
				m.findMode = false
				m.findInput.Blur()
				m.findInput.SetValue("")
				m.findQuery = ""
				m.findMatches = nil
				m.findCursor = 0
				m.viewport.SetContent(renderEvents(m.streamEvents, m.viewport.Width, true, m.jsonPretty))
				return m, nil
			case "enter":
				m.findMode = false
				m.findInput.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.findInput, cmd = m.findInput.Update(msg)
			m.applyStreamFind()
			return m, cmd
		}
		switch msg.String() {
		case "esc":
			if m.findQuery != "" {
				m.resetFind()
				m.viewport.SetContent(renderEvents(m.streamEvents, m.viewport.Width, true, m.jsonPretty))
				return m, nil
			}
			m.mode = modeStreams
			return m, nil
		case "/":
			m.findMode = true
			m.findInput.Focus()
			return m, textinput.Blink
		case "n":
			if len(m.findMatches) > 0 {
				m.findCursor = (m.findCursor + 1) % len(m.findMatches)
				m.viewport.SetYOffset(m.findMatches[m.findCursor])
			}
			return m, nil
		case "N":
			if len(m.findMatches) > 0 {
				m.findCursor = (m.findCursor - 1 + len(m.findMatches)) % len(m.findMatches)
				m.viewport.SetYOffset(m.findMatches[m.findCursor])
			}
			return m, nil
		case "r":
			m.resetFind()
			m.streamEventsLoading = true
			m.viewport.SetContent("")
			return m, tea.Batch(m.loader.Tick(), m.loadStreamEventsCmd(m.target.Name, m.streamTarget))
		case "J":
			m.jsonPretty = !m.jsonPretty
			if m.findQuery != "" {
				m.applyStreamFind()
			} else {
				m.viewport.SetContent(renderEvents(m.streamEvents, m.viewport.Width, true, m.jsonPretty))
			}
			return m, nil
		case "y":
			if len(m.streamEvents) > 0 {
				m.status = doYank(renderEvents(m.streamEvents, 0, false, m.jsonPretty), "stream events")
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
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
		case "J":
			m.jsonPretty = !m.jsonPretty
			m.viewport.SetContent(renderEvents(m.results, m.viewport.Width, true, m.jsonPretty))
			return m, nil
		case "y":
			if len(m.results) > 0 {
				m.status = doYank(renderEvents(m.results, 0, false, m.jsonPretty), "all matches")
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
	if m.mode == modeStreamEvents && m.findMode {
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
	case modeStreamEvents:
		items = len(m.streamEvents)
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
				{Keys: "enter", Desc: "view stream events"},
				{Keys: "r", Desc: "refresh"},
				{Keys: "esc", Desc: "back to log groups"},
			},
		}}
	case modeStreamEvents:
		if m.findMode {
			return []help.Section{{
				Title: "CloudWatch · find in events",
				Items: []help.Item{
					{Keys: "type", Desc: "highlight matches (live)"},
					{Keys: "enter", Desc: "apply and exit input"},
					{Keys: "esc", Desc: "clear and exit search"},
				},
			}}
		}
		return []help.Section{{
			Title: "CloudWatch · stream events",
			Items: []help.Item{
				{Keys: "/", Desc: "find (in-page search)"},
				{Keys: "n / N", Desc: "next / prev match"},
				{Keys: "J", Desc: "toggle JSON pretty-print"},
				{Keys: "↑/↓", Desc: "scroll"},
				{Keys: "y", Desc: "yank all events"},
				{Keys: "r", Desc: "reload"},
				{Keys: "esc", Desc: "back to streams"},
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
				{Keys: "J", Desc: "toggle JSON pretty-print"},
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
				{Keys: "J", Desc: "toggle JSON pretty-print"},
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

// applyStreamFind re-renders the viewport with current findInput value
// and updates findMatches / findCursor. Called on every keystroke in
// findMode so highlights track typing live.
func (m *Model) applyStreamFind() {
	m.findQuery = strings.TrimSpace(m.findInput.Value())
	content, matches := renderEventsFindable(m.streamEvents, m.viewport.Width, true, m.findQuery, m.jsonPretty)
	m.viewport.SetContent(content)
	m.findMatches = matches
	if len(matches) == 0 {
		m.findCursor = 0
		return
	}
	if m.findCursor >= len(matches) {
		m.findCursor = 0
	}
	m.viewport.SetYOffset(matches[m.findCursor])
}

func (m *Model) resetFind() {
	m.findMode = false
	m.findInput.Blur()
	m.findInput.SetValue("")
	m.findQuery = ""
	m.findMatches = nil
	m.findCursor = 0
}

func (m Model) selectedStream() *LogStream {
	if len(m.streams) == 0 {
		return nil
	}
	idx := m.stTable.Cursor()
	if idx < 0 || idx >= len(m.streams) {
		return nil
	}
	return &m.streams[idx]
}

// streamHeaderTable renders the stream-events header as a two-column
// Field/Value table, matching the table-based detail panels used elsewhere.
// It is a fixed three-row table so the layout height is constant; the JSON
// and find indicators live on a separate meta line (see streamMetaLine).
func (m Model) streamHeaderTable() string {
	return datatable.RenderKeyValue("Field", "Value", []datatable.KV{
		{Key: "Group", Value: m.target.Name},
		{Key: "Stream", Value: m.streamTarget},
		{Key: "Events", Value: fmt.Sprintf("%d", len(m.streamEvents))},
	}, m.width)
}

// streamMetaLine renders the (always one-line) JSON / find status row shown
// directly under the header table. Returns an empty line when neither is
// active so the layout height stays constant.
func (m Model) streamMetaLine() string {
	var bits []string
	if m.jsonPretty {
		bits = append(bits, "json pretty-print")
	}
	if m.findQuery != "" {
		if len(m.findMatches) > 0 {
			bits = append(bits, fmt.Sprintf("match %d/%d for %q", m.findCursor+1, len(m.findMatches), m.findQuery))
		} else {
			bits = append(bits, fmt.Sprintf("no match for %q", m.findQuery))
		}
	}
	return mutedStyle.Render(strings.Join(bits, " · "))
}

// streamEventsViewportHeight sizes the events viewport to fit beneath the
// fixed-height key/value header table. That table is ~8 rows taller than the
// single-line title it replaced, so we reserve 8 extra rows on top of the
// generic height budget (height-6) used by the other views.
func (m Model) streamEventsViewportHeight() int {
	h := m.height - 14
	if h < 3 {
		h = 3
	}
	return h
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
		help := mutedStyle.Render("enter: view events · r: refresh · esc: back")
		body := m.stTable.View()
		if len(m.streams) == 0 {
			body = mutedStyle.Render("(no streams)")
		}
		return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)
	case modeStreamEvents:
		if m.streamEventsLoading {
			title := headerStyle.Render(m.target.Name + " / " + m.streamTarget)
			return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("loading events..."))
		}
		header := m.streamHeaderTable()
		help := mutedStyle.Render("/: find · n/N: next/prev · J: json · ↑/↓ scroll · y: yank · r: reload · esc: back")
		parts := []string{header, m.streamMetaLine()}
		if m.findMode {
			parts = append(parts, m.findInput.View())
		}
		parts = append(parts, m.viewport.View())
		if m.status != "" {
			parts = append(parts, mutedStyle.Render(m.status))
		}
		parts = append(parts, help)
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
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
		titleText := m.resultsTitle
		if m.jsonPretty {
			titleText += " · json"
		}
		title := headerStyle.Render(titleText)
		help := mutedStyle.Render("↑/↓ scroll · J: json · y: yank all · esc: back")
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
	headerText := fmt.Sprintf("CloudWatch Log Groups (%d loaded)", len(m.groups))
	if m.groupsToken != "" {
		headerText = fmt.Sprintf("CloudWatch Log Groups (%d loaded, fetching more...)", len(m.groups))
	}
	header := headerStyle.Render(headerText)
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.groupsTable.View()
	if len(m.groupsFilt) == 0 {
		body = mutedStyle.Render("No groups match filter.")
	}
	help := mutedStyle.Render("enter: streams · t: tail (shells out) · s: search · /: filter · r: refresh")
	return lipgloss.JoinVertical(lipgloss.Left, header, filterLine, "", body, "", help)
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

func renderEvents(events []LogEvent, width int, colorize, pretty bool) string {
	content, _ := renderEventsFindable(events, width, colorize, "", pretty)
	return content
}

// renderEventsFindable is like renderEvents but also highlights every
// occurrence of `query` (case-insensitive literal). Returns the rendered
// content together with the visual line offset where each matching event
// begins in the wrapped output; n/N navigation jumps to those offsets.
// An empty query disables highlighting and yields a nil matches slice.
// pretty enables JSON pretty-printing on messages that parse as JSON.
func renderEventsFindable(events []LogEvent, width int, colorize bool, query string, pretty bool) (string, []int) {
	if len(events) == 0 {
		return mutedStyle.Render("(no matches)"), nil
	}
	var findRe *regexp.Regexp
	if q := strings.TrimSpace(query); q != "" {
		if re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(q)); err == nil {
			findRe = re
		}
	}
	var sb strings.Builder
	var matchLines []int
	visualLine := 0
	for _, e := range events {
		matched := findRe != nil && findRe.MatchString(e.Message)
		src := formatLogLine(e, colorize, findRe, pretty)
		wrapped := wrapLine(src, width)
		if matched {
			matchLines = append(matchLines, visualLine)
		}
		sb.WriteString(wrapped)
		sb.WriteString("\n")
		visualLine += strings.Count(wrapped, "\n") + 1
	}
	return sb.String(), matchLines
}

// formatLogLine assembles a single log line. When colorize is true the
// timestamp, stream tag, and any embedded log-level keywords are styled;
// when false it returns a plain string suitable for clipboard yank.
// findRe, when non-nil, overrides level coloring inside the message body
// and wraps each hit in findHighlightStyle so search matches stand out.
// pretty rewrites JSON-shaped messages with json.MarshalIndent so they
// span multiple lines instead of one long ugly blob.
func formatLogLine(e LogEvent, colorize bool, findRe *regexp.Regexp, pretty bool) string {
	ts := e.Time.Format("15:04:05")
	stream := "[" + shortStream(e.Stream) + "]"
	msg := e.Message
	if pretty {
		msg = tryPrettyJSON(msg)
	}
	if findRe != nil {
		msg = findRe.ReplaceAllStringFunc(msg, func(m string) string {
			return findHighlightStyle.Render(m)
		})
	} else if colorize {
		msg = colorizeMessage(msg)
	}
	if colorize {
		return logTimeStyle.Render(ts) + " " + logStreamStyle.Render(stream) + " " + msg
	}
	return ts + " " + stream + " " + msg
}

// wrapLine word-wraps s to width. Falls back to a hard break inside a
// long token (URLs, base64, JSON without whitespace) so a single big
// token can't blow past the viewport edge. width <= 0 disables wrap.
func wrapLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Wrap(s, width, " -")
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

	logTimeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	logStreamStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	logErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	logWarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	logInfoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	logDebugStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	findHighlightStyle = lipgloss.NewStyle().Background(lipgloss.Color("220")).Foreground(lipgloss.Color("16")).Bold(true)
)

// logLevelRe matches common log-level keywords as whole words. Case
// insensitive so it picks up both ERROR and "level":"error" in JSON logs.
// WARNING precedes WARN so the longer alternative wins.
var logLevelRe = regexp.MustCompile(`(?i)\b(ERROR|FATAL|PANIC|WARNING|WARN|INFO|DEBUG|TRACE)\b`)

func colorizeMessage(s string) string {
	return logLevelRe.ReplaceAllStringFunc(s, func(match string) string {
		switch strings.ToUpper(match) {
		case "ERROR", "FATAL", "PANIC":
			return logErrorStyle.Render(match)
		case "WARN", "WARNING":
			return logWarnStyle.Render(match)
		case "INFO":
			return logInfoStyle.Render(match)
		case "DEBUG", "TRACE":
			return logDebugStyle.Render(match)
		}
		return match
	})
}
