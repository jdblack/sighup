package roller

import (
	"fmt"
	"os"
	"time"
)

// DefaultAnnotation is the pod-template annotation the roller owns. Its value is the digest the
// roller last asked for, so writing it does both jobs at once: it records the intent, and because
// it lives in the pod template, changing it forces a rollout.
const DefaultAnnotation = "sighup.vn.linuxguru.net/applied"

// Config is the whole of the roller's behaviour, taken from the environment.
type Config struct {
	// What to watch.
	Registry   string // e.g. harbor.vn.linuxguru.net
	Repository string // e.g. library/someapp
	Tag        string // e.g. edge

	// What to roll.
	Namespace  string
	Deployment string
	Annotation string

	// serve mode.
	Listen        string
	PollInterval  time.Duration
	Timeout       time.Duration
	WebhookSecret string
	WebhookHeader string
}

// Ref is the image reference whose digest is tracked, e.g. harbor.example.com/library/someapp:edge.
func (c Config) Ref() string { return c.Registry + "/" + c.Repository + ":" + c.Tag }

// Target is the workload's namespaced identity, used in logs.
func (c Config) Target() string { return c.Namespace + "/" + c.Deployment }

// ConfigFromEnv reads and validates the configuration.
func ConfigFromEnv() (Config, error) {
	c := Config{
		Registry:      os.Getenv("SIGHUP_REGISTRY"),
		Repository:    os.Getenv("SIGHUP_REPOSITORY"),
		Tag:           os.Getenv("SIGHUP_TAG"),
		Namespace:     os.Getenv("SIGHUP_NAMESPACE"),
		Deployment:    os.Getenv("SIGHUP_DEPLOYMENT"),
		Annotation:    envOr("SIGHUP_ANNOTATION", DefaultAnnotation),
		Listen:        envOr("SIGHUP_LISTEN", ":8080"),
		WebhookSecret: os.Getenv("SIGHUP_WEBHOOK_SECRET"),
		WebhookHeader: envOr("SIGHUP_WEBHOOK_HEADER", "Authorization"),
	}

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

	for _, req := range []struct{ name, val string }{
		{"SIGHUP_REGISTRY", c.Registry},
		{"SIGHUP_REPOSITORY", c.Repository},
		{"SIGHUP_TAG", c.Tag},
		{"SIGHUP_NAMESPACE", c.Namespace},
		{"SIGHUP_DEPLOYMENT", c.Deployment},
	} {
		if req.val == "" {
			return Config{}, fmt.Errorf("%s is required", req.name)
		}
	}

	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
