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
		addr        string
		kubeconfig  string
		repoPath    string
		githubToken string
		githubRepo  string
	)

	flag.StringVar(&addr, "addr", ":8090", "HTTP listen address")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (uses in-cluster config if empty)")
	flag.StringVar(&repoPath, "repo-path", ".", "Path to the GitOps repo root")
	flag.StringVar(&githubToken, "github-token", "", "GitHub personal access token (falls back to GITHUB_PAT env)")
	flag.StringVar(&githubRepo, "github-repo", "dusttostars/vcluster-ephemeral-envs", "GitHub repo (owner/name)")
	flag.Parse()

	if githubToken == "" {
		githubToken = os.Getenv("GITHUB_PAT")
	}

	config, err := buildConfig(kubeconfig)
	if err != nil {
		log.Fatalf("building kube config: %v", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("creating dynamic client: %v", err)
	}

	h := &handler{
		client:      client,
		repoPath:    repoPath,
		githubToken: githubToken,
		githubRepo:  githubRepo,
	}

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/environments", h.listEnvironments)
	mux.HandleFunc("POST /api/environments", h.createEnvironment)
	mux.HandleFunc("DELETE /api/environments/{tenant}/{name}", h.deleteEnvironment)
	mux.HandleFunc("GET /api/tenants", h.listTenants)
	mux.HandleFunc("GET /api/tenants/details", h.listTenantsDetailed)
	mux.HandleFunc("POST /api/tenants", h.createTenant)
	mux.HandleFunc("GET /api/settings", h.getSettings)

	// GitHub routes
	mux.HandleFunc("GET /api/github/branches", h.listBranches)

	// App proxy and discovery routes
	mux.HandleFunc("GET /api/environments/{tenant}/{name}/apps", h.listDeployedApps)
	mux.HandleFunc("GET /app/{tenant}/{name}/{rest...}", h.proxyApp)

	// Deploy routes
	mux.HandleFunc("POST /api/environments/{tenant}/{name}/deploy", h.deployBranch)

	// Helm chart routes
	mux.HandleFunc("GET /api/charts/catalog", h.chartCatalog)
	mux.HandleFunc("GET /api/environments/{tenant}/{name}/charts", h.listCharts)
	mux.HandleFunc("POST /api/environments/{tenant}/{name}/charts", h.installChart)
	mux.HandleFunc("DELETE /api/environments/{tenant}/{name}/charts/{release}", h.uninstallChart)

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
		WriteTimeout: 5 * time.Minute,
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
