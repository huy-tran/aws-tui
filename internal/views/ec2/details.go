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
)

// detailsModel is pushed onto the view stack by the EC2 list when the user
// presses 'i'. It renders the full metadata for a single instance and
// supports y-chord yank hotkeys for instance ID, private IP and public DNS.
type detailsModel struct {
	ctx          *awspkg.Context
	inst         Instance
	yankPending  bool
	status       string
}

func newDetails(ctx *awspkg.Context, inst Instance) detailsModel {
	return detailsModel{ctx: ctx, inst: inst}
}

func (m detailsModel) Init() tea.Cmd { return nil }

func (m detailsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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

	rows := []string{
		field("Name", defaultStr(m.inst.Name, "-")),
		field("Instance ID", m.inst.ID),
		field("State", m.inst.State),
		field("Type", m.inst.Type),
		field("Platform", m.inst.Platform),
		field("AMI", m.inst.AMIID),
		field("Launched", m.inst.LaunchTime),
		"",
		field("Private IP", defaultStr(m.inst.PrivIP, "-")),
		field("Public IP", defaultStr(m.inst.PubIP, "-")),
		field("Public DNS", defaultStr(m.inst.PubDNS, "-")),
		"",
		field("VPC", defaultStr(m.inst.VPCID, "-")),
		field("Subnet", defaultStr(m.inst.SubnetID, "-")),
		field("AZ", defaultStr(m.inst.AZ, "-")),
		field("IAM Role", defaultStr(m.inst.IAMRole, "-")),
		"",
		field("Security Groups", joinOrDash(m.inst.SecurityGroups)),
	}

	if len(m.inst.Tags) > 0 {
		rows = append(rows, "", headerStyle.Render("Tags"))
		tags := append([]TagPair(nil), m.inst.Tags...)
		sort.Slice(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
		for _, t := range tags {
			rows = append(rows, "  "+field(t.Key, t.Value))
		}
	}

	help := mutedStyle.Render("y i = yank ID · y p = yank private IP · y d = yank public DNS · esc to close")

	body := strings.Join(rows, "\n")

	footer := help
	if m.status != "" {
		footer = lipgloss.JoinVertical(lipgloss.Left, mutedStyle.Render(m.status), help)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		context,
		"",
		body,
		"",
		footer,
	)
}

func field(label, value string) string {
	return fmt.Sprintf("  %-18s %s", label+":", value)
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
