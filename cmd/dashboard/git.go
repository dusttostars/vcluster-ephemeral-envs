package main

import (
	"context"
	"fmt"
	"os/exec"
)

func gitCommitPush(ctx context.Context, repoPath, filePath, message string) error {
	cmds := [][]string{
		{"git", "-C", repoPath, "add", filePath},
		{"git", "-C", repoPath, "commit", "-m", message},
		{"git", "-C", repoPath, "push"},
	}

	for _, args := range cmds {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("running %v: %s: %w", args, string(out), err)
		}
	}

	return nil
}
