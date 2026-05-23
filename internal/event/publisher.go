// Package event publishes named events into silo's event hub via the
// SDK's RuntimeHost client. Failures are logged.
package event

import (
	"context"

	"github.com/hashicorp/go-hclog"

	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimehost"
)

// Publisher wraps a *runtimehost.Client. Construct once at plugin startup;
// safe for concurrent use.
type Publisher struct {
	host   *runtimehost.Client
	logger hclog.Logger
}

// New builds a Publisher. A nil host is tolerated (logs and skips).
func New(host *runtimehost.Client, logger hclog.Logger) *Publisher {
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	return &Publisher{host: host, logger: logger}
}

// Publish broadcasts an event. The host prefixes the name with plugin.<id>.
func (p *Publisher) Publish(ctx context.Context, name string, payload map[string]any) {
	if p == nil || p.host == nil {
		if p != nil {
			p.logger.Warn("host not bound; skipping event", "name", name)
		}
		return
	}
	if err := p.host.PublishEvent(ctx, name, payload); err != nil {
		p.logger.Warn("publish event", "name", name, "err", err)
	}
}
