// Package render provides a thin wrapper around text/template used by both
// the CLI (to produce the EphemeralEnvironment CR at PR time) and the
// controller (to produce the vcluster Application and in-vcluster app
// manifests at reconcile time). Keeping a single entry point means both
// sides stay in lockstep on syntax and helpers.
package render

import (
	"bytes"
	"fmt"
	"os"
	"text/template"
)

// Params is what the templates see under `.` — a flat struct by convention
// so templates can reference `.Name`, `.Tenant`, `.Image`, etc.
type Params struct {
	Name     string
	Tenant   string
	Branch   string
	TTL      string
	OwnerUID string
	Image    string
	Replicas int32
	Port     int32
	Env      map[string]string
}

// File renders a template at the given path.
func File(path string, p Params) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %w", path, err)
	}
	return String(string(raw), p)
}

// String renders an in-memory template. Named blocks so the caller
// gets a useful error location if parsing fails.
func String(tmpl string, p Params) ([]byte, error) {
	t, err := template.New("ephemeral-env").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}
	return buf.Bytes(), nil
}
