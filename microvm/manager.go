package microvm

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/internal/awsapi/sdkclient"
)

// Manager owns the lifecycle of MicroVMs and images within a single AWS
// credential context. Construct with NewFromConfig (production) or
// NewWithAPI (testing). Manager is safe for concurrent use.
type Manager struct {
	api          awsapi.API
	region       string
	accountID    string
	pollInterval time.Duration
	logger       *slog.Logger
}

// Option configures a Manager at construction time.
type Option func(*Manager)

// WithRegion sets the AWS region the Manager targets.
func WithRegion(r string) Option {
	return func(m *Manager) { m.region = r }
}

// WithAccountID sets the AWS account ID used for constructing resource ARNs.
func WithAccountID(id string) Option {
	return func(m *Manager) { m.accountID = id }
}

// WithPollInterval overrides the polling interval used when waiting for async
// operations (image builds, VM provisioning). Primarily for testing.
func WithPollInterval(d time.Duration) Option {
	return func(m *Manager) { m.pollInterval = d }
}

// WithManagerLogger sets an optional structured logger on the Manager.
// The logger is never used to record shell auth tokens.
func WithManagerLogger(l *slog.Logger) Option {
	return func(m *Manager) { m.logger = l }
}

// NewFromConfig constructs a Manager from the caller-supplied aws.Config.
// The library never resolves ambient credentials; the caller is responsible
// for building cfg (e.g. via config.LoadDefaultConfig or an assumed-role provider).
func NewFromConfig(cfg aws.Config, opts ...Option) *Manager {
	m := &Manager{
		region:       cfg.Region,
		pollInterval: 5 * time.Second,
	}
	m.api = sdkclient.New(cfg)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// NewWithAPI constructs a Manager with an injected API implementation.
// Use for testing with awsapi.Mock; use NewFromConfig in production.
func NewWithAPI(api awsapi.API, opts ...Option) *Manager {
	m := &Manager{
		api:          api,
		pollInterval: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// discardLogger drops all records. It is the default so the library never
// writes to a process-global logger; callers opt in via WithManagerLogger.
var discardLogger = slog.New(slog.DiscardHandler)

// log returns the Manager's logger, or a no-op logger if none was injected.
func (m *Manager) log() *slog.Logger {
	if m.logger != nil {
		return m.logger
	}
	return discardLogger
}

// imageARN constructs the full Lambda MicroVM image ARN from the image name.
func (m *Manager) imageARN(name string) string {
	return "arn:aws:lambda:" + m.region + ":" + m.accountID + ":microvm-image:" + name
}

// poll calls check immediately, then once per interval, until check returns
// done=true (success), check returns an error, or ctx is canceled. There is no
// attempt cap — callers bound the total wait via ctx (e.g. context.WithTimeout).
func (m *Manager) poll(ctx context.Context, check func(context.Context) (done bool, err error)) error {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
