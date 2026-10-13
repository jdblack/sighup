package roller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
	Target  string // namespaced Deployment identity
	Tag     string
	Applied string // digest the roller had last applied ("" if never)
	Digest  string // digest currently published in the registry
	Rolled  bool
	Reason  string
	Err     error // non-nil when the check could not complete
}

// Roller reconciles ONE Deployment against ONE registry tag. Each target gets its own Roller, and
// therefore its own mutex: a slow check on one Deployment must never stall another.
type Roller struct {
	registry string
	target   Target
	timeout  time.Duration
	kube     kubernetes.Interface
	digest   DigestSource
	log      *slog.Logger

	// Serialises runs for THIS target so a webhook storm cannot patch the same Deployment
	// concurrently. Held for the duration of a check: a get, a head, and at most one patch.
	mu sync.Mutex
}

// RunOnce performs a single check for this target. If the registry digest differs from the one
// recorded on the Deployment, it patches the pod-template annotation, which forces a rollout.
//
// Only that one annotation changes. The container image keeps the moving tag (e.g. `edge`), so the
// roller never touches a field the Helm release owns and cannot fight it.
func (r *Roller) RunOnce(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	ann := r.target.AnnotationKey()
	res := Result{Target: r.target.String(), Tag: r.target.Tag}

	dep, err := r.kube.AppsV1().Deployments(r.target.Namespace).Get(ctx, r.target.Deployment, metav1.GetOptions{})
	if err != nil {
		return res, fmt.Errorf("get deployment %s: %w", res.Target, err)
	}

	digest, err := r.digest.Digest(ctx, r.target.Ref(r.registry))
	if err != nil {
		return res, fmt.Errorf("resolve %s: %w", r.target.Ref(r.registry), err)
	}
	res.Digest = digest
	res.Applied = dep.Spec.Template.Annotations[ann]

	if res.Applied == digest {
		res.Reason = "digest already applied"
		return res, nil
	}

	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{ann: digest},
				},
			},
		},
	})
	if err != nil {
		return res, err
	}

	if _, err := r.kube.AppsV1().Deployments(r.target.Namespace).Patch(
		ctx,
		r.target.Deployment,
		types.MergePatchType,
		patch,
		metav1.PatchOptions{FieldManager: "sighup"},
	); err != nil {
		return res, fmt.Errorf("patch deployment %s: %w", res.Target, err)
	}

	res.Rolled = true
	if res.Applied == "" {
		res.Reason = "no digest applied yet"
	} else {
		res.Reason = "digest changed"
	}
	return res, nil
}

// check runs RunOnce under this target's lock and logs the outcome. Shared by the poll loop and the
// webhook handler.
func (r *Roller) check(ctx context.Context, trigger string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	res, err := r.RunOnce(ctx)
	if err != nil {
		r.log.Error("check failed", "trigger", trigger, "target", r.target.String(), "error", err)
		return
	}

	if res.Rolled {
		r.log.Info("rolled deployment",
			"trigger", trigger,
			"target", res.Target,
			"tag", res.Tag,
			"from", res.Applied,
			"to", res.Digest,
			"reason", res.Reason,
		)
		return
	}

	r.log.Debug("no change",
		"trigger", trigger,
		"target", res.Target,
		"digest", res.Digest,
	)
}
