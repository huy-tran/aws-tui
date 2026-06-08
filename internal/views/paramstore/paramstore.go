package paramstore

import (
	"context"
	"encoding/gob"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ssmsdk "github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/huy-tran/aws-tui/internal/audit"
	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/timefmt"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const parametersCacheTTL = 60 * time.Second

// secureIdleTimeout is how long a revealed SecureString value stays visible
// without any keystroke before it re-masks automatically.
const secureIdleTimeout = 30 * time.Second

// Register the cached slice with gob so the persistent cache can round-trip
// it across restarts. Parameter values themselves are never cached, so the
// disk file only ever holds list metadata.
func init() { gob.Register([]Parameter(nil)) }

type Parameter struct {
	Name         string
	Type         string
	Version      int64
	LastModified string
	ModifiedBy   string
	Description  string
	KMSKeyID     string
	Value        string // populated lazily by GetParameter; never persisted
}

type ParameterVersion struct {
	Version      int64
	LastModified string
	ModifiedBy   string
	Type         string
}

type (
	parametersLoadedMsg struct{ items []Parameter }
	parameterValueMsg   struct{ p Parameter }
	// maskCheckMsg fires on a Tick after a SecureString reveal; if the
	// user has been idle for >= secureIdleTimeout the value re-masks.
	maskCheckMsg      struct{}
	parameterSavedMsg struct {
		name    string
		version int64
		dryRun  bool
	}
	historyLoadedMsg struct {
		name     string
		versions []ParameterVersion
	}
	errMsg struct{ err error }
)

type mode int

const (
	modeList mode = iota
	modeValue
	modeHistory
	modeEdit
	modeEditConfirmProd
	modeCreate
)

// formField indexes the focusable inputs in modeEdit / modeCreate so tab can
// cycle between them.
type formField int

type Model struct {
	ctx    *awspkg.Context
	mode   mode
	width  int
	height int

	// list
	params     []Parameter
	paramsFilt []Parameter
	table      datatable.Model
	filter     textinput.Model
	filterMode bool
	loading    bool
	err        error
	status     string

	// value / history
	target         Parameter
	valueLoading   bool
	historyLoading bool
	pendingEdit    bool // when true, the next parameterValueMsg routes to modeEdit
	history        []ParameterVersion
	histTable      datatable.Model
	valueViewport  viewport.Model
	valueLines     []string // rendered value, split per line, for cursor + line-yank
	valueCursor    int      // index into valueLines
	// SecureString reveal state. revealedValue=false means the viewport
	// shows the masked rendering; lastReveal is bumped on every keystroke
	// in modeValue while revealed so the user gets a full idle window
	// after their last interaction.
	revealedValue bool
	lastReveal    time.Time

	loader     loader.Model
	lastLoaded time.Time

	// edit / create
	valueInput  textarea.Model
	descInput   textinput.Model
	nameInput   textinput.Model
	kmsKeyInput textinput.Model
	confirm     textinput.Model
	typeChoice  int // 0=String 1=StringList 2=SecureString
	focusIdx    formField

	// In-edit text search. findEditMode flips the bottom row of the edit
	// view into a search prompt; findEditMatches caches (row, col) pairs
	// in the textarea's underlying value so n/N navigation can hop the
	// textarea cursor without re-scanning. findPreview renders the value
	// with highlighted matches in place of the textarea (textareas have
	// no public hook for inline styling), and scrolls to the active match.
	findEditMode    bool
	findEditInput   textinput.Model
	findEditMatches []findMatch
	findEditCursor  int
	findPreview     viewport.Model
}

// findMatch is a position inside the textarea's logical value (rows split
// on '\n'). Col is a byte offset into that row, not a rune offset - this
// matches what textarea.SetCursor expects.
type findMatch struct {
	Row int
	Col int
}

func New(ctx *awspkg.Context) Model {
	cols := []datatable.Column{
		{Title: "Name", Flex: true},
		{Title: "Type"},
		{Title: "Modified", SortAs: datatable.SortTime},
		{Title: "Ver", SortAs: datatable.SortNumeric},
	}
	t := datatable.New(cols)
	t.SetHeight(20)

	histCols := []datatable.Column{
		{Title: "Version", SortAs: datatable.SortNumeric},
		{Title: "Modified", SortAs: datatable.SortTime},
		{Title: "Modified by", Flex: true},
	}
	hist := datatable.New(histCols)
	hist.SetHeight(15)

	flt := textinput.New()
	flt.Placeholder = "filter by name"
	flt.CharLimit = 256
	flt.Prompt = "/ "

	value := textarea.New()
	value.Placeholder = "value"
	value.SetHeight(8)
	// bubbles defaults MaxHeight to 99 lines and the InsertNewline branch
	// silently no-ops once len(value) >= MaxHeight. Parameter Store values
	// (JSON blobs, certs, env exports) routinely exceed that, so lift the
	// cap to the package-wide maxLines (10000) by setting it to 0.
	value.MaxHeight = 0
	// Add alternate newline bindings so users whose terminal swallows plain
	// 'enter' (or who reach for shift+enter / alt+enter habitually from
	// chat apps and IDEs) still get a newline.
	value.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("enter", "ctrl+m", "ctrl+j", "shift+enter", "alt+enter"),
		key.WithHelp("enter / shift+enter", "insert newline"),
	)

	desc := textinput.New()
	desc.Placeholder = "description (optional)"
	desc.CharLimit = 1024

	name := textinput.New()
	name.Placeholder = "/path/to/parameter"
	name.CharLimit = 1024

	kms := textinput.New()
	kms.Placeholder = "alias/aws/ssm"
	kms.CharLimit = 256

	confirm := textinput.New()
	confirm.Placeholder = "type the parameter name to confirm"
	confirm.CharLimit = 1024

	findFlt := textinput.New()
	findFlt.Placeholder = "find in value"
	findFlt.CharLimit = 256
	findFlt.Prompt = "/ "

	vp := viewport.New(0, 0)
	findVP := viewport.New(0, 0)

	return Model{
		ctx:           ctx,
		table:         t,
		histTable:     hist,
		filter:        flt,
		valueInput:    value,
		descInput:     desc,
		nameInput:     name,
		kmsKeyInput:   kms,
		confirm:       confirm,
		findEditInput: findFlt,
		findPreview:   findVP,
		valueViewport: vp,
		loader:        loader.New(),
		loading:       true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadParametersCmd(false))
}

func (m Model) anyLoading() bool {
	return m.loading || m.valueLoading || m.historyLoading
}

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

// loadValueCmd fetches the value of a parameter. selectorVersion <= 0 fetches
// the current version; > 0 fetches a specific historical version using the
// "name:version" selector form.
func (m Model) loadValueCmd(name string, selectorVersion int64) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.SSM()
		fullName := name
		if selectorVersion > 0 {
			fullName = name + ":" + strconv.FormatInt(selectorVersion, 10)
		}
		out, err := client.GetParameter(context.Background(), &ssmsdk.GetParameterInput{
			Name:           awssdk.String(fullName),
			WithDecryption: awssdk.Bool(true),
		})
		if err != nil {
			return errMsg{err: err}
		}
		p := Parameter{
			Name:         name,
			Type:         string(out.Parameter.Type),
			Version:      out.Parameter.Version,
			LastModified: formatTime(out.Parameter.LastModifiedDate),
			Value:        awssdk.ToString(out.Parameter.Value),
		}
		return parameterValueMsg{p: p}
	}
}

func (m Model) loadHistoryCmd(name string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.SSM()
		out, err := client.GetParameterHistory(context.Background(), &ssmsdk.GetParameterHistoryInput{
			Name:           awssdk.String(name),
			WithDecryption: awssdk.Bool(false),
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

func (m Model) saveCmd(name, value, kmsKey, description string, paramType ssmtypes.ParameterType, overwrite bool) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		// Audit payload deliberately records only metadata - never the raw
		// value, so the on-disk audit log is safe to share / commit / etc.
		action := audit.Action{
			Profile: ctx.Profile,
			Region:  ctx.Region,
			Action:  "ssm:PutParameter",
			Target:  name,
			Payload: map[string]any{
				"type":        string(paramType),
				"overwrite":   overwrite,
				"value_bytes": len(value),
			},
		}
		if audit.IsDryRun() {
			audit.Log(action, true, "")
			ctx.Cache.Invalidate("ssm:params:" + ctx.Region)
			return parameterSavedMsg{name: name, version: 0, dryRun: true}
		}
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
		audit.Log(action, false, fmt.Sprintf("v%d", out.Version))
		ctx.Cache.Invalidate("ssm:params:" + ctx.Region)
		return parameterSavedMsg{name: name, version: out.Version}
	}
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
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		m.histTable.SetHeight(h)
		m.histTable.SetWidth(msg.Width)
		// Leave room above/below for labels and help when editing.
		valH := msg.Height - 12
		if valH < 4 {
			valH = 4
		}
		m.valueInput.SetHeight(valH)
		valW := msg.Width - 4
		if valW < 20 {
			valW = 20
		}
		m.valueInput.SetWidth(valW)
		m.descInput.Width = valW
		m.nameInput.Width = valW
		m.kmsKeyInput.Width = valW
		m.confirm.Width = valW
		m.findEditInput.Width = valW
		m.findPreview.Width = valW
		m.findPreview.Height = valH
		// Value viewport: reserve room for title (1) + blank (1) +
		// metadata (~6) + blank (1) + label (1) + box border (2) +
		// help (1) + status (1) ≈ 14 lines.
		vpH := msg.Height - 14
		if vpH < 3 {
			vpH = 3
		}
		m.valueViewport.Width = boxWidth(msg.Width)
		m.valueViewport.Height = vpH
		return m, nil

	case parametersLoadedMsg:
		m.loading = false
		m.params = msg.items
		m.lastLoaded = time.Now()
		m.applyFilter()
		return m, nil

	case parameterValueMsg:
		m.valueLoading = false
		// Preserve metadata from the list entry; merge in decrypted value.
		m.target.Type = msg.p.Type
		m.target.Version = msg.p.Version
		m.target.LastModified = msg.p.LastModified
		m.target.Value = msg.p.Value
		m.valueLines = strings.Split(renderValueBody(m.target), "\n")
		m.valueCursor = 0
		m.revealedValue = false // SecureString always opens masked
		m.refreshValueViewport()
		m.valueViewport.GotoTop()
		if m.pendingEdit {
			m.pendingEdit = false
			m.enterEditFromTarget()
			return m, textarea.Blink
		}
		return m, nil

	case maskCheckMsg:
		// Only relevant if we're still on a revealed SecureString in
		// modeValue. Anything else and the message is just discarded.
		if m.mode != modeValue || !m.revealedValue || !m.isSecureString() {
			return m, nil
		}
		elapsed := time.Since(m.lastReveal)
		if elapsed >= secureIdleTimeout {
			m.revealedValue = false
			m.refreshValueViewport()
			return m, nil
		}
		// User did something since the last reveal; reschedule the check
		// for the remaining window.
		return m, scheduleMaskCheck(secureIdleTimeout - elapsed)

	case historyLoadedMsg:
		if msg.name == m.target.Name {
			m.history = msg.versions
			m.histTable.SetRows(buildHistoryRows(m.history))
			m.historyLoading = false
		}
		return m, nil

	case parameterSavedMsg:
		if msg.dryRun {
			m.status = fmt.Sprintf("dry-run: save %s", msg.name)
			m.mode = modeList
			return m, nil
		}
		m.status = fmt.Sprintf("saved %s (v%d)", msg.name, msg.version)
		m.mode = modeValue
		m.target.Name = msg.name
		m.valueLoading = true
		m.target.Value = ""
		return m, m.loadValueCmd(msg.name, 0)

	case errMsg:
		m.loading = false
		m.valueLoading = false
		m.historyLoading = false
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
	switch m.mode {
	case modeValue:
		return m.updateValueKeys(msg)
	case modeHistory:
		return m.updateHistoryKeys(msg)
	case modeEdit:
		return m.updateEditKeys(msg)
	case modeEditConfirmProd:
		return m.updateProdConfirmKeys(msg)
	case modeCreate:
		return m.updateCreateKeys(msg)
	}
	return m.updateListKeys(msg)
}

// --- list -----------------------------------------------------------------

func (m Model) updateListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

	switch msg.String() {
	case "/":
		m.filterMode = true
		m.filter.Focus()
		m.status = ""
		return m, textinput.Blink
	case "r":
		m.ctx.Cache.Invalidate("ssm:params:" + m.ctx.Region)
		m.loading = true
		m.err = nil
		return m, tea.Batch(m.loader.Tick(), m.loadParametersCmd(true))
	case "n":
		m.mode = modeCreate
		m.nameInput.SetValue("")
		m.kmsKeyInput.SetValue("alias/aws/ssm")
		m.valueInput.SetValue("")
		m.descInput.SetValue("")
		m.typeChoice = 0
		m.focusIdx = 0
		m.nameInput.Focus()
		m.valueInput.Blur()
		m.descInput.Blur()
		m.kmsKeyInput.Blur()
		m.status = ""
		return m, textinput.Blink
	case "enter":
		if p := m.selected(); p != nil {
			m.target = *p
			m.target.Value = ""
			m.valueLoading = true
			m.valueViewport.SetContent("")
			m.mode = modeValue
			m.status = ""
			return m, tea.Batch(m.loader.Tick(), m.loadValueCmd(p.Name, 0))
		}
	case "e":
		if p := m.selected(); p != nil {
			m.target = *p
			m.valueLoading = true
			m.target.Value = ""
			m.pendingEdit = true
			m.status = ""
			// Routing to modeEdit waits for the value to arrive so the
			// textarea opens pre-populated.
			return m, tea.Batch(m.loader.Tick(), m.loadValueCmd(p.Name, 0))
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// --- value ----------------------------------------------------------------

func (m Model) updateValueKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Bump the idle clock on any keystroke while a SecureString is
	// revealed - lets the user scroll / move cursor without surprise
	// re-masks. The check inside the conditional avoids touching the
	// timer for non-secure values.
	if m.revealedValue && m.isSecureString() {
		m.lastReveal = time.Now()
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.target = Parameter{}
		m.revealedValue = false
		m.status = ""
		return m, nil
	case "r":
		// Only meaningful for SecureString; for plain String types
		// 'r' does nothing here (the list view uses it for refresh,
		// but in modeValue refresh isn't a thing).
		if !m.isSecureString() {
			return m, nil
		}
		m.revealedValue = !m.revealedValue
		if m.revealedValue {
			m.lastReveal = time.Now()
		}
		m.refreshValueViewport()
		if m.revealedValue {
			return m, scheduleMaskCheck(secureIdleTimeout)
		}
		return m, nil
	case "y":
		if m.target.Value == "" {
			m.status = "value not loaded yet"
			return m, nil
		}
		// Always yank the raw value regardless of mask state - the
		// mask is a visual concern only.
		m.status = doYank(m.target.Value, "value")
		return m, nil
	case "Y":
		if len(m.valueLines) == 0 {
			m.status = "value not loaded yet"
			return m, nil
		}
		// Yank the real line from the underlying value, not the
		// rendered (possibly masked) one in the viewport.
		line := m.valueLines[m.valueCursor]
		m.status = doYank(line, fmt.Sprintf("line %d", m.valueCursor+1))
		return m, nil
	case "h":
		m.mode = modeHistory
		m.history = nil
		m.histTable.SetRows(nil)
		m.historyLoading = true
		return m, tea.Batch(m.loader.Tick(), m.loadHistoryCmd(m.target.Name))
	case "e":
		m.enterEditFromTarget()
		return m, textarea.Blink
	case "up", "k":
		m.moveValueCursor(-1)
		return m, nil
	case "down", "j":
		m.moveValueCursor(1)
		return m, nil
	case "home", "g":
		m.moveValueCursorTo(0)
		return m, nil
	case "end", "G":
		m.moveValueCursorTo(len(m.valueLines) - 1)
		return m, nil
	case "pgup":
		m.moveValueCursor(-m.valueViewport.Height)
		return m, nil
	case "pgdown", " ":
		m.moveValueCursor(m.valueViewport.Height)
		return m, nil
	}
	// Anything else: pass through to the viewport (mouse scroll, etc).
	var cmd tea.Cmd
	m.valueViewport, cmd = m.valueViewport.Update(msg)
	return m, cmd
}

func (m *Model) moveValueCursor(delta int) {
	if len(m.valueLines) == 0 {
		return
	}
	m.moveValueCursorTo(m.valueCursor + delta)
}

func (m *Model) moveValueCursorTo(idx int) {
	if len(m.valueLines) == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.valueLines) {
		idx = len(m.valueLines) - 1
	}
	m.valueCursor = idx
	// Re-render content so the new cursor row picks up the highlight.
	// Routes through refreshValueViewport so the mask state is honored.
	m.refreshValueViewport()
	// Scroll the viewport so the cursor stays in view.
	off := m.valueViewport.YOffset
	h := m.valueViewport.Height
	if h <= 0 {
		return
	}
	if m.valueCursor < off {
		m.valueViewport.SetYOffset(m.valueCursor)
	} else if m.valueCursor >= off+h {
		m.valueViewport.SetYOffset(m.valueCursor - h + 1)
	}
}

// --- history --------------------------------------------------------------

func (m Model) updateHistoryKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeValue
		return m, nil
	case "enter":
		if v := m.selectedVersion(); v != nil {
			m.valueLoading = true
			m.target.Value = ""
			m.valueViewport.SetContent("")
			m.mode = modeValue
			return m, tea.Batch(m.loader.Tick(), m.loadValueCmd(m.target.Name, v.Version))
		}
	}
	var cmd tea.Cmd
	m.histTable, cmd = m.histTable.Update(msg)
	return m, cmd
}

// --- edit -----------------------------------------------------------------

func (m *Model) enterEditFromTarget() {
	m.mode = modeEdit
	m.valueInput.SetValue(m.target.Value)
	m.descInput.SetValue(m.target.Description)
	m.focusIdx = 0
	m.valueInput.Focus()
	m.descInput.Blur()
	m.status = ""
}

func (m Model) updateEditKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.findEditMode {
		switch msg.String() {
		case "esc":
			m.findEditMode = false
			m.findEditInput.Blur()
			m.findEditInput.SetValue("")
			m.findEditMatches = nil
			m.findEditCursor = 0
			m.applyEditFocus()
			return m, nil
		case "enter":
			m.applyFindEditQuery()
			m.jumpToCurrentMatch()
			// Exit the find input but keep matches for ctrl+n / ctrl+p
			// navigation, and refocus the textarea so the user can
			// resume editing at the jump point.
			m.findEditMode = false
			m.findEditInput.Blur()
			m.applyEditFocus()
			return m, nil
		case "down", "ctrl+n":
			if len(m.findEditMatches) > 0 {
				m.findEditCursor = (m.findEditCursor + 1) % len(m.findEditMatches)
				m.refreshFindPreview()
			}
			return m, nil
		case "up", "ctrl+p":
			if len(m.findEditMatches) > 0 {
				m.findEditCursor = (m.findEditCursor - 1 + len(m.findEditMatches)) % len(m.findEditMatches)
				m.refreshFindPreview()
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.findEditInput, cmd = m.findEditInput.Update(msg)
		m.applyFindEditQuery()
		return m, cmd
	}

	switch msg.String() {
	case "esc":
		m.mode = modeValue
		m.valueInput.Blur()
		m.descInput.Blur()
		return m, nil
	case "ctrl+s":
		value := m.valueInput.Value()
		if value == "" {
			m.status = "value cannot be empty"
			return m, nil
		}
		if isProdName(m.target.Name) {
			m.mode = modeEditConfirmProd
			m.confirm.SetValue("")
			m.confirm.Focus()
			m.status = ""
			return m, textinput.Blink
		}
		return m, m.saveCmd(
			m.target.Name,
			value,
			m.target.KMSKeyID,
			m.descInput.Value(),
			ssmtypes.ParameterType(m.target.Type),
			true,
		)
	case "ctrl+f":
		m.findEditMode = true
		m.valueInput.Blur()
		m.descInput.Blur()
		m.findEditInput.SetValue("")
		m.findEditMatches = nil
		m.findEditCursor = 0
		m.findEditInput.Focus()
		m.refreshFindPreview()
		return m, textinput.Blink
	case "ctrl+n":
		if len(m.findEditMatches) > 0 {
			m.findEditCursor = (m.findEditCursor + 1) % len(m.findEditMatches)
			m.jumpToCurrentMatch()
		}
		return m, nil
	case "ctrl+p":
		if len(m.findEditMatches) > 0 {
			m.findEditCursor = (m.findEditCursor - 1 + len(m.findEditMatches)) % len(m.findEditMatches)
			m.jumpToCurrentMatch()
		}
		return m, nil
	case "tab":
		m.focusIdx = (m.focusIdx + 1) % 2
		m.applyEditFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.focusIdx = (m.focusIdx + 1) % 2 // only 2 fields; same as forward
		m.applyEditFocus()
		return m, textinput.Blink
	}
	return m.routeEditTyping(msg)
}

// applyFindEditQuery recomputes findEditMatches from the current textarea
// value and the latest input. Called on every keystroke in findEditMode so
// the match count tracks live, and rebuilds the find preview viewport.
func (m *Model) applyFindEditQuery() {
	q := strings.TrimSpace(m.findEditInput.Value())
	if q == "" {
		m.findEditMatches = nil
		m.findEditCursor = 0
		m.findPreview.SetContent(renderValueHighlights(m.valueInput.Value(), "", -1, m.findPreview.Width))
		m.findPreview.GotoTop()
		return
	}
	m.findEditMatches = findInValue(m.valueInput.Value(), q)
	if m.findEditCursor >= len(m.findEditMatches) {
		m.findEditCursor = 0
	}
	m.refreshFindPreview()
}

// refreshFindPreview rebuilds the preview viewport content with current
// matches highlighted and scrolls so the active match is visible.
func (m *Model) refreshFindPreview() {
	q := strings.TrimSpace(m.findEditInput.Value())
	m.findPreview.SetContent(renderValueHighlights(m.valueInput.Value(), q, m.findEditCursor, m.findPreview.Width))
	if len(m.findEditMatches) == 0 {
		m.findPreview.GotoTop()
		return
	}
	target := m.findEditMatches[m.findEditCursor]
	// Approximate visual line of the match: count newlines preceding the
	// match plus the wrap rows of each preceding logical line. Cheap and
	// close enough for scroll-into-view; the highlight makes the exact
	// position obvious.
	off := visualLineOf(m.valueInput.Value(), target.Row, m.findPreview.Width)
	// Center the match in the viewport when possible.
	if h := m.findPreview.Height; h > 0 {
		off -= h / 2
		if off < 0 {
			off = 0
		}
	}
	m.findPreview.SetYOffset(off)
}

// renderValueHighlights returns value with every occurrence of query (case
// insensitive) wrapped in findHighlightStyle. The match at currentIdx (if
// >= 0) gets the brighter findCurrentStyle so the user sees which one
// ctrl+n / ctrl+p will jump to. width drives soft wrap so the preview
// fits the screen without horizontal scrolling.
func renderValueHighlights(value, query string, currentIdx, width int) string {
	if value == "" {
		return mutedStyle.Render("(empty)")
	}
	if width <= 0 {
		width = 80
	}
	if query == "" {
		return ansi.Wrap(value, width, " -")
	}
	lower := strings.ToLower(value)
	needle := strings.ToLower(query)
	var b strings.Builder
	i := 0
	hit := 0
	for i < len(value) {
		idx := strings.Index(lower[i:], needle)
		if idx < 0 {
			b.WriteString(value[i:])
			break
		}
		b.WriteString(value[i : i+idx])
		match := value[i+idx : i+idx+len(needle)]
		style := findHighlightStyle
		if hit == currentIdx {
			style = findCurrentStyle
		}
		b.WriteString(style.Render(match))
		i = i + idx + len(needle)
		hit++
	}
	return ansi.Wrap(b.String(), width, " -")
}

// visualLineOf returns the count of wrapped visual rows that precede the
// given logical row in value when wrapped at width. Used by the find
// preview viewport to scroll the active match into view.
func visualLineOf(value string, row, width int) int {
	if row <= 0 || width <= 0 {
		return 0
	}
	lines := strings.Split(value, "\n")
	if row >= len(lines) {
		row = len(lines) - 1
	}
	total := 0
	for i := 0; i < row; i++ {
		w := lipgloss.Width(lines[i])
		if w == 0 {
			total++
			continue
		}
		total += (w + width - 1) / width
	}
	return total
}

// jumpToCurrentMatch positions the textarea cursor on the active match.
// Brute-forces row navigation by stepping CursorUp/Down because the
// textarea has no public SetRow.
func (m *Model) jumpToCurrentMatch() {
	if len(m.findEditMatches) == 0 {
		return
	}
	if m.findEditCursor < 0 || m.findEditCursor >= len(m.findEditMatches) {
		m.findEditCursor = 0
	}
	target := m.findEditMatches[m.findEditCursor]
	// Walk the textarea cursor to the target row.
	guard := len(strings.Split(m.valueInput.Value(), "\n")) + 4
	for m.valueInput.Line() > target.Row && guard > 0 {
		m.valueInput.CursorUp()
		guard--
	}
	for m.valueInput.Line() < target.Row && guard > 0 {
		m.valueInput.CursorDown()
		guard--
	}
	m.valueInput.SetCursor(target.Col)
}

// findInValue scans value (split on '\n') for case-insensitive matches of
// query and returns each hit as a (row, col) byte offset. ASCII-safe; on
// non-ASCII text the offsets remain byte-aligned which is what
// textarea.SetCursor expects.
func findInValue(value, query string) []findMatch {
	if query == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	needle := strings.ToLower(query)
	var out []findMatch
	for r, line := range lines {
		lower := strings.ToLower(line)
		start := 0
		for start <= len(lower) {
			idx := strings.Index(lower[start:], needle)
			if idx < 0 {
				break
			}
			out = append(out, findMatch{Row: r, Col: start + idx})
			start += idx + len(needle)
		}
	}
	return out
}

func (m *Model) applyEditFocus() {
	if m.focusIdx == 0 {
		m.valueInput.Focus()
		m.descInput.Blur()
	} else {
		m.valueInput.Blur()
		m.descInput.Focus()
	}
}

func (m Model) routeEditTyping(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.focusIdx == 0 {
		m.valueInput, cmd = m.valueInput.Update(msg)
	} else {
		m.descInput, cmd = m.descInput.Update(msg)
	}
	return m, cmd
}

func (m Model) updateProdConfirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeEdit
		m.confirm.Blur()
		m.applyEditFocus()
		return m, nil
	case "enter":
		if m.confirm.Value() != m.target.Name {
			m.status = "name mismatch - try again or esc"
			return m, nil
		}
		m.confirm.Blur()
		return m, m.saveCmd(
			m.target.Name,
			m.valueInput.Value(),
			m.target.KMSKeyID,
			m.descInput.Value(),
			ssmtypes.ParameterType(m.target.Type),
			true,
		)
	}
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(msg)
	return m, cmd
}

// --- create ---------------------------------------------------------------
//
// Form fields:
//   0 name (textinput)
//   1 type   (0/1/2 toggle, edited via space/left/right when focused)
//   2 kms    (textinput, only meaningful for SecureString)
//   3 value  (textarea)
//   4 desc   (textinput)

func (m Model) updateCreateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.nameInput.Blur()
		m.valueInput.Blur()
		m.descInput.Blur()
		m.kmsKeyInput.Blur()
		return m, nil
	case "ctrl+s":
		name := strings.TrimSpace(m.nameInput.Value())
		if !strings.HasPrefix(name, "/") {
			m.status = "name must start with /"
			return m, nil
		}
		value := m.valueInput.Value()
		if value == "" {
			m.status = "value cannot be empty"
			return m, nil
		}
		return m, m.saveCmd(
			name,
			value,
			m.kmsKeyInput.Value(),
			m.descInput.Value(),
			paramTypeFromChoice(m.typeChoice),
			false,
		)
	case "tab":
		m.focusIdx = (m.focusIdx + 1) % 5
		m.applyCreateFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.focusIdx = (m.focusIdx + 4) % 5
		m.applyCreateFocus()
		return m, textinput.Blink
	}

	// Type field has its own controls.
	if m.focusIdx == 1 {
		switch msg.String() {
		case "left":
			m.typeChoice = (m.typeChoice + 2) % 3
			return m, nil
		case "right", " ":
			m.typeChoice = (m.typeChoice + 1) % 3
			return m, nil
		}
		return m, nil
	}

	var cmd tea.Cmd
	switch m.focusIdx {
	case 0:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case 2:
		m.kmsKeyInput, cmd = m.kmsKeyInput.Update(msg)
	case 3:
		m.valueInput, cmd = m.valueInput.Update(msg)
	case 4:
		m.descInput, cmd = m.descInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) applyCreateFocus() {
	m.nameInput.Blur()
	m.kmsKeyInput.Blur()
	m.valueInput.Blur()
	m.descInput.Blur()
	switch m.focusIdx {
	case 0:
		m.nameInput.Focus()
	case 2:
		m.kmsKeyInput.Focus()
	case 3:
		m.valueInput.Focus()
	case 4:
		m.descInput.Focus()
	}
}

// --- helpers --------------------------------------------------------------

func (m Model) CapturingInput() bool {
	if m.mode == modeList && m.filterMode {
		return true
	}
	if m.mode == modeEdit && m.findEditMode {
		return true
	}
	return m.mode == modeEdit ||
		m.mode == modeEditConfirmProd ||
		m.mode == modeCreate
}

func (m Model) InSubnav() bool {
	return m.mode != modeList
}

func (m Model) BookmarkCurrent() (string, string, bool) {
	p := m.selected()
	if p == nil {
		return "", "", false
	}
	return p.Name, p.Name, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	m.applyFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	items := len(m.paramsFilt)
	if m.mode == modeHistory {
		items = len(m.history)
	}
	return statusbar.Snapshot{
		Items:      items,
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	switch m.mode {
	case modeValue:
		return []help.Section{{
			Title: "Parameter Store · value",
			Items: []help.Item{
				{Keys: "↑/↓ j/k", Desc: "move cursor row"},
				{Keys: "pgup/pgdn", Desc: "page"},
				{Keys: "home/end g/G", Desc: "first / last line"},
				{Keys: "r", Desc: "reveal/mask SecureString (auto-masks after 30s idle)"},
				{Keys: "y", Desc: "yank full value (raw, even when masked)"},
				{Keys: "Y", Desc: "yank cursor line (raw, even when masked)"},
				{Keys: "h", Desc: "history"},
				{Keys: "e", Desc: "edit"},
				{Keys: "esc", Desc: "back to list"},
			},
		}}
	case modeHistory:
		return []help.Section{{
			Title: "Parameter Store · history",
			Items: []help.Item{
				{Keys: "enter", Desc: "view that version's value"},
				{Keys: "esc", Desc: "back to value"},
			},
		}}
	case modeEdit:
		if m.findEditMode {
			return []help.Section{{
				Title: "Parameter Store · find in value",
				Items: []help.Item{
					{Keys: "type", Desc: "live match count"},
					{Keys: "enter", Desc: "jump to first match and back to editing"},
					{Keys: "esc", Desc: "cancel search"},
				},
			}}
		}
		return []help.Section{{
			Title: "Parameter Store · edit",
			Items: []help.Item{
				{Keys: "enter / shift+enter", Desc: "insert newline (when value is focused)"},
				{Keys: "tab", Desc: "next field (value / description)"},
				{Keys: "ctrl+f", Desc: "find in value"},
				{Keys: "ctrl+n / ctrl+p", Desc: "next / prev match"},
				{Keys: "ctrl+s", Desc: "save (prod paths require name-typed confirm)"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	case modeEditConfirmProd:
		return []help.Section{{
			Title: "Parameter Store · confirm prod write",
			Items: []help.Item{
				{Keys: "type param name", Desc: "must match exactly"},
				{Keys: "enter", Desc: "save"},
				{Keys: "esc", Desc: "back to edit"},
			},
		}}
	case modeCreate:
		return []help.Section{{
			Title: "Parameter Store · create",
			Items: []help.Item{
				{Keys: "tab / S-tab", Desc: "cycle fields"},
				{Keys: "left/right/space", Desc: "change type (when type focused)"},
				{Keys: "ctrl+s", Desc: "create"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	}
	if m.filterMode {
		return []help.Section{{
			Title: "Parameter Store · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match parameter name"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "Parameter Store",
		Items: []help.Item{
			{Keys: "enter", Desc: "view value (decrypts SecureString)"},
			{Keys: "e", Desc: "edit"},
			{Keys: "n", Desc: "new parameter"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
		},
	}}
}

func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.paramsFilt = m.params
	} else {
		out := make([]Parameter, 0, len(m.params))
		for _, p := range m.params {
			if strings.Contains(strings.ToLower(p.Name), q) {
				out = append(out, p)
			}
		}
		m.paramsFilt = out
	}
	m.table.SetRows(buildRows(m.paramsFilt))
}

func (m Model) selected() *Parameter {
	if m.loading || len(m.paramsFilt) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.paramsFilt) {
		return nil
	}
	return &m.paramsFilt[idx]
}

func (m Model) selectedVersion() *ParameterVersion {
	if len(m.history) == 0 {
		return nil
	}
	idx := m.histTable.Cursor()
	if idx < 0 || idx >= len(m.history) {
		return nil
	}
	return &m.history[idx]
}

func isProdName(name string) bool {
	return strings.Contains(strings.ToLower(name), "/prod")
}

func paramTypeFromChoice(c int) ssmtypes.ParameterType {
	switch c {
	case 1:
		return ssmtypes.ParameterTypeStringList
	case 2:
		return ssmtypes.ParameterTypeSecureString
	}
	return ssmtypes.ParameterTypeString
}

func paramTypeLabel(c int) string {
	switch c {
	case 1:
		return "StringList"
	case 2:
		return "SecureString"
	}
	return "String"
}

func doYank(value, label string) string {
	if err := clipboard.WriteAll(value); err != nil {
		return "clipboard error: " + err.Error()
	}
	return "copied " + label
}

func buildRows(items []Parameter) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, p := range items {
		rows[i] = datatable.Row{
			p.Name,
			typeBadge(p.Type),
			p.LastModified,
			strconv.FormatInt(p.Version, 10),
		}
	}
	return rows
}

// typeBadge highlights SecureString in warning yellow so it's obvious at
// a glance which parameters are encrypted (and therefore which a yank
// will pull a decrypted secret onto the clipboard). StringList gets a
// cyan tint to set it apart from plain String, which stays default.
func typeBadge(t string) string {
	switch t {
	case "SecureString":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Render(t)
	case "StringList":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render(t)
	case "String":
		return t
	case "":
		return mutedStyle.Render("N/A")
	}
	return t
}

func buildHistoryRows(items []ParameterVersion) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, v := range items {
		who := v.ModifiedBy
		if who == "" {
			who = "-"
		}
		// Pull the trailing identifier off the user ARN to keep the column
		// readable: arn:aws:iam::123:user/jane -> user/jane.
		if idx := strings.LastIndex(who, ":"); idx >= 0 && idx < len(who)-1 {
			who = who[idx+1:]
		}
		rows[i] = datatable.Row{
			strconv.FormatInt(v.Version, 10),
			v.LastModified,
			who,
		}
	}
	return rows
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return timefmt.Zone(*t, "2006-01-02 15:04:05")
}

// --- view -----------------------------------------------------------------

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry · esc to back."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	switch m.mode {
	case modeValue:
		return m.viewValue()
	case modeHistory:
		return m.viewHistory()
	case modeEdit:
		return m.viewEdit()
	case modeEditConfirmProd:
		return m.viewProdConfirm()
	case modeCreate:
		return m.viewCreate()
	}
	return m.viewList()
}

func (m Model) viewList() string {
	if m.loading {
		return m.loader.Render("loading parameters...")
	}
	if len(m.params) == 0 {
		return mutedStyle.Render("No parameters in this region.")
	}
	header := headerStyle.Render(fmt.Sprintf("Parameter Store (%d)", len(m.params)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.table.View()
	if len(m.paramsFilt) == 0 && m.filter.Value() != "" {
		body = mutedStyle.Render("No parameters match filter.")
	}
	help := mutedStyle.Render("enter: view · e: edit · n: new · /: filter · r: refresh")
	parts := []string{header, filterLine, "", body, "", help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) viewValue() string {
	title := headerStyle.Render(m.target.Name)
	meta := datatable.RenderKeyValue("Field", "Value", []datatable.KV{
		{Key: "Type", Value: m.target.Type},
		{Key: "Version", Value: strconv.FormatInt(m.target.Version, 10)},
		{Key: "Last mod", Value: m.target.LastModified},
		{Key: "Mod by", Value: emptyDash(m.target.ModifiedBy)},
		{Key: "KMS Key", Value: emptyDash(m.target.KMSKeyID)},
		{Key: "Description", Value: emptyDash(m.target.Description)},
	}, boxWidth(m.width))

	valueLabel := "Value"
	if m.isSecureString() {
		if m.revealedValue {
			valueLabel = "Value (decrypted · re-masks after idle · press 'r' to mask now)"
		} else {
			valueLabel = "Value (masked · press 'r' to reveal)"
		}
	}

	var valueBox string
	if m.valueLoading {
		valueBox = m.loader.Render("loading...")
	} else {
		valueBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			Render(m.valueViewport.View())
	}

	help := mutedStyle.Render("↑/↓ move · r: reveal/mask · y: yank value · Y: yank line · h: history · e: edit · esc: back")
	parts := []string{title, "", meta, "", mutedStyle.Render(valueLabel), valueBox, "", help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderValueBody formats the raw parameter value for viewport display:
// StringList values render one entry per line; everything else verbatim.
func renderValueBody(p Parameter) string {
	if p.Type == string(ssmtypes.ParameterTypeStringList) {
		return strings.ReplaceAll(p.Value, ",", "\n")
	}
	return p.Value
}

// isSecureString reports whether the loaded value is a SecureString and
// thus subject to mask-by-default / auto-mask treatment.
func (m Model) isSecureString() bool {
	return m.target.Type == string(ssmtypes.ParameterTypeSecureString)
}

// refreshValueViewport rebuilds the visible content based on the current
// mask state and cursor row. Called whenever revealedValue, valueCursor,
// or valueLines change.
func (m *Model) refreshValueViewport() {
	lines := m.valueLines
	if m.isSecureString() && !m.revealedValue {
		lines = maskLines(m.valueLines)
	}
	m.valueViewport.SetContent(renderValueWithCursor(lines, m.valueCursor))
}

// maskLines returns a copy of lines where every printable character is
// replaced with '•'. Whitespace (spaces, tabs, newlines) is preserved so
// the layout - line count, indentation, sentence breaks - is visible
// without revealing content.
func maskLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		var b strings.Builder
		for _, r := range ln {
			switch r {
			case ' ', '\t':
				b.WriteRune(r)
			default:
				b.WriteRune('•')
			}
		}
		out[i] = b.String()
	}
	return out
}

// scheduleMaskCheck returns a tea.Cmd that fires maskCheckMsg after the
// given delay. The Update handler decides whether to re-mask or reschedule.
func scheduleMaskCheck(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return maskCheckMsg{} })
}

// renderValueWithCursor returns the value content with the cursor row
// highlighted via a reverse-video style so the user knows which line
// 'Y' (yank line) will copy.
func renderValueWithCursor(lines []string, cursor int) string {
	if len(lines) == 0 {
		return ""
	}
	hi := lipgloss.NewStyle().Reverse(true)
	var b strings.Builder
	for i, line := range lines {
		if i == cursor {
			b.WriteString(hi.Render(line))
		} else {
			b.WriteString(line)
		}
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m Model) viewHistory() string {
	title := headerStyle.Render("History: " + m.target.Name)
	body := m.histTable.View()
	if m.historyLoading {
		body = m.loader.Render("loading history...")
	}
	help := mutedStyle.Render("enter: view that version's value · esc: back")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)
}

func (m Model) viewEdit() string {
	title := headerStyle.Render("Edit " + m.target.Name)
	meta := strings.Join([]string{
		field("Type", m.target.Type+typeImmutableHint()),
		field("Current ver", strconv.FormatInt(m.target.Version, 10)),
	}, "\n")
	help := mutedStyle.Render("enter / ctrl+j: newline · tab: switch field · ctrl+f: find · ctrl+n/p: next match · ctrl+s: save · esc: cancel")
	valueLabel := focusLabel("New value", m.focusIdx == 0 && !m.findEditMode)
	if q := strings.TrimSpace(m.findEditInput.Value()); q != "" {
		if len(m.findEditMatches) > 0 {
			valueLabel += fmt.Sprintf("  match %d/%d for %q", m.findEditCursor+1, len(m.findEditMatches), q)
		} else {
			valueLabel += fmt.Sprintf("  no match for %q", q)
		}
	}
	parts := []string{
		title, "", meta, "",
		mutedStyle.Render(valueLabel),
	}
	if m.findEditMode {
		// Replace the textarea with a highlighted preview while finding.
		// The textarea has no inline-style hook, so this is the only way
		// to make matches visible. Enter switches back to the textarea
		// positioned at the active match.
		previewBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			Render(m.findPreview.View())
		parts = append(parts, previewBox, m.findEditInput.View())
	} else {
		parts = append(parts, m.valueInput.View())
	}
	parts = append(parts,
		mutedStyle.Render(focusLabel("Description (optional)", m.focusIdx == 1 && !m.findEditMode)),
		m.descInput.View(),
		help,
	)
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func typeImmutableHint() string { return " (immutable for this name)" }

func (m Model) viewProdConfirm() string {
	title := headerStyle.Render("CONFIRM PROD WRITE")
	warn := errorStyle.Render("You are about to overwrite a prod parameter:")
	name := headerStyle.Render("  " + m.target.Name)
	prompt := "Type the parameter name to confirm:"
	help := mutedStyle.Render("enter to save · esc to cancel")
	status := ""
	if m.status != "" {
		status = errorStyle.Render(m.status)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		title, "", warn, name, "", prompt, m.confirm.View(), "", status, "", help,
	)
}

func (m Model) viewCreate() string {
	title := headerStyle.Render("Create Parameter")
	typeRow := renderTypeChoice(m.typeChoice, m.focusIdx == 1)
	kmsLabel := mutedStyle.Render(focusLabel("KMS Key (only used for SecureString)", m.focusIdx == 2))
	help := mutedStyle.Render("tab: next field · left/right: change type · ctrl+s: create · esc: cancel")
	parts := []string{
		title, "",
		mutedStyle.Render(focusLabel("Name", m.focusIdx == 0)),
		m.nameInput.View(),
		mutedStyle.Render(focusLabel("Type", m.focusIdx == 1)),
		typeRow,
		kmsLabel,
		m.kmsKeyInput.View(),
		mutedStyle.Render(focusLabel("Value", m.focusIdx == 3)),
		m.valueInput.View(),
		mutedStyle.Render(focusLabel("Description (optional)", m.focusIdx == 4)),
		m.descInput.View(),
		help,
	}
	if m.status != "" {
		parts = append(parts, errorStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func renderTypeChoice(active int, focused bool) string {
	choices := []string{"String", "StringList", "SecureString"}
	var parts []string
	for i, c := range choices {
		label := " " + c + " "
		style := lipgloss.NewStyle()
		if i == active {
			style = style.Bold(true).Foreground(lipgloss.Color("213")).Underline(focused)
			label = "[" + c + "]"
		} else {
			style = mutedStyle
		}
		parts = append(parts, style.Render(label))
	}
	return strings.Join(parts, "  ")
}

func field(label, value string) string {
	return fmt.Sprintf("  %-12s %s", label+":", value)
}

func focusLabel(label string, focused bool) string {
	if focused {
		return "▶ " + label
	}
	return "  " + label
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func boxWidth(viewWidth int) int {
	if viewWidth <= 4 {
		return 40
	}
	w := viewWidth - 2
	if w < 20 {
		return 20
	}
	return w
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
	// findHighlightStyle paints every match while the find prompt is open;
	// findCurrentStyle is the brighter "this is where enter will jump"
	// variant for the active match.
	findHighlightStyle = lipgloss.NewStyle().Background(lipgloss.Color("220")).Foreground(lipgloss.Color("16"))
	findCurrentStyle   = lipgloss.NewStyle().Background(lipgloss.Color("208")).Foreground(lipgloss.Color("16")).Bold(true)
)
