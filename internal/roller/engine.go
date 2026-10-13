package roller

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/client-go/kubernetes"
)

// Engine owns the set of Targets: one Roller per target, over a shared registry and API client. It
// is what the binary actually runs -- `once` walks every target, `serve` polls them and routes
// webhooks to the matching subset.
type Engine struct {
	cfg     Config
	rollers []*Roller
	log     *slog.Logger
}

// New builds an Engine with a real Kubernetes client and registry resolver.
func New(cfg Config, log *slog.Logger) (*Engine, error) {
	kube, err := newKubeClient()
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return newEngine(cfg, kube, Registry{}, log), nil
}

// newEngine is the test seam: a caller-supplied client and digest source.
func newEngine(cfg Config, kube kubernetes.Interface, digest DigestSource, log *slog.Logger) *Engine {
	rollers := make([]*Roller, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		rollers = append(rollers, &Roller{
			registry: cfg.Registry,
			target:   t,
			timeout:  cfg.Timeout,
			kube:     kube,
			digest:   digest,
			log:      log,
		})
	}
	return &Engine{cfg: cfg, rollers: rollers, log: log}
}

// RunOnce checks every target exactly once. Failures are per-target: one unreachable target does
// not stop the rest, and each Result carries its own Err.
func (e *Engine) RunOnce(ctx context.Context) []Result {
	results := make([]Result, 0, len(e.rollers))
	for _, r := range e.rollers {
		res, err := r.RunOnce(ctx)
		res.Err = err
		results = append(results, res)
	}
	return results
}

// check runs a set of rollers under their own locks, logging each outcome.
func (e *Engine) check(ctx context.Context, trigger string, rollers []*Roller) {
	for _, r := range rollers {
		r.check(ctx, trigger)
	}
}
