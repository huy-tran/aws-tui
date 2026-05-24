# 02 — Architecture

## Bubble Tea overview

Each view is its own `tea.Model` (Init/Update/View). The root app model holds a **view stack** and delegates Update/View calls to the top of the stack. Navigation works by pushing/popping models.

## Root model (internal/app/app.go)

```go
package app

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/YOUR_USERNAME/aws-tui/internal/aws"
	"github.com/YOUR_USERNAME/aws-tui/internal/state"
)

type Model struct {
	stack    []tea.Model        // View stack, top = active
	awsCtx   *aws.Context       // Current profile + region + clients
	state    *state.Store       // Persisted state
	width    int
	height   int
}

func New() (Model, error) {
	store, err := state.Load()
	if err != nil {
		return Model{}, err
	}

	m := Model{
		stack: []tea.Model{},
		state: store,
	}

	// Start with profile picker
	picker := profile.New(store)
	m.stack = append(m.stack, picker)

	return m, nil
}

func (m Model) Init() tea.Cmd {
	if len(m.stack) == 0 {
		return nil
	}
	return m.stack[len(m.stack)-1].Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Forward to all stack members
		for i, v := range m.stack {
			updated, _ := v.Update(msg)
			m.stack[i] = updated
		}
		return m, nil

	case tea.KeyMsg:
		// Global keybindings
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+p":
			// Jump back to profile picker
			return m.resetToProfilePicker()
		}

	case PushViewMsg:
		m.stack = append(m.stack, msg.View)
		return m, msg.View.Init()

	case PopViewMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case ProfileSelectedMsg:
		m.awsCtx = aws.NewContext(msg.Profile)
		// Push region picker next
		return m, PushView(region.New(m.awsCtx, m.state))

	case RegionSelectedMsg:
		m.awsCtx.SetRegion(msg.Region)
		// Push dashboard
		return m, PushView(dashboard.New(m.awsCtx))
	}

	// Delegate to top view
	if len(m.stack) > 0 {
		top := len(m.stack) - 1
		updated, cmd := m.stack[top].Update(msg)
		m.stack[top] = updated
		return m, cmd
	}

	return m, nil
}

func (m Model) View() string {
	if len(m.stack) == 0 {
		return ""
	}
	return m.stack[len(m.stack)-1].View()
}
```

## Navigation messages (internal/app/nav.go)

```go
package app

import tea "github.com/charmbracelet/bubbletea"

// PushViewMsg pushes a new view onto the stack.
type PushViewMsg struct {
	View tea.Model
}

// PopViewMsg pops the current view.
type PopViewMsg struct{}

// ProfileSelectedMsg is emitted when the user picks a profile.
type ProfileSelectedMsg struct {
	Profile string
}

// RegionSelectedMsg is emitted when the user picks a region.
type RegionSelectedMsg struct {
	Region string
}

func PushView(v tea.Model) tea.Cmd {
	return func() tea.Msg { return PushViewMsg{View: v} }
}

func PopView() tea.Cmd {
	return func() tea.Msg { return PopViewMsg{} }
}
```

## Global keybindings (internal/app/keys.go)

These work from any screen:

| Key      | Action                          |
|----------|---------------------------------|
| `ctrl+c` | Quit                            |
| `ctrl+p` | Jump to profile picker          |
| `ctrl+r` | Jump to region picker (if context exists) |
| `esc`    | Pop current view (back)         |
| `?`      | Toggle help overlay             |

Views handle their own local keys (j/k navigation, enter, etc.) without conflicting with these.

## Why a view stack?

- **Back navigation is free.** `esc` pops the stack.
- **Each view owns its state.** No giant shared model.
- **Modal dialogs are just pushed views.** A "confirm invalidation?" prompt is a model pushed temporarily.
- **Testing is easier.** Each view can be tested in isolation.

## Window resize handling

Bubble Tea sends `tea.WindowSizeMsg` once at start and on every resize. The root forwards it to **every** view in the stack so they all stay aware of dimensions (the top view uses it for layout, but lower views need it for when they're re-shown after a pop).

## Async work pattern

AWS API calls happen in `tea.Cmd` (background functions returning messages):

```go
func loadInstancesCmd(client *ec2.Client) tea.Cmd {
	return func() tea.Msg {
		out, err := client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{})
		if err != nil {
			return ErrMsg{Err: err}
		}
		return InstancesLoadedMsg{Instances: parseInstances(out)}
	}
}
```

The view shows a spinner until the message arrives. Never block the Update function.

## Error handling pattern

A shared `ErrMsg` type bubbled up to the root model, displayed in a status bar:

```go
type ErrMsg struct {
	Err     error
	Context string  // e.g. "loading EC2 instances"
}
```

For SSO expiry specifically, use a typed error so the dashboard can offer to run `aws sso login` (see `03-aws-client.md`).
