package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/dusttostars/vcluster-ephemeral-envs/internal/render"
)

var eeGVR = schema.GroupVersionResource{
	Group:    "ephemeral.io",
	Version:  "v1alpha1",
	Resource: "ephemeralenvironments",
}

func envCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Render and apply ephemeral environment resources",
	}
	cmd.AddCommand(envRenderCmd())
	cmd.AddCommand(envApplyCmd())
	cmd.AddCommand(envDeleteCmd())
	return cmd
}

func envDeleteCmd() *cobra.Command {
	var (
		tenant     string
		suffix     string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete EphemeralEnvironment CRs in a tenant whose name ends with --suffix",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := buildRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			client, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return err
			}
			ns := fmt.Sprintf("tenant-%s", tenant)
			ctx := context.Background()
			list, err := client.Resource(eeGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			deleted := 0
			for _, env := range list.Items {
				name := env.GetName()
				if name == suffix || strings.HasSuffix(name, "-"+suffix) {
					if err := client.Resource(eeGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
						fmt.Fprintf(os.Stderr, "failed to delete %s: %v\n", name, err)
						continue
					}
					fmt.Fprintf(os.Stderr, "deleted %s/%s\n", ns, name)
					deleted++
				}
			}
			fmt.Fprintf(os.Stderr, "deleted %d env(s) matching suffix %q in %s\n", deleted, suffix, ns)
			return nil
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "default", "Tenant")
	cmd.Flags().StringVar(&suffix, "suffix", "", "Env name (or suffix after <app>-) to match (required)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.MarkFlagRequired("suffix")
	return cmd
}

func envRenderCmd() *cobra.Command {
	var (
		tenant   string
		branch   string
		ttl      string
		image    string
		replicas int32
		port     int32
		envPairs []string
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

			out, err := render.Template("cr.yaml.tmpl", p)
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
	cmd.MarkFlagRequired("tenant")
	cmd.MarkFlagRequired("branch")
	cmd.MarkFlagRequired("image")

	return cmd
}

func envApplyCmd() *cobra.Command {
	var (
		tenant     string
		branch     string
		ttl        string
		image      string
		replicas   int32
		port       int32
		envPairs   []string
		kubeconfig string
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Render and apply an EphemeralEnvironment CR (uses in-cluster config by default)",
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

			raw, err := render.Template("cr.yaml.tmpl", p)
			if err != nil {
				return err
			}
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal(raw, obj); err != nil {
				return fmt.Errorf("parsing rendered CR: %w", err)
			}

			cfg, err := buildRESTConfig(kubeconfig)
			if err != nil {
				return fmt.Errorf("kube config: %w", err)
			}
			client, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return err
			}

			ns := fmt.Sprintf("tenant-%s", tenant)
			ctx := context.Background()
			existing, getErr := client.Resource(eeGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			if getErr == nil {
				obj.SetResourceVersion(existing.GetResourceVersion())
				_, err = client.Resource(eeGVR).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
				if err != nil {
					return fmt.Errorf("updating %s/%s: %w", ns, name, err)
				}
				fmt.Fprintf(os.Stderr, "updated %s/%s\n", ns, name)
				return nil
			}
			_, err = client.Resource(eeGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("creating %s/%s: %w", ns, name, err)
			}
			fmt.Fprintf(os.Stderr, "created %s/%s\n", ns, name)
			return nil
		},
	}

	cmd.Flags().StringVar(&tenant, "tenant", "", "Tenant name (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch / env name (required)")
	cmd.Flags().StringVar(&ttl, "ttl", "2h", "Lifetime of the environment")
	cmd.Flags().StringVar(&image, "image", "", "App image (required)")
	cmd.Flags().Int32Var(&replicas, "replicas", 1, "App replicas")
	cmd.Flags().Int32Var(&port, "port", 80, "App container port")
	cmd.Flags().StringSliceVar(&envPairs, "env", nil, "Env vars KEY=VALUE (repeatable)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (uses in-cluster config if empty)")
	cmd.MarkFlagRequired("tenant")
	cmd.MarkFlagRequired("branch")
	cmd.MarkFlagRequired("image")
	return cmd
}

func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	home, _ := os.UserHomeDir()
	return clientcmd.BuildConfigFromFlags("", home+"/.kube/config")
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
