package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/dusttostars/vcluster-ephemeral-envs/internal/tenant"
	"github.com/dusttostars/vcluster-ephemeral-envs/internal/vcluster"
)

func main() {
	root := &cobra.Command{
		Use:   "ephemeral",
		Short: "Manage ephemeral vcluster environments",
	}

	root.AddCommand(createCmd())
	root.AddCommand(deleteCmd())
	root.AddCommand(tenantCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func createCmd() *cobra.Command {
	var (
		tenantName string
		branch     string
		ttl        string
		repoDir    string
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an ephemeral vcluster environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			duration, err := time.ParseDuration(ttl)
			if err != nil {
				return fmt.Errorf("invalid TTL %q: %w", ttl, err)
			}

			env := vcluster.NewEnvironment(name, tenantName, branch, duration)

			path, err := env.WriteManifest(repoDir)
			if err != nil {
				return fmt.Errorf("writing manifest: %w", err)
			}

			fmt.Printf("Environment manifest created: %s\n", path)
			fmt.Printf("  Name:   %s\n", env.Name)
			fmt.Printf("  Tenant: %s\n", env.Tenant)
			fmt.Printf("  Branch: %s\n", env.Branch)
			fmt.Printf("  TTL:    %s\n", env.TTL)
			fmt.Println("\nCommit and push to trigger ArgoCD sync.")

			return nil
		},
	}

	cmd.Flags().StringVar(&tenantName, "tenant", "", "Tenant name (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch name (required)")
	cmd.Flags().StringVar(&ttl, "ttl", "2h", "Time-to-live for the environment")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "Path to the GitOps repo root")
	cmd.MarkFlagRequired("tenant")
	cmd.MarkFlagRequired("branch")

	return cmd
}

func deleteCmd() *cobra.Command {
	var (
		tenantName string
		repoDir    string
	)

	cmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete an ephemeral vcluster environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			env := &vcluster.Environment{
				Name:   name,
				Tenant: tenantName,
			}

			if err := env.RemoveManifest(repoDir); err != nil {
				return fmt.Errorf("removing manifest: %w", err)
			}

			fmt.Printf("Environment manifest removed for %s (tenant=%s)\n", name, tenantName)
			fmt.Println("Commit and push to trigger ArgoCD prune.")

			return nil
		},
	}

	cmd.Flags().StringVar(&tenantName, "tenant", "", "Tenant name (required)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "Path to the GitOps repo root")
	cmd.MarkFlagRequired("tenant")

	return cmd
}

func tenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Manage tenants",
	}

	cmd.AddCommand(tenantCreateCmd())
	return cmd
}

func tenantCreateCmd() *cobra.Command {
	var (
		maxCPU    string
		maxMemory string
		maxEnvs   int
		maxAge    int
		repoDir   string
		repoURL   string
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new tenant with namespace, quotas, RBAC, and ArgoCD project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			t := &tenant.Tenant{
				Name:       name,
				MaxCPU:     maxCPU,
				MaxMemory:  maxMemory,
				MaxEnvs:    maxEnvs,
				MaxAgeSecs: maxAge,
			}

			if err := t.WriteManifests(repoDir); err != nil {
				return fmt.Errorf("writing tenant manifests: %w", err)
			}

			if err := t.WriteArgoProject(repoDir, repoURL); err != nil {
				return fmt.Errorf("writing argo project: %w", err)
			}

			log.Printf("Tenant %q created successfully", name)
			fmt.Printf("  Namespace:  tenant-%s\n", name)
			fmt.Printf("  CPU Limit:  %s\n", maxCPU)
			fmt.Printf("  Mem Limit:  %s\n", maxMemory)
			fmt.Printf("  Max Envs:   %d\n", maxEnvs)
			fmt.Printf("  Max Age:    %ds\n", maxAge)
			fmt.Println("\nCommit and push to trigger ArgoCD sync.")

			return nil
		},
	}

	cmd.Flags().StringVar(&maxCPU, "max-cpu", "4", "Max CPU for the tenant")
	cmd.Flags().StringVar(&maxMemory, "max-memory", "8Gi", "Max memory for the tenant")
	cmd.Flags().IntVar(&maxEnvs, "max-envs", 5, "Max concurrent environments")
	cmd.Flags().IntVar(&maxAge, "max-age", 86400, "Max age in seconds for any environment")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "Path to the GitOps repo root")
	cmd.Flags().StringVar(&repoURL, "repo-url", "", "Git repo URL for ArgoCD project (required)")
	cmd.MarkFlagRequired("repo-url")

	return cmd
}
