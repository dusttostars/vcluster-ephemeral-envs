package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/willsbctm/vcluster-ephemeral-envs/internal/cleanup"
)

func main() {
	var (
		kubeconfig string
		repoPath   string
		interval   time.Duration
		maxAge     time.Duration
	)

	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (uses in-cluster config if empty)")
	flag.StringVar(&repoPath, "repo-path", "/repo", "Path to the local GitOps repo clone")
	flag.DurationVar(&interval, "interval", 60*time.Second, "Reconcile interval")
	flag.DurationVar(&maxAge, "max-age", 24*time.Hour, "Maximum age for any ephemeral environment")
	flag.Parse()

	config, err := buildConfig(kubeconfig)
	if err != nil {
		log.Fatalf("building kube config: %v", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("creating dynamic client: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	controller := cleanup.NewController(client, repoPath, interval, maxAge)
	if err := controller.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("controller error: %v", err)
	}
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	// Try in-cluster first, fall back to default kubeconfig location.
	config, err := rest.InClusterConfig()
	if err != nil {
		home, _ := os.UserHomeDir()
		return clientcmd.BuildConfigFromFlags("", home+"/.kube/config")
	}
	return config, nil
}
