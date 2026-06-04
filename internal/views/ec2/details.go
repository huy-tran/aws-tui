package ec2

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
)

// detailsModel is pushed onto the view stack by the EC2 list when the user
// presses 'i'. It renders the full metadata for a single instance and
// supports y-chord yank hotkeys for instance ID, private IP and public DNS.
type detailsModel struct {
	ctx          *awspkg.Context
	inst         Instance
	yankPending  bool
	status       string
	width        int
}

func newDetails(ctx *awspkg.Context, inst Instance) detailsModel {
	return detailsModel{ctx: ctx, inst: inst}
}

func (m detailsModel) Init() tea.Cmd { return nil }

func (m detailsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		key := msg.String()
		if m.yankPending {
			m.yankPending = false
			switch key {
			case "i":
				m.status = m.yank(m.inst.ID, "instance ID")
			case "p":
				m.status = m.yank(m.inst.PrivIP, "private IP")
			case "d":
				m.status = m.yank(m.inst.PubDNS, "public DNS")
			default:
				m.status = ""
			}
			return m, nil
		}
		switch key {
		case "y":
			m.yankPending = true
			m.status = "yank: i=ID · p=private IP · d=public DNS"
			return m, nil
		case "esc", "q":
			return m, nav.PopView()
		}
	}
	return m, nil
}

func (m detailsModel) yank(value, label string) string {
	if value == "" {
		return "no " + label + " to copy"
	}
	if err := clipboard.WriteAll(value); err != nil {
		return "clipboard error: " + err.Error()
	}
	return "copied " + label + ": " + value
}

func (m detailsModel) View() string {
	header := headerStyle.Render(fmt.Sprintf("Instance Details — %s", m.inst.ID))
	context := mutedStyle.Render(fmt.Sprintf("%s · %s", m.ctx.Profile, m.ctx.Region))

	body := datatable.RenderKeyValue("Field", "Value", []datatable.KV{
		{Key: "Name", Value: defaultStr(m.inst.Name, "-")},
		{Key: "Instance ID", Value: m.inst.ID},
		{Key: "State", Value: m.inst.State},
		{Key: "Type", Value: m.inst.Type},
		{Key: "Platform", Value: m.inst.Platform},
		{Key: "AMI", Value: m.inst.AMIID},
		{Key: "Launched", Value: m.inst.LaunchTime},
		{Key: "Private IP", Value: defaultStr(m.inst.PrivIP, "-")},
		{Key: "Public IP", Value: defaultStr(m.inst.PubIP, "-")},
		{Key: "Public DNS", Value: defaultStr(m.inst.PubDNS, "-")},
		{Key: "VPC", Value: defaultStr(m.inst.VPCID, "-")},
		{Key: "Subnet", Value: defaultStr(m.inst.SubnetID, "-")},
		{Key: "AZ", Value: defaultStr(m.inst.AZ, "-")},
		{Key: "IAM Role", Value: defaultStr(m.inst.IAMRole, "-")},
		{Key: "Security Groups", Value: joinOrDash(m.inst.SecurityGroups)},
	}, m.width)

	parts := []string{header, context, "", body}
	if len(m.inst.Tags) > 0 {
		tags := append([]TagPair(nil), m.inst.Tags...)
		sort.Slice(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
		tagKVs := make([]datatable.KV, len(tags))
		for i, t := range tags {
			tagKVs[i] = datatable.KV{Key: t.Key, Value: t.Value}
		}
		parts = append(parts, "", headerStyle.Render("Tags"),
			datatable.RenderKeyValue("Tag", "Value", tagKVs, m.width))
	}

	help := mutedStyle.Render("y i = yank ID · y p = yank private IP · y d = yank public DNS · esc to close")
	parts = append(parts, "")
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	parts = append(parts, help)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func joinOrDash(parts []string) string {
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}
