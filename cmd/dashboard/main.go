package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed static
var staticFiles embed.FS

func main() {
	var (
		addr       string
		kubeconfig string
		repoPath   string
	)

	flag.StringVar(&addr, "addr", ":8090", "HTTP listen address")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (uses in-cluster config if empty)")
	flag.StringVar(&repoPath, "repo-path", ".", "Path to the GitOps repo root")
	flag.Parse()

	config, err := buildConfig(kubeconfig)
	if err != nil {
		log.Fatalf("building kube config: %v", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("creating dynamic client: %v", err)
	}

	h := &handler{
		client:   client,
		repoPath: repoPath,
	}

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/environments", h.listEnvironments)
	mux.HandleFunc("POST /api/environments", h.createEnvironment)
	mux.HandleFunc("DELETE /api/environments/{tenant}/{name}", h.deleteEnvironment)
	mux.HandleFunc("GET /api/tenants", h.listTenants)

	// Static files
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("loading static files: %v", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("dashboard listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		home, _ := os.UserHomeDir()
		kubeconfig = fmt.Sprintf("%s/.kube/config", home)
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return config, nil
}
