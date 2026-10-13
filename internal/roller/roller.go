package roller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// DigestSource resolves the digest currently published under an image reference. It is an
// interface so the decision logic can be tested without a registry.
type DigestSource interface {
	Digest(ctx context.Context, ref string) (string, error)
}

// Result is the outcome of one check.
type Result struct {
	Deployment string
	Tag        string
	Applied    string // digest the roller had last applied ("" if never)
	Digest     string // digest currently published in the registry
	Rolled     bool
	Reason     string
}

// Roller reconciles one Deployment against one registry tag.
type Roller struct {
	cfg    Config
	kube   kubernetes.Interface
	digest DigestSource
	log    *slog.Logger

	// Serialises runs so a webhook storm cannot patch the same Deployment concurrently. Held for
	// the duration of a check: a get, a head, and at most one patch.
	mu sync.Mutex
}

// New builds a Roller with a real Kubernetes client and registry resolver.
func New(cfg Config, log *slog.Logger) (*Roller, error) {
	kube, err := newKubeClient()
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return &Roller{cfg: cfg, kube: kube, digest: Registry{}, log: log}, nil
}

// RunOnce performs a single check. If the registry digest differs from the one recorded on the
// Deployment, it patches the pod-template annotation, which forces a rollout.
//
// Only that one annotation changes. The container image keeps the moving tag (e.g. `edge`), so the
// roller never touches a field the Helm release owns and cannot fight it.
func (r *Roller) RunOnce(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	res := Result{Deployment: r.cfg.Target(), Tag: r.cfg.Tag}

	dep, err := r.kube.AppsV1().Deployments(r.cfg.Namespace).Get(ctx, r.cfg.Deployment, metav1.GetOptions{})
	if err != nil {
		return res, fmt.Errorf("get deployment %s: %w", res.Deployment, err)
	}

	digest, err := r.digest.Digest(ctx, r.cfg.Ref())
	if err != nil {
		return res, fmt.Errorf("resolve %s: %w", r.cfg.Ref(), err)
	}
	res.Digest = digest
	res.Applied = dep.Spec.Template.Annotations[r.cfg.Annotation]

	if res.Applied == digest {
		res.Reason = "digest already applied"
		return res, nil
	}

	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{r.cfg.Annotation: digest},
				},
			},
		},
	})
	if err != nil {
		return res, err
	}

	if _, err := r.kube.AppsV1().Deployments(r.cfg.Namespace).Patch(
		ctx,
		r.cfg.Deployment,
		types.MergePatchType,
		patch,
		metav1.PatchOptions{FieldManager: "sighup"},
	); err != nil {
		return res, fmt.Errorf("patch deployment %s: %w", res.Deployment, err)
	}

	res.Rolled = true
	if res.Applied == "" {
		res.Reason = "no digest applied yet"
	} else {
		res.Reason = "digest changed"
	}
	return res, nil
}

// check runs RunOnce under the lock and logs the outcome. Shared by the poll loop and the webhook
// handler.
func (r *Roller) check(ctx context.Context, trigger string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	res, err := r.RunOnce(ctx)
	if err != nil {
		r.log.Error("check failed", "trigger", trigger, "error", err)
		return
	}

	if res.Rolled {
		r.log.Info("rolled deployment",
			"trigger", trigger,
			"deployment", res.Deployment,
			"tag", res.Tag,
			"from", res.Applied,
			"to", res.Digest,
			"reason", res.Reason,
		)
		return
	}

	r.log.Debug("no change",
		"trigger", trigger,
		"deployment", res.Deployment,
		"digest", res.Digest,
	)
}
