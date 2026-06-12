package console

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"golang.org/x/net/websocket"
)

// aciShell is the command run for an interactive Open() session — the base
// images used by AzureACIParams (e.g. nginx:latest) all ship /bin/sh.
const aciShell = "/bin/sh"

// aciExecOrigin is a placeholder Origin for the outbound websocket.Dial.
// ACI's exec websocket endpoint doesn't enforce browser-style Origin checks,
// but x/net/websocket requires a syntactically valid URL.
const aciExecOrigin = "http://localhost/"

// aciExecTimeout bounds how long Exec waits for the wrapped command to finish
// and close the websocket — ACI's exec stream signals completion by closing
// the connection when the spawned process exits, with no separate
// "exit status" message.
const aciExecTimeout = 90 * time.Second

// aciExitMarkerPrefix/Suffix wrap Exec's command so its exit code can be
// recovered from the raw output stream — ACI exec is a single combined
// stdout+stderr byte stream with no structured result, unlike SSH's
// session.Run.
const (
	aciExitMarkerPrefix = "\x01EXIT:"
	aciExitMarkerSuffix = "\x01"
)

// aciConnector opens shells / runs commands on Azure Container Instances via
// ContainersClient.ExecuteCommand, which returns a one-shot websocket URI +
// password (no SSH agent involved — unlike the EC2/Azure VM connectors).
type aciConnector struct {
	registry *Registry
}

// target extracts the resource group and container group/name needed for
// ExecuteCommand from the resource's terraform outputs and name
// (resource_group_name output, "<name>-aci" container group, "<name>"
// container — stack.tf.tmpl's azurerm_container_group.main).
func (c *aciConnector) target(res Resource) (rgName, containerGroup, containerName string, err error) {
	rgName, _ = res.Outputs["resource_group_name"].(string)
	if rgName == "" || res.Name == "" {
		return "", "", "", fmt.Errorf("console: resource is missing resource_group_name output — apply it first")
	}
	return rgName, res.Name + "-aci", res.Name, nil
}

// dialExec calls ExecuteCommand for command and dials the returned websocket,
// sending the response's password as the first frame — that handshake is
// ACI exec's only authentication on the (otherwise unauthenticated)
// websocket URI.
func (c *aciConnector) dialExec(ctx context.Context, res Resource, command string, rows, cols uint16) (*websocket.Conn, error) {
	rgName, containerGroup, containerName, err := c.target(res)
	if err != nil {
		return nil, err
	}
	cred, subscriptionID, err := c.registry.loadAzureCredential(ctx, res.CloudAccountID)
	if err != nil {
		return nil, err
	}
	client, err := armcontainerinstance.NewContainersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("console: build azure container instance client: %w", err)
	}

	resp, err := client.ExecuteCommand(ctx, rgName, containerGroup, containerName, armcontainerinstance.ContainerExecRequest{
		Command: to.Ptr(command),
		TerminalSize: &armcontainerinstance.ContainerExecRequestTerminalSize{
			Rows: to.Ptr(int32(rows)),
			Cols: to.Ptr(int32(cols)),
		},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("console: aci execute command: %w", err)
	}
	if resp.WebSocketURI == nil || resp.Password == nil {
		return nil, fmt.Errorf("console: aci execute command returned no websocket uri/password")
	}

	conn, err := websocket.Dial(*resp.WebSocketURI, "", aciExecOrigin)
	if err != nil {
		return nil, fmt.Errorf("console: aci websocket dial: %w", err)
	}
	conn.PayloadType = websocket.BinaryFrame
	if _, err := conn.Write([]byte(*resp.Password)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("console: aci websocket auth: %w", err)
	}
	return conn, nil
}

// aciSession wraps the ACI exec websocket as a Session. ACI's exec API has no
// post-connect resize — TerminalSize is fixed at ExecuteCommand time, so
// Resize is a no-op (the frontend's ResizeObserver calls it best-effort and
// ignores errors).
type aciSession struct {
	conn *websocket.Conn
}

func (s *aciSession) Read(p []byte) (int, error)     { return s.conn.Read(p) }
func (s *aciSession) Write(p []byte) (int, error)    { return s.conn.Write(p) }
func (s *aciSession) Resize(rows, cols uint16) error { return nil }
func (s *aciSession) Close() error                   { return s.conn.Close() }

func (c *aciConnector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	conn, err := c.dialExec(ctx, res, aciShell, rows, cols)
	if err != nil {
		return nil, err
	}
	return &aciSession{conn: conn}, nil
}

func (c *aciConnector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	wrapped := fmt.Sprintf("/bin/sh -c %s", shellQuote(command+"; echo "+aciExitMarkerPrefix+"$?"+aciExitMarkerSuffix))
	conn, err := c.dialExec(ctx, res, wrapped, 24, 80)
	if err != nil {
		return ExecResult{}, err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(aciExecTimeout))

	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	stdout, exitCode := parseExitMarker(out.String())
	return ExecResult{Stdout: stdout, ExitCode: exitCode}, nil
}

// parseExitMarker splits output (raw combined stdout+stderr from a "<cmd>;
// echo \x01EXIT:$?\x01"-wrapped command) into the command's own output and
// its exit code. If the marker is missing (command truncated by timeout,
// connection dropped, etc.) it returns the full output and exit code 0.
func parseExitMarker(output string) (stdout string, exitCode int) {
	idx := strings.LastIndex(output, aciExitMarkerPrefix)
	if idx < 0 {
		return output, 0
	}
	rest := output[idx+len(aciExitMarkerPrefix):]
	end := strings.Index(rest, aciExitMarkerSuffix)
	if end < 0 {
		return output, 0
	}
	code, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		code = 0
	}
	return output[:idx], code
}

// shellQuote single-quotes s for use as one /bin/sh -c argument, escaping any
// embedded single quotes — used by the ACI and ECS exec connectors, whose
// ExecuteCommand APIs take a single command string rather than an argv slice.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
