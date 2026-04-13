package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var jobGVR = schema.GroupVersionResource{
	Group:    "batch",
	Version:  "v1",
	Resource: "jobs",
}

type deployRequest struct {
	Branch    string `json:"branch"`
	Namespace string `json:"namespace"`
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

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
	repoURL := fmt.Sprintf("https://github.com/%s.git", h.githubRepo)

	// 1. Build image with Kaniko Job.
	jobName := fmt.Sprintf("build-%s-%d", slug, time.Now().Unix())
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	log.Printf("deploy: creating kaniko build job %s for branch %s", jobName, req.Branch)

	if err := h.runKanikoBuild(ctx, jobName, repoURL, req.Branch, imageRef); err != nil {
		http.Error(w, fmt.Sprintf("building image: %v", err), http.StatusInternalServerError)
		return
	}

	// 2. Install the app chart into the vcluster.
	kubeconfig, cleanup, err := h.getVClusterKubeconfig(ctx, tenant, name)
	if err != nil {
		http.Error(w, fmt.Sprintf("getting kubeconfig: %v", err), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	chartPath := filepath.Join(h.repoPath, "charts", "app")
	releaseName := fmt.Sprintf("app-%s", slug)

	helmArgs := []string{
		"upgrade", "--install", releaseName, chartPath,
		"--kubeconfig", kubeconfig,
		"--namespace", req.Namespace,
		"--create-namespace",
		"--set", fmt.Sprintf("image.repository=%s", imageRepo),
		"--set", fmt.Sprintf("image.tag=%s", imageTag),
		"--set", "image.pullPolicy=Always",
		"--set", fmt.Sprintf("gitBranch=%s", req.Branch),
		"--set", fmt.Sprintf("gitCommit=%s", slug),
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

// runKanikoBuild creates a Kaniko Job that clones the repo, builds the image,
// and pushes to the local registry. Blocks until the job completes.
func (h *handler) runKanikoBuild(ctx context.Context, jobName, repoURL, branch, imageRef string) error {
	buildNS := "ephemeral-system"
	backoffLimit := int64(0)
	ttl := int64(300)

	job := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]interface{}{
				"name":      jobName,
				"namespace": buildNS,
			},
			"spec": map[string]interface{}{
				"backoffLimit":            backoffLimit,
				"ttlSecondsAfterFinished": ttl,
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"restartPolicy": "Never",
						"initContainers": []interface{}{
							map[string]interface{}{
								"name":  "clone",
								"image": "alpine/git:latest",
								"command": []interface{}{
									"git", "clone", "--depth", "1",
									"--branch", branch,
									repoURL, "/workspace",
								},
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "workspace",
										"mountPath": "/workspace",
									},
								},
							},
						},
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "kaniko",
								"image": "gcr.io/kaniko-project/executor:latest",
								"args": []interface{}{
									"--dockerfile=/workspace/app/Dockerfile",
									"--context=/workspace/app",
									fmt.Sprintf("--destination=%s", imageRef),
									"--insecure",
									"--cache=false",
								},
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "workspace",
										"mountPath": "/workspace",
									},
								},
								"resources": map[string]interface{}{
									"limits": map[string]interface{}{
										"cpu":    "1",
										"memory": "1Gi",
									},
									"requests": map[string]interface{}{
										"cpu":    "200m",
										"memory": "256Mi",
									},
								},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name":     "workspace",
								"emptyDir": map[string]interface{}{},
							},
						},
					},
				},
			},
		},
	}

	// Create the job.
	_, err := h.client.Resource(jobGVR).Namespace(buildNS).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating kaniko job: %w", err)
	}

	log.Printf("deploy: kaniko job %s created, waiting for completion...", jobName)

	// Poll until the job completes (up to 5 minutes).
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		j, err := h.client.Resource(jobGVR).Namespace(buildNS).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("checking job status: %w", err)
		}

		status, _, _ := nestedMap(j.Object, "status")

		// Check for completion.
		succeeded, _ := status["succeeded"].(int64)
		if succeeded > 0 {
			log.Printf("deploy: kaniko job %s succeeded", jobName)
			return nil
		}

		// Check for failure.
		failed, _ := status["failed"].(int64)
		if failed > 0 {
			return fmt.Errorf("kaniko build job failed")
		}

		// Also check conditions for completion.
		if conditions, ok := status["conditions"].([]interface{}); ok {
			for _, c := range conditions {
				if cm, ok := c.(map[string]interface{}); ok {
					ctype, _ := cm["type"].(string)
					cstatus, _ := cm["status"].(string)
					if ctype == "Complete" && cstatus == "True" {
						log.Printf("deploy: kaniko job %s completed", jobName)
						return nil
					}
					if ctype == "Failed" && cstatus == "True" {
						reason, _ := cm["reason"].(string)
						return fmt.Errorf("kaniko build failed: %s", reason)
					}
				}
			}
		}

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("kaniko build timed out after 5 minutes")
}

func nestedMap(obj map[string]interface{}, fields ...string) (map[string]interface{}, bool, error) {
	current := obj
	for _, f := range fields {
		next, ok := current[f]
		if !ok {
			return nil, false, nil
		}
		current, ok = next.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
	}
	return current, true, nil
}
