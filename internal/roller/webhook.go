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
func (r *Roller) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /hook", r.webhook)

	return mux
}

// relevant reports whether an event is a push to the tracked repository and tag.
func (r *Roller) relevant(ev harborEvent) bool {
	if ev.Type != "PUSH_ARTIFACT" {
		return false
	}
	if ev.EventData.Repository.RepoFullName != r.cfg.Repository {
		return false
	}
	for _, res := range ev.EventData.Resources {
		if res.Tag == r.cfg.Tag {
			return true
		}
	}
	return false
}

func (r *Roller) webhook(w http.ResponseWriter, req *http.Request) {
	if secret := r.cfg.WebhookSecret; secret != "" {
		got := req.Header.Get(r.cfg.WebhookHeader)
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

	if !r.relevant(ev) {
		// A push we do not track: acknowledge and move on.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Harbor retries a webhook only three times with a 3s client timeout, so acknowledge now and
	// do the work in the background. The poll loop is the safety net for anything missed.
	w.WriteHeader(http.StatusAccepted)
	go r.check(context.Background(), "webhook")
}

// Serve runs the poll loop and the webhook listener until ctx is cancelled.
func (r *Roller) Serve(ctx context.Context) error {
	srv := &http.Server{
		Addr:              r.cfg.Listen,
		Handler:           r.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		r.log.Info("listening",
			"addr", r.cfg.Listen,
			"deployment", r.cfg.Target(),
			"ref", r.cfg.Ref(),
			"poll_interval", r.cfg.PollInterval.String(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Reconcile immediately at startup, so a restart catches anything missed while down.
	r.check(ctx, "startup")

	ticker := time.NewTicker(r.cfg.PollInterval)
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
			r.check(ctx, "poll")
		}
	}
}
