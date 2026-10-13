package roller

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Registry resolves digests against an OCI/Docker v2 registry. Harbor speaks v2, so this is
// ordinary Docker Hub-style resolution -- including the bearer-token handshake -- and anonymous
// access works for a public project such as `library`.
type Registry struct{}

// Digest returns the manifest digest currently published for ref, e.g. "sha256:1a2b...".
func (Registry) Digest(ctx context.Context, ref string) (string, error) {
	r, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return "", fmt.Errorf("parse reference %q: %w", ref, err)
	}

	desc, err := remote.Head(r, remote.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("head %q: %w", ref, err)
	}

	return desc.Digest.String(), nil
}
