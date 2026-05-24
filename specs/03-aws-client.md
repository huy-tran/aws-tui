# 03 — AWS Client

The `internal/aws` package wraps the AWS SDK v2 and provides:

- Profile discovery from `~/.aws/config`
- Lazy credential loading (no API calls until needed)
- SSO expiry detection with inline refresh
- Cached service clients per profile+region

## Context type (internal/aws/client.go)

A single `Context` represents "I am working as profile X in region Y". All service clients hang off it.

```go
package aws

import (
	"context"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type Context struct {
	Profile string
	Region  string

	mu     sync.Mutex
	cfg    *awssdk.Config
	loaded bool
}

func NewContext(profile string) *Context {
	return &Context{Profile: profile}
}

func (c *Context) SetRegion(region string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Region = region
	c.loaded = false
	c.cfg = nil
}

// Load resolves credentials. Returns SSOExpiredError if SSO token expired.
func (c *Context) Load(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded {
		return nil
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithSharedConfigProfile(c.Profile),
		config.WithRegion(c.Region),
	)
	if err != nil {
		return err
	}

	// Force credential resolution now so we catch SSO expiry early
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		if isSSOExpired(err) {
			return &SSOExpiredError{Profile: c.Profile, Underlying: err}
		}
		return err
	}

	c.cfg = &cfg
	c.loaded = true
	return nil
}

// Service client accessors
func (c *Context) EC2() *ec2.Client         { return ec2.NewFromConfig(*c.cfg) }
func (c *Context) S3() *s3.Client           { return s3.NewFromConfig(*c.cfg) }
func (c *Context) CloudFront() *cloudfront.Client {
	return cloudfront.NewFromConfig(*c.cfg)
}
func (c *Context) Beanstalk() *elasticbeanstalk.Client {
	return elasticbeanstalk.NewFromConfig(*c.cfg)
}
func (c *Context) Logs() *cloudwatchlogs.Client {
	return cloudwatchlogs.NewFromConfig(*c.cfg)
}
func (c *Context) STS() *sts.Client { return sts.NewFromConfig(*c.cfg) }
func (c *Context) SSM() *ssm.Client { return ssm.NewFromConfig(*c.cfg) }
```

Service clients are created on demand, not cached. They're cheap to create from a config.

## Profile discovery (internal/aws/profiles.go)

Read `~/.aws/config` and return a list of profile names. Do NOT read `~/.aws/credentials` separately — SSO and assume-role profiles only appear in `config`. Profiles in `credentials` but not `config` use default region/output, which is fine, so also union them in.

```go
package aws

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/ini.v1"
)

type Profile struct {
	Name          string
	Region        string  // From config if set
	Source        string  // "sso", "assume-role", "static", "unknown"
	SSOStartURL   string  // Empty unless SSO
}

func ListProfiles() ([]Profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(home, ".aws", "config")
	credentialsPath := filepath.Join(home, ".aws", "credentials")

	seen := map[string]*Profile{}

	// Parse ~/.aws/config
	if cfg, err := ini.Load(configPath); err == nil {
		for _, section := range cfg.Sections() {
			name := section.Name()
			if name == "DEFAULT" {
				continue
			}
			// In config, profiles are named "profile X" (except "default")
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

	// Union with ~/.aws/credentials (static keys)
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
```

## SSO expiry handling (internal/aws/sso.go)

```go
package aws

import (
	"fmt"
	"os/exec"
	"strings"
)

type SSOExpiredError struct {
	Profile    string
	Underlying error
}

func (e *SSOExpiredError) Error() string {
	return fmt.Sprintf("SSO session expired for profile %q: %v", e.Profile, e.Underlying)
}

// isSSOExpired heuristically detects SSO expiry from SDK errors.
// The SDK doesn't expose a typed error for this, so we string-match.
func isSSOExpired(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token is expired") ||
		strings.Contains(msg, "sso session has expired") ||
		strings.Contains(msg, "ssooidc") && strings.Contains(msg, "expired")
}

// RefreshSSO runs `aws sso login --profile X` and waits for it to finish.
// Returns the command output for display.
func RefreshSSO(profile string) error {
	cmd := exec.Command("aws", "sso", "login", "--profile", profile)
	// Inherit stdio so the user can interact with the browser flow
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
```

When the dashboard catches an `*SSOExpiredError`, it should:
1. Show a modal: "SSO session expired. Press `l` to run `aws sso login`."
2. On `l`, suspend the TUI (`tea.ExecProcess` style), run the login, resume.
3. After resume, retry the original API call.

## Region list

Regions are essentially static. Hardcode the common ones for the picker, but also accept any string the user types:

```go
package aws

var CommonRegions = []string{
	"ap-southeast-2", // Sydney
	"ap-southeast-1", // Singapore
	"ap-southeast-4", // Melbourne
	"ap-northeast-1", // Tokyo
	"us-east-1",      // N. Virginia
	"us-east-2",      // Ohio
	"us-west-1",      // N. California
	"us-west-2",      // Oregon
	"eu-west-1",      // Ireland
	"eu-west-2",      // London
	"eu-central-1",   // Frankfurt
}
```

The region picker uses this as the default list but allows typing any other region (e.g. `me-south-1`).

## Testing the client

Quick smoke test before building views:

```go
package main

import (
	"context"
	"fmt"

	"github.com/YOUR_USERNAME/aws-tui/internal/aws"
)

func main() {
	profiles, _ := aws.ListProfiles()
	for _, p := range profiles {
		fmt.Printf("%s (%s) region=%s\n", p.Name, p.Source, p.Region)
	}

	if len(profiles) > 0 {
		ctx := aws.NewContext(profiles[0].Name)
		ctx.SetRegion("ap-southeast-2")
		if err := ctx.Load(context.Background()); err != nil {
			fmt.Println("load error:", err)
		} else {
			fmt.Println("loaded OK")
		}
	}
}
```
