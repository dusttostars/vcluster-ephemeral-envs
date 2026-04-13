package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type githubBranch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type branchResponse struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

func (h *handler) listBranches(w http.ResponseWriter, r *http.Request) {
	if h.githubToken == "" || h.githubRepo == "" {
		http.Error(w, "GitHub integration not configured", http.StatusServiceUnavailable)
		return
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/branches?per_page=100", h.githubRepo)

	req, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("creating request: %v", err), http.StatusInternalServerError)
		return
	}

	req.Header.Set("Authorization", "Bearer "+h.githubToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("calling GitHub API: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("GitHub API returned %d: %s", resp.StatusCode, string(body)), http.StatusBadGateway)
		return
	}

	var branches []githubBranch
	if err := json.NewDecoder(resp.Body).Decode(&branches); err != nil {
		http.Error(w, fmt.Sprintf("decoding response: %v", err), http.StatusInternalServerError)
		return
	}

	result := make([]branchResponse, len(branches))
	for i, b := range branches {
		result[i] = branchResponse{
			Name: b.Name,
			SHA:  b.Commit.SHA,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
