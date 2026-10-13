package roller

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// newKubeClient builds a client from the in-cluster configuration, falling back to a kubeconfig so
// the same binary can run against a cluster during development.
func newKubeClient() (kubernetes.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()

	cfg, err := clientcmd.
		NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).
		ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.UserAgent = "sighup"

	return kubernetes.NewForConfig(cfg)
}
