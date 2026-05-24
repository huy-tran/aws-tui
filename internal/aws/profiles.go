package aws

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/ini.v1"
)

type Profile struct {
	Name        string
	Region      string // from config if set
	Source      string // "sso", "assume-role", "static", "unknown"
	SSOStartURL string // empty unless SSO
}

func ListProfiles() ([]Profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(home, ".aws", "config")
	credentialsPath := filepath.Join(home, ".aws", "credentials")

	seen := map[string]*Profile{}

	// Parse ~/.aws/config. SSO and assume-role profiles only live here.
	if cfg, err := ini.Load(configPath); err == nil {
		for _, section := range cfg.Sections() {
			name := section.Name()
			if name == "DEFAULT" {
				continue
			}
			// In config, profiles are named "profile X" (except "default").
			profileName := name
			if strings.HasPrefix(name, "profile ") {
				profileName = strings.TrimPrefix(name, "profile ")
			} else if name != "default" {
				continue
			}

			p := &Profile{
				Name:        profileName,
				Region:      section.Key("region").String(),
				SSOStartURL: section.Key("sso_start_url").String(),
			}

			switch {
			case p.SSOStartURL != "" || section.Key("sso_session").String() != "":
				p.Source = "sso"
			case section.Key("role_arn").String() != "":
				p.Source = "assume-role"
			default:
				p.Source = "unknown"
			}

			seen[profileName] = p
		}
	}

	// Union with ~/.aws/credentials (static keys). Profiles present in both
	// files retain their config-derived metadata; pure-credentials profiles
	// fall back to "static".
	if creds, err := ini.Load(credentialsPath); err == nil {
		for _, section := range creds.Sections() {
			name := section.Name()
			if name == "DEFAULT" {
				continue
			}
			if _, exists := seen[name]; !exists {
				seen[name] = &Profile{
					Name:   name,
					Source: "static",
				}
			} else if seen[name].Source == "unknown" {
				seen[name].Source = "static"
			}
		}
	}

	out := make([]Profile, 0, len(seen))
	for _, p := range seen {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
