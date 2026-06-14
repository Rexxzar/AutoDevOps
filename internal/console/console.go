// Package console implements the Resource Console: browser-based interactive
// shell + one-shot command execution for provisioned compute, mirroring AWS
// EC2 Instance Connect / Azure Bastion "Connect" without AutoDevOps ever
// storing a long-lived private key (see the design notes: Dev Notes: "Resource
// Console design").
package console

import (
	"context"
	"io"

	"github.com/Rexxzar/autodevops/internal/sshutil"
)

// Session is a live interactive shell to a remote resource. EC2/Azure VM
// connectors back it with an SSH session; ECS/ACI connectors (T26) will back
// it with their respective exec channels. The Manager and API layer only see
// this shape.
type Session interface {
	io.Reader
	io.Writer
	Resize(rows, cols uint16) error
	Close() error
}

// ExecResult is the result of a one-shot command, returned by
// POST /resources/{id}/console/exec.
type ExecResult = sshutil.ExecResult

// Resource is the subset of a provisioned_resources row a Connector needs to
// open a session or run a command — built by the API layer from
// resourceRow.Outputs (see internal/api/console.go).
type Resource struct {
	Name           string
	Provider       string
	ResourceType   string
	CloudAccountID string
	Region         string
	Outputs        map[string]any
}

// Connector knows how to open an interactive Session or run a one-shot
// command against one (provider, resource_type) pair — registered in
// Registry.Connector.
type Connector interface {
	Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error)
	Exec(ctx context.Context, res Resource, command string) (ExecResult, error)
}

// connectorLookup resolves a Connector for (provider, resourceType) —
// implemented by *Registry, and by fakes in manager_test.go so Manager can be
// exercised without real cloud credentials.
type connectorLookup interface {
	Connector(provider, resourceType string) (Connector, bool)
}
