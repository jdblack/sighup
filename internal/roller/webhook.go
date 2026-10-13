package roller

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// maxBody caps the webhook payload; Harbor's is a few KB.
const maxBody = 1 << 20

// harborEvent is the subset of Harbor's default webhook payload the roller needs. The CloudEvents
// format is not handled: configure the Harbor webhook for "Default".
type harborEvent struct {
	Type      string `json:"type"`
	EventData struct {
		Resources []struct {
			Digest string `json:"digest"`
			Tag    string `json:"tag"`
		} `json:"resources"`
		Repository struct {
			RepoFullName string `json:"repo_full_name"`
		} `json:"repository"`
	} `json:"event_data"`
}

// Handler returns the HTTP surface: probes and the Harbor webhook.
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /hook", e.webhook)

	return mux
}

// matching returns the targets a push event concerns: those watching the pushed repository whose
// tag is among the pushed tags. A single event can fan out to several Deployments that share an
// image, which is the whole point of running more than one target.
func (e *Engine) matching(ev harborEvent) []*Roller {
	if ev.Type != "PUSH_ARTIFACT" {
		return nil
	}

	var out []*Roller
	for _, r := range e.rollers {
		if r.target.Repository != ev.EventData.Repository.RepoFullName {
			continue
		}
		for _, res := range ev.EventData.Resources {
			if res.Tag == r.target.Tag {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

func (e *Engine) webhook(w http.ResponseWriter, req *http.Request) {
	if secret := e.cfg.WebhookSecret; secret != "" {
		got := req.Header.Get(e.cfg.WebhookHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var ev harborEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBody)).Decode(&ev); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	matched := e.matching(ev)
	if len(matched) == 0 {
		// A push we do not track: acknowledge and move on.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Harbor retries a webhook only three times with a 3s client timeout, so acknowledge now and
	// do the work in the background. The poll loop is the safety net for anything missed.
	w.WriteHeader(http.StatusAccepted)
	go e.check(context.Background(), "webhook", matched)
}

// Serve runs the poll loop and the webhook listener until ctx is cancelled.
func (e *Engine) Serve(ctx context.Context) error {
	srv := &http.Server{
		Addr:              e.cfg.Listen,
		Handler:           e.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		e.log.Info("listening",
			"addr", e.cfg.Listen,
			"targets", len(e.rollers),
			"registry", e.cfg.Registry,
			"poll_interval", e.cfg.PollInterval.String(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Reconcile immediately at startup, so a restart catches anything missed while down.
	e.check(ctx, "startup", e.rollers)

	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		case err := <-errCh:
			return err
		case <-ticker.C:
			e.check(ctx, "poll", e.rollers)
		}
	}
}
