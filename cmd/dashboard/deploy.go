package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type deployRequest struct {
	Branch    string `json:"branch"`
	Namespace string `json:"namespace"`
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// slugify converts a branch name to a valid image tag / release name.
func slugify(s string) string {
	slug := slugRe.ReplaceAllString(s, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return strings.ToLower(slug)
}

func (h *handler) deployBranch(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Branch == "" {
		http.Error(w, "branch is required", http.StatusBadRequest)
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	ctx := r.Context()
	slug := slugify(req.Branch)
	imageTag := slug
	imageRepo := "localhost:32000/app"
	imageRef := fmt.Sprintf("%s:%s", imageRepo, imageTag)

	// 1. Clone the branch into a temp directory.
	cloneDir, err := os.MkdirTemp("", "deploy-clone-*")
	if err != nil {
		http.Error(w, fmt.Sprintf("creating temp dir: %v", err), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(cloneDir)

	repoURL := fmt.Sprintf("https://github.com/%s.git", h.githubRepo)
	cloneCmd := exec.CommandContext(ctx, "git", "clone",
		"--depth", "1",
		"--branch", req.Branch,
		repoURL,
		cloneDir,
	)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		log.Printf("git clone failed: %s", string(out))
		http.Error(w, fmt.Sprintf("cloning branch %s: %s", req.Branch, string(out)), http.StatusInternalServerError)
		return
	}

	// 2. Determine build context — prefer app/ subdirectory if it exists.
	buildContext := cloneDir
	dockerfile := "Dockerfile"
	if _, err := os.Stat(filepath.Join(cloneDir, "app", "Dockerfile")); err == nil {
		buildContext = filepath.Join(cloneDir, "app")
	} else {
		for _, candidate := range []string{"Dockerfile", "Dockerfile.cli"} {
			if _, err := os.Stat(filepath.Join(cloneDir, candidate)); err == nil {
				dockerfile = candidate
				break
			}
		}
	}

	// 3. Build the Docker image.
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-t", imageRef,
		"-f", filepath.Join(buildContext, dockerfile),
		buildContext,
	)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		log.Printf("docker build failed: %s", string(out))
		http.Error(w, fmt.Sprintf("building image: %s", string(out)), http.StatusInternalServerError)
		return
	}

	// 4. Push to MicroK8s built-in registry (localhost:32000).
	pushCmd := exec.CommandContext(ctx, "docker", "push", imageRef)
	if out, err := pushCmd.CombinedOutput(); err != nil {
		log.Printf("docker push failed, trying microk8s import: %s", string(out))
		// Fallback: save and import into microk8s containerd.
		saveCmd := exec.CommandContext(ctx, "docker", "save", imageRef)
		importCmd := exec.CommandContext(ctx, "microk8s", "ctr", "image", "import", "-")
		importCmd.Stdin, _ = saveCmd.StdoutPipe()
		if err := importCmd.Start(); err == nil {
			if err := saveCmd.Run(); err == nil {
				importCmd.Wait()
			}
		}
	}

	// 5. Install the app chart into the vcluster.
	kubeconfig, cleanup, err := h.getVClusterKubeconfig(ctx, tenant, name)
	if err != nil {
		http.Error(w, fmt.Sprintf("getting kubeconfig: %v", err), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	chartPath := filepath.Join(h.repoPath, "charts", "app")
	releaseName := fmt.Sprintf("app-%s", slug)

	// Get the short commit SHA from the cloned repo.
	commitCmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "rev-parse", "--short", "HEAD")
	commitOut, _ := commitCmd.Output()
	commitSHA := strings.TrimSpace(string(commitOut))

	helmArgs := []string{
		"upgrade", "--install", releaseName, chartPath,
		"--kubeconfig", kubeconfig,
		"--namespace", req.Namespace,
		"--create-namespace",
		"--set", fmt.Sprintf("image.repository=%s", imageRepo),
		"--set", fmt.Sprintf("image.tag=%s", imageTag),
		"--set", "image.pullPolicy=Always",
		"--set", fmt.Sprintf("gitBranch=%s", req.Branch),
		"--set", fmt.Sprintf("gitCommit=%s", commitSHA),
	}

	helmCmd := exec.CommandContext(ctx, "helm", helmArgs...)
	if out, err := helmCmd.CombinedOutput(); err != nil {
		log.Printf("helm install failed: %s", string(out))
		http.Error(w, fmt.Sprintf("deploying to vcluster: %s", string(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Branch %s deployed as %s into %s/%s", req.Branch, releaseName, tenant, name),
		"image":   imageRef,
		"release": releaseName,
	})
}
