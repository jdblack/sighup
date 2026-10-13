package roller

import (
	"context"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// mapDigest resolves the refs it knows and fails the rest, so a test can make one target succeed
// and another fail.
type mapDigest map[string]string

func (m mapDigest) Digest(_ context.Context, ref string) (string, error) {
	d, ok := m[ref]
	if !ok {
		return "", fmt.Errorf("no such ref %q", ref)
	}
	return d, nil
}

const testRegistry = "harbor.example"

func ref(repository, tag string) string { return testRegistry + "/" + repository + ":" + tag }

// target builds a target watching ref(repository, tag) and rolling Deployment deployment.
func target(namespace, deployment, repository, tag string) Target {
	return Target{Repository: repository, Tag: tag, Namespace: namespace, Deployment: deployment}
}

func newTestEngine(t *testing.T, targets []Target, digests mapDigest) (*Engine, *fake.Clientset) {
	t.Helper()

	objs := make([]runtime.Object, 0, len(targets))
	for _, tg := range targets {
		objs = append(objs, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: tg.Deployment, Namespace: tg.Namespace},
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{}},
		})
	}
	kube := fake.NewClientset(objs...)

	cfg := Config{
		Registry:      testRegistry,
		Targets:       targets,
		Timeout:       5 * time.Second,
		WebhookHeader: "Authorization",
		Listen:        ":0",
	}

	return newEngine(cfg, kube, digests, slog.New(slog.NewTextHandler(io.Discard, nil))), kube
}

func annotationOf(t *testing.T, kube *fake.Clientset, namespace, name string) string {
	t.Helper()

	dep, err := kube.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment %s/%s: %v", namespace, name, err)
	}
	return dep.Spec.Template.Annotations[DefaultAnnotation]
}

func TestEngineRollsOnNewDigest(t *testing.T) {
	app := target("app", "someapp", "library/someapp", "edge")
	e, kube := newTestEngine(t, []Target{app}, mapDigest{ref("library/someapp", "edge"): "sha256:aaa"})

	results := e.RunOnce(context.Background())
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("RunOnce: %v", results[0].Err)
	}
	if !results[0].Rolled {
		t.Fatalf("expected a roll, got %+v", results[0])
	}
	if got := annotationOf(t, kube, "app", "someapp"); got != "sha256:aaa" {
		t.Fatalf("annotation = %q, want sha256:aaa", got)
	}
}

func TestEngineIsIdempotent(t *testing.T) {
	app := target("app", "someapp", "library/someapp", "edge")
	e, _ := newTestEngine(t, []Target{app}, mapDigest{ref("library/someapp", "edge"): "sha256:aaa"})

	if res := e.RunOnce(context.Background()); res[0].Err != nil {
		t.Fatalf("first run: %v", res[0].Err)
	}

	res := e.RunOnce(context.Background())
	if res[0].Rolled {
		t.Fatalf("second run should be a no-op, got %+v", res[0])
	}
	if res[0].Reason != "digest already applied" {
		t.Fatalf("reason = %q", res[0].Reason)
	}
}

func TestEngineRollsAgainWhenDigestMoves(t *testing.T) {
	app := target("app", "someapp", "library/someapp", "edge")
	digests := mapDigest{ref("library/someapp", "edge"): "sha256:aaa"}
	e, kube := newTestEngine(t, []Target{app}, digests)

	if res := e.RunOnce(context.Background()); res[0].Err != nil {
		t.Fatalf("first run: %v", res[0].Err)
	}

	digests[ref("library/someapp", "edge")] = "sha256:bbb"

	res := e.RunOnce(context.Background())
	if !res[0].Rolled {
		t.Fatalf("expected a second roll, got %+v", res[0])
	}
	if got := annotationOf(t, kube, "app", "someapp"); got != "sha256:bbb" {
		t.Fatalf("annotation = %q, want sha256:bbb", got)
	}
}

// One target's failure must not stop the others: they are independent Rollers.
func TestEngineIsolatesFailures(t *testing.T) {
	good := target("app", "good", "library/good", "edge")
	bad := target("app", "bad", "library/bad", "edge")
	e, kube := newTestEngine(t, []Target{good, bad}, mapDigest{ref("library/good", "edge"): "sha256:aaa"})

	results := e.RunOnce(context.Background())

	if results[0].Err != nil || !results[0].Rolled {
		t.Fatalf("good target: %+v", results[0])
	}
	if results[1].Err == nil {
		t.Fatalf("bad target should have failed: %+v", results[1])
	}
	if got := annotationOf(t, kube, "app", "good"); got != "sha256:aaa" {
		t.Fatalf("good annotation = %q, want sha256:aaa", got)
	}
	if got := annotationOf(t, kube, "app", "bad"); got != "" {
		t.Fatalf("bad annotation = %q, want empty", got)
	}
}

func TestWebhookRejectsBadSecret(t *testing.T) {
	app := target("app", "someapp", "library/someapp", "edge")
	e, _ := newTestEngine(t, []Target{app}, mapDigest{})
	e.cfg.WebhookSecret = "correct-horse"

	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "wrong")
	w := httptest.NewRecorder()
	e.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestWebhookIgnoresUntrackedTag(t *testing.T) {
	app := target("app", "someapp", "library/someapp", "edge")
	e, _ := newTestEngine(t, []Target{app}, mapDigest{})

	body := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"sha-abc1234"}],"repository":{"repo_full_name":"library/someapp"}}}`
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	w := httptest.NewRecorder()
	e.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (ignored)", w.Code)
	}
}

func TestWebhookIgnoresOtherRepository(t *testing.T) {
	app := target("app", "someapp", "library/someapp", "edge")
	e, kube := newTestEngine(t, []Target{app}, mapDigest{ref("library/someapp", "edge"): "sha256:aaa"})

	body := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"edge"}],"repository":{"repo_full_name":"library/other"}}}`
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	w := httptest.NewRecorder()
	e.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (ignored)", w.Code)
	}
	if got := annotationOf(t, kube, "app", "someapp"); got != "" {
		t.Fatalf("annotation = %q, want empty (untouched)", got)
	}
}

// One push event names one repo:tag; every target watching it must roll -- that is the fan-out.
func TestWebhookFanOutRollsEveryMatch(t *testing.T) {
	web := target("app", "web", "library/someapp", "edge")
	api := target("app", "api", "library/someapp", "edge")
	other := target("app", "other", "library/other", "edge")

	digests := mapDigest{
		ref("library/someapp", "edge"): "sha256:aaa",
		ref("library/other", "edge"):   "sha256:bbb",
	}
	e, kube := newTestEngine(t, []Target{web, api, other}, digests)

	body := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"edge","digest":"sha256:aaa"}],"repository":{"repo_full_name":"library/someapp"}}}`
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	w := httptest.NewRecorder()
	e.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", w.Code)
	}

	// The patch happens in the background; wait briefly for it to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if annotationOf(t, kube, "app", "web") == "sha256:aaa" && annotationOf(t, kube, "app", "api") == "sha256:aaa" {
			if got := annotationOf(t, kube, "app", "other"); got != "" {
				t.Fatalf("non-matching target was rolled: annotation = %q", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("webhook did not roll every matching target")
}

func TestParseTargets(t *testing.T) {
	valid := `[
		{"repository":"library/a","tag":"edge","namespace":"app","deployment":"a"},
		{"repository":"library/b","tag":"stable","namespace":"other","deployment":"b"}
	]`

	targets, err := parseTargets(valid)
	if err != nil {
		t.Fatalf("parseTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	if got, want := targets[1].Ref(testRegistry), "harbor.example/library/b:stable"; got != want {
		t.Fatalf("Ref() = %q, want %q", got, want)
	}

	for name, raw := range map[string]string{
		"empty":         "",
		"blank":         "   ",
		"empty array":   "[]",
		"missing field": `[{"repository":"library/a","tag":"edge","namespace":"app"}]`,
		"duplicate":     `[{"repository":"a","tag":"e","namespace":"n","deployment":"d"},{"repository":"b","tag":"e","namespace":"n","deployment":"d"}]`,
		"bad json":      `{}`,
	} {
		if _, err := parseTargets(raw); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("SIGHUP_REGISTRY", testRegistry)
	t.Setenv("SIGHUP_TARGETS", `[{"repository":"library/someapp","tag":"edge","namespace":"app","deployment":"someapp"}]`)

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(cfg.Targets))
	}
	if got, want := cfg.Targets[0].Ref(cfg.Registry), "harbor.example/library/someapp:edge"; got != want {
		t.Fatalf("Ref() = %q, want %q", got, want)
	}
	if cfg.PollInterval != 15*time.Minute {
		t.Fatalf("PollInterval = %v, want 15m", cfg.PollInterval)
	}

	t.Setenv("SIGHUP_TARGETS", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error when SIGHUP_TARGETS is empty")
	}

	t.Setenv("SIGHUP_REGISTRY", "")
	t.Setenv("SIGHUP_TARGETS", `[{"repository":"library/someapp","tag":"edge","namespace":"app","deployment":"someapp"}]`)
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error when SIGHUP_REGISTRY is empty")
	}
}
