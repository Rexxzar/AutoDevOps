package console

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ecsShell is the command run for an interactive Open() session — the
// stack.tf.tmpl ECS container defaults to nginx:latest (Debian-based),
// which ships /bin/sh.
const ecsShell = "/bin/sh"

// ecsExecTimeout bounds how long Exec waits for the wrapped command to finish
// and the SSM data channel to report channel_closed.
const ecsExecTimeout = 90 * time.Second

// ecsConnector opens shells / runs commands on ECS Fargate tasks via
// ecs:ExecuteCommand, which hands back an SSM Session Manager data-channel
// URL + token (see ssm.go for the wire protocol).
type ecsConnector struct {
	registry *Registry
}

// target resolves the running task for res's ECS service at connect time
// (not from the T11 poller, which may be stale) — cluster name comes from
// stack.tf.tmpl's ecs_cluster_name output, service/container name is
// provisioned_resources.name (aws_ecs_service.main and the task definition's
// container are both named "{{ .Name }}").
func (c *ecsConnector) target(ctx context.Context, res Resource) (cluster, taskArn, containerName string, err error) {
	cluster, _ = res.Outputs["ecs_cluster_name"].(string)
	if cluster == "" || res.Name == "" {
		return "", "", "", fmt.Errorf("console: resource is missing ecs_cluster_name output — apply it first")
	}

	cfg, err := c.registry.loadAWSConfig(ctx, res.CloudAccountID, res.Region)
	if err != nil {
		return "", "", "", err
	}
	client := ecs.NewFromConfig(cfg)

	out, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		ServiceName:   aws.String(res.Name),
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("console: ecs list tasks: %w", err)
	}
	if len(out.TaskArns) == 0 {
		return "", "", "", fmt.Errorf("console: no running tasks for ecs service %s — is it still deploying?", res.Name)
	}
	return cluster, out.TaskArns[0], res.Name, nil
}

// dialExec resolves the live task, calls ExecuteCommand for command, and
// opens the returned SSM data channel.
func (c *ecsConnector) dialExec(ctx context.Context, res Resource, command string) (*ssmSession, error) {
	cluster, taskArn, containerName, err := c.target(ctx, res)
	if err != nil {
		return nil, err
	}
	cfg, err := c.registry.loadAWSConfig(ctx, res.CloudAccountID, res.Region)
	if err != nil {
		return nil, err
	}
	client := ecs.NewFromConfig(cfg)

	out, err := client.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String(cluster),
		Task:        aws.String(taskArn),
		Container:   aws.String(containerName),
		Command:     aws.String(command),
		Interactive: true, // required by the ECS API regardless of session kind
	})
	if err != nil {
		return nil, fmt.Errorf("console: ecs execute command: %w", err)
	}
	if out.Session == nil || out.Session.StreamUrl == nil || out.Session.TokenValue == nil {
		return nil, fmt.Errorf("console: ecs execute command returned no session details")
	}
	return dialSSMSession(*out.Session.StreamUrl, *out.Session.TokenValue)
}

func (c *ecsConnector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	return c.dialExec(ctx, res, ecsShell)
}

func (c *ecsConnector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	wrapped := fmt.Sprintf("/bin/sh -c %s", shellQuote(command+"; echo "+aciExitMarkerPrefix+"$?"+aciExitMarkerSuffix))
	sess, err := c.dialExec(ctx, res, wrapped)
	if err != nil {
		return ExecResult{}, err
	}
	defer sess.Close()
	sess.conn.SetDeadline(time.Now().Add(ecsExecTimeout))

	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := sess.Read(buf)
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
