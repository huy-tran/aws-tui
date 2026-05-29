package aws

import (
	"context"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codedeploy"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/securityhub"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Context represents "I am working as profile X in region Y". All service
// clients hang off it. Credentials are resolved lazily on the first Load() call.
type Context struct {
	Profile string
	Region  string
	Cache   *Cache

	mu     sync.Mutex
	cfg    *awssdk.Config
	loaded bool
}

func NewContext(profile string) *Context {
	c := &Context{Profile: profile}
	if path := CachePath(profile); path != "" {
		c.Cache = NewPersistentCache(path)
	} else {
		c.Cache = NewCache()
	}
	return c
}

func (c *Context) SetRegion(region string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Region = region
	c.loaded = false
	c.cfg = nil
}

// Load resolves credentials. Returns *SSOExpiredError if SSO token has expired.
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

	// Force credential resolution now so SSO expiry surfaces early.
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

// Service client accessors. Clients are constructed on demand; they are cheap.
// Caller must have invoked Load() first.

func (c *Context) EC2() *ec2.Client {
	return ec2.NewFromConfig(*c.cfg)
}

func (c *Context) S3() *s3.Client {
	return s3.NewFromConfig(*c.cfg)
}

// S3InRegion returns an S3 client targeting the given bucket's region.
// Falls back to the context's region when empty.
func (c *Context) S3InRegion(region string) *s3.Client {
	if region == "" || region == c.Region {
		return s3.NewFromConfig(*c.cfg)
	}
	return s3.NewFromConfig(*c.cfg, func(o *s3.Options) {
		o.Region = region
	})
}

func (c *Context) CloudFront() *cloudfront.Client {
	return cloudfront.NewFromConfig(*c.cfg)
}

func (c *Context) Beanstalk() *elasticbeanstalk.Client {
	return elasticbeanstalk.NewFromConfig(*c.cfg)
}

func (c *Context) Logs() *cloudwatchlogs.Client {
	return cloudwatchlogs.NewFromConfig(*c.cfg)
}

func (c *Context) STS() *sts.Client {
	return sts.NewFromConfig(*c.cfg)
}

func (c *Context) SSM() *ssm.Client {
	return ssm.NewFromConfig(*c.cfg)
}

func (c *Context) SecurityHub() *securityhub.Client {
	return securityhub.NewFromConfig(*c.cfg)
}

func (c *Context) RDS() *rds.Client {
	return rds.NewFromConfig(*c.cfg)
}

func (c *Context) CodeDeploy() *codedeploy.Client {
	return codedeploy.NewFromConfig(*c.cfg)
}
