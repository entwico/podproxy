package kube

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewKubeClient builds a *rest.Config and *kubernetes.Clientset from the given
// kubeconfig path and optional context. If kubeconfigPath is empty, it falls
// back to the default location (~/.kube/config) or in-cluster config.
// If kubeContext is empty, the kubeconfig's current-context is used.
func NewKubeClient(kubeconfigPath, kubeContext string) (*rest.Config, *kubernetes.Clientset, error) {
	if kubeconfigPath == "" {
		kubeconfigPath = defaultKubeconfig()
	}

	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		// try in-cluster config as fallback.
		kubeconfigErr := err

		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("unable to load kubeconfig %q (%v) or in-cluster config: %w", kubeconfigPath, kubeconfigErr, err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	return config, clientset, nil
}

// ResolveServiceToPod resolves a Kubernetes service to the name of its first
// ready pod endpoint. This is used when the SOCKS5 destination is a service
// rather than a direct pod address.
func ResolveServiceToPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName string) (string, error) {
	// apply a default timeout when the caller hasn't set a deadline
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}

	slices, err := clientset.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + serviceName,
	})
	if err != nil {
		return "", fmt.Errorf("listing endpoint slices for service %s/%s: %w", namespace, serviceName, err)
	}

	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			// nil Ready means the endpoint is ready per the API spec
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}

			if ep.Conditions.Serving != nil && !*ep.Conditions.Serving {
				continue
			}

			if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
				continue
			}

			if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" {
				return ep.TargetRef.Name, nil
			}
		}
	}

	return "", fmt.Errorf("no ready pod endpoints found for service %s/%s", namespace, serviceName)
}

func defaultKubeconfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".kube", "config")
}
