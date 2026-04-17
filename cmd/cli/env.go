package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/dusttostars/vcluster-ephemeral-envs/internal/render"
)

func envCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Render ephemeral environment resources",
	}
	cmd.AddCommand(envRenderCmd())
	return cmd
}

func envRenderCmd() *cobra.Command {
	var (
		tenant     string
		branch     string
		ttl        string
		image      string
		replicas   int32
		port       int32
		envPairs   []string
		templateFP string
	)

	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render an EphemeralEnvironment CR to stdout",
		Long:  "Pipe the output into `kubectl apply -f -` from a PR workflow.",
		RunE: func(cmd *cobra.Command, args []string) error {
			envMap, err := parseKVPairs(envPairs)
			if err != nil {
				return err
			}

			name := strings.ReplaceAll(branch, "/", "-")
			p := render.Params{
				Name:     name,
				Tenant:   tenant,
				Branch:   branch,
				TTL:      ttl,
				Image:    image,
				Replicas: replicas,
				Port:     port,
				Env:      envMap,
			}

			out, err := render.File(templateFP, p)
			if err != nil {
				return err
			}
			fmt.Fprint(os.Stdout, string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&tenant, "tenant", "", "Tenant name (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch name (required)")
	cmd.Flags().StringVar(&ttl, "ttl", "2h", "Lifetime of the environment")
	cmd.Flags().StringVar(&image, "image", "", "App image (required)")
	cmd.Flags().Int32Var(&replicas, "replicas", 1, "App replicas")
	cmd.Flags().Int32Var(&port, "port", 80, "App container port")
	cmd.Flags().StringSliceVar(&envPairs, "env", nil, "Env vars for the app in KEY=VALUE form (repeatable)")
	cmd.Flags().StringVar(&templateFP, "template", "charts/ephemeral-env/templates/cr.yaml.tmpl", "Template path")
	cmd.MarkFlagRequired("tenant")
	cmd.MarkFlagRequired("branch")
	cmd.MarkFlagRequired("image")

	return cmd
}

func parseKVPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid env pair %q (expected KEY=VALUE)", p)
		}
		m[k] = v
	}
	return m, nil
}
