package roller

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// DefaultAnnotation is the pod-template annotation the roller owns. Its value is the digest the
// roller last asked for, so writing it does both jobs at once: it records the intent, and because
// it lives in the pod template, changing it forces a rollout.
const DefaultAnnotation = "sighup/applied"

// Target is one Deployment to roll, and the moving tag that drives it.
type Target struct {
	Repository string `json:"repository"` // e.g. library/someapp
	Tag        string `json:"tag"`        // e.g. edge
	Namespace  string `json:"namespace"`  //
	Deployment string `json:"deployment"` //
	// Annotation overrides DefaultAnnotation. One annotation key per Deployment is enough, so this
	// exists only so two sighups on the same Deployment cannot collide.
	Annotation string `json:"annotation,omitempty"`
}

// Ref is the image reference whose digest is tracked, e.g. harbor.example.com/library/someapp:edge.
func (t Target) Ref(registry string) string { return registry + "/" + t.Repository + ":" + t.Tag }

// String is the workload's namespaced identity, used in logs.
func (t Target) String() string { return t.Namespace + "/" + t.Deployment }

// AnnotationKey is the pod-template annotation this target owns.
func (t Target) AnnotationKey() string {
	if t.Annotation == "" {
		return DefaultAnnotation
	}
	return t.Annotation
}

// Config is the whole of the roller's behaviour, taken from the environment.
type Config struct {
	// What to watch: one registry host, shared by every target.
	Registry string // e.g. ghcr.io

	// What to roll: one entry per Deployment.
	Targets []Target

	// serve mode.
	Listen        string
	PollInterval  time.Duration
	Timeout       time.Duration
	WebhookSecret string
	WebhookHeader string
}

// ConfigFromEnv reads and validates the configuration.
func ConfigFromEnv() (Config, error) {
	c := Config{
		Registry:      os.Getenv("SIGHUP_REGISTRY"),
		Listen:        envOr("SIGHUP_LISTEN", ":8080"),
		WebhookSecret: os.Getenv("SIGHUP_WEBHOOK_SECRET"),
		WebhookHeader: envOr("SIGHUP_WEBHOOK_HEADER", "Authorization"),
	}

	if c.Registry == "" {
		return Config{}, fmt.Errorf("SIGHUP_REGISTRY is required")
	}

	targets, err := parseTargets(os.Getenv("SIGHUP_TARGETS"))
	if err != nil {
		return Config{}, err
	}
	c.Targets = targets

	poll, err := time.ParseDuration(envOr("SIGHUP_POLL_INTERVAL", "15m"))
	if err != nil {
		return Config{}, fmt.Errorf("SIGHUP_POLL_INTERVAL: %w", err)
	}
	c.PollInterval = poll

	timeout, err := time.ParseDuration(envOr("SIGHUP_TIMEOUT", "30s"))
	if err != nil {
		return Config{}, fmt.Errorf("SIGHUP_TIMEOUT: %w", err)
	}
	c.Timeout = timeout

	return c, nil
}

// parseTargets decodes SIGHUP_TARGETS, a JSON array of Target. Each target must be complete and no
// two may name the same Deployment -- that is the one-writer-per-Deployment invariant, enforced up
// front rather than discovered later as an annotation race.
func parseTargets(raw string) ([]Target, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("SIGHUP_TARGETS is required (a JSON array of {repository,tag,namespace,deployment})")
	}

	var targets []Target
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, fmt.Errorf("SIGHUP_TARGETS: %w", err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("SIGHUP_TARGETS: at least one target is required")
	}

	seen := make(map[string]bool, len(targets))
	for i, t := range targets {
		for _, f := range []struct{ name, val string }{
			{"repository", t.Repository},
			{"tag", t.Tag},
			{"namespace", t.Namespace},
			{"deployment", t.Deployment},
		} {
			if f.val == "" {
				return nil, fmt.Errorf("SIGHUP_TARGETS[%d].%s is required", i, f.name)
			}
		}
		if seen[t.String()] {
			return nil, fmt.Errorf("SIGHUP_TARGETS: duplicate target %s", t)
		}
		seen[t.String()] = true
	}

	return targets, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
