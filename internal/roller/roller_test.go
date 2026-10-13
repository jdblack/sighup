package roller

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type stubDigest struct {
	digest string
	err    error
}

func (s stubDigest) Digest(context.Context, string) (string, error) { return s.digest, s.err }

func newTestRoller(t *testing.T, digest string) (*Roller, *fake.Clientset) {
	t.Helper()

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "someapp", Namespace: "app"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{}},
	}
	kube := fake.NewClientset(dep)

	return &Roller{
		cfg: Config{
			Registry:   "harbor.example",
			Repository: "library/someapp",
			Tag:        "edge",
			Namespace:  "app",
			Deployment: "someapp",
			Annotation: DefaultAnnotation,
		},
		kube:   kube,
		digest: stubDigest{digest: digest},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, kube
}

func annotationOf(t *testing.T, kube *fake.Clientset) string {
	t.Helper()

	dep, err := kube.AppsV1().Deployments("app").Get(context.Background(), "someapp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return dep.Spec.Template.Annotations[DefaultAnnotation]
}

func TestRunOnceRollsOnNewDigest(t *testing.T) {
	r, kube := newTestRoller(t, "sha256:aaa")

	res, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !res.Rolled {
		t.Fatalf("expected a roll, got %+v", res)
	}
	if got := annotationOf(t, kube); got != "sha256:aaa" {
		t.Fatalf("annotation = %q, want sha256:aaa", got)
	}
}

func TestRunOnceIsIdempotent(t *testing.T) {
	r, _ := newTestRoller(t, "sha256:aaa")

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	res, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Rolled {
		t.Fatalf("second run should be a no-op, got %+v", res)
	}
	if res.Reason != "digest already applied" {
		t.Fatalf("reason = %q", res.Reason)
	}
}

func TestRunOnceRollsAgainWhenDigestMoves(t *testing.T) {
	r, kube := newTestRoller(t, "sha256:aaa")

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	r.digest = stubDigest{digest: "sha256:bbb"}

	res, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !res.Rolled {
		t.Fatalf("expected a second roll, got %+v", res)
	}
	if got := annotationOf(t, kube); got != "sha256:bbb" {
		t.Fatalf("annotation = %q, want sha256:bbb", got)
	}
}

func TestRunOnceRejectsBadDigestSource(t *testing.T) {
	r, _ := newTestRoller(t, "sha256:aaa")
	r.digest = stubDigest{err: context.DeadlineExceeded}

	if _, err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("expected an error from the digest source")
	}
}

func TestWebhookRejectsBadSecret(t *testing.T) {
	r, _ := newTestRoller(t, "sha256:aaa")
	r.cfg.WebhookSecret = "correct-horse"

	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "wrong")
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestWebhookIgnoresUntrackedTag(t *testing.T) {
	r, _ := newTestRoller(t, "sha256:aaa")

	body := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"sha-abc1234"}],"repository":{"repo_full_name":"library/someapp"}}}`
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (ignored)", w.Code)
	}
}

func TestWebhookAcceptsTrackedTagAndRolls(t *testing.T) {
	r, kube := newTestRoller(t, "sha256:aaa")

	body := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"edge","digest":"sha256:aaa"}],"repository":{"repo_full_name":"library/someapp"}}}`
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", w.Code)
	}

	// The patch happens in the background; wait briefly for it to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if annotationOf(t, kube) == "sha256:aaa" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("webhook did not apply the digest")
}

func TestConfigFromEnvRequiresCoordinates(t *testing.T) {
	t.Setenv("SIGHUP_REGISTRY", "harbor.example")
	t.Setenv("SIGHUP_REPOSITORY", "library/someapp")
	t.Setenv("SIGHUP_TAG", "edge")
	t.Setenv("SIGHUP_NAMESPACE", "app")
	t.Setenv("SIGHUP_DEPLOYMENT", "someapp")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if got, want := cfg.Ref(), "harbor.example/library/someapp:edge"; got != want {
		t.Fatalf("Ref() = %q, want %q", got, want)
	}
	if cfg.PollInterval != 15*time.Minute {
		t.Fatalf("PollInterval = %v, want 15m", cfg.PollInterval)
	}

	t.Setenv("SIGHUP_DEPLOYMENT", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error when SIGHUP_DEPLOYMENT is empty")
	}
}
