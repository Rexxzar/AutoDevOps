package terraform

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run is a persisted record of one terraform_runs row — a single
// plan/apply/destroy invocation against a deployment's workspace, mirroring
// the table created in the T01 schema.
type Run struct {
	ID             string `json:"id"`
	DeploymentID   string `json:"deployment_id"`
	CloudAccountID string `json:"cloud_account_id,omitempty"`
	Template       string `json:"template"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	PlanOutput     string `json:"plan_output,omitempty"`
	StateFilePath  string `json:"state_file_path,omitempty"`
}

// Action and status values — match the terraform_runs CHECK constraints.
const (
	actionPlan    = "plan"
	actionApply   = "apply"
	actionDestroy = "destroy"

	statusRunning   = "running"
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
)

// Runner executes terraform plan/apply/destroy pipelines against per-
// deployment workspaces, persisting each invocation to terraform_runs and
// publishing live progress over SSE — the same DB-write-plus-hub.Publish
// shape the k8s self-healing controller uses for pod health (see
// internal/k8s/k8s.go upsertPodHealth and internal/k8s/healer.go).
type Runner struct {
	execPath string
	baseDir  string
	pool     *pgxpool.Pool
	hub      *sse.Hub

	mu       sync.Mutex
	inFlight map[string]struct{}
}

// NewRunner builds a Runner. execPath is a resolved terraform binary (see
// EnsureBinary); baseDir is the root directory under which each deployment
// gets its own persistent workspace (one combined stack + state file per
// deployment — see terraform-engine-design memory).
func NewRunner(execPath, baseDir string, pool *pgxpool.Pool, hub *sse.Hub) *Runner {
	return &Runner{
		execPath: execPath,
		baseDir:  baseDir,
		pool:     pool,
		hub:      hub,
		inFlight: make(map[string]struct{}),
	}
}

// Plan renders config into the deployment's workspace (overwriting any
// previous version — the engine always regenerates the full desired state)
// and runs `terraform init` + `terraform plan -out=tfplan`, recording the run
// with a human-readable summary of the plan (`terraform show <planfile>`).
func (r *Runner) Plan(ctx context.Context, deploymentID, cloudAccountID, template string, config []byte) (*Run, error) {
	return r.run(ctx, deploymentID, cloudAccountID, template, actionPlan, false, func(ctx context.Context) (string, error) {
		dir, err := writeConfig(r.baseDir, deploymentID, config)
		if err != nil {
			return "", err
		}

		tf, log, err := r.client(dir)
		if err != nil {
			return "", err
		}
		if err := tf.Init(ctx); err != nil {
			return log.String(), fmt.Errorf("init: %w", err)
		}

		plan := planFilePath(dir)
		if _, err := tf.Plan(ctx, tfexec.Out(plan)); err != nil {
			return log.String(), fmt.Errorf("plan: %w", err)
		}

		summary, err := tf.ShowPlanFileRaw(ctx, plan)
		if err != nil {
			return log.String(), fmt.Errorf("show plan: %w", err)
		}
		return summary, nil
	})
}

// Apply runs `terraform apply` against the most recently saved plan for the
// deployment, recording the resulting state file path. Returns a friendly
// error if Plan hasn't been run for this deployment yet.
func (r *Runner) Apply(ctx context.Context, deploymentID, cloudAccountID, template string) (*Run, error) {
	return r.run(ctx, deploymentID, cloudAccountID, template, actionApply, true, func(ctx context.Context) (string, error) {
		dir := workspaceDir(r.baseDir, deploymentID)
		ok, err := hasPlan(dir)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no saved plan for deployment %s — run Plan first", deploymentID)
		}

		tf, log, err := r.client(dir)
		if err != nil {
			return "", err
		}
		if err := tf.Init(ctx); err != nil {
			return log.String(), fmt.Errorf("init: %w", err)
		}
		if err := tf.Apply(ctx, tfexec.DirOrPlan(planFilePath(dir))); err != nil {
			return log.String(), fmt.Errorf("apply: %w", err)
		}
		return log.String(), nil
	})
}

// Destroy tears down every resource in the deployment's existing workspace.
// Returns a friendly error if the deployment has no workspace yet (nothing
// has ever been planned/applied).
func (r *Runner) Destroy(ctx context.Context, deploymentID, cloudAccountID, template string) (*Run, error) {
	return r.run(ctx, deploymentID, cloudAccountID, template, actionDestroy, true, func(ctx context.Context) (string, error) {
		dir := workspaceDir(r.baseDir, deploymentID)
		ok, err := hasWorkspace(dir)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no workspace for deployment %s — nothing to destroy", deploymentID)
		}

		tf, log, err := r.client(dir)
		if err != nil {
			return "", err
		}
		if err := tf.Init(ctx); err != nil {
			return log.String(), fmt.Errorf("init: %w", err)
		}
		if err := tf.Destroy(ctx); err != nil {
			return log.String(), fmt.Errorf("destroy: %w", err)
		}
		return log.String(), nil
	})
}

// client builds a tfexec client rooted at dir with combined stdout/stderr
// captured into a buffer — used both as the recorded run output on success
// and as failure context when a step errors partway through.
func (r *Runner) client(dir string) (*tfexec.Terraform, *bytes.Buffer, error) {
	tf, err := tfexec.NewTerraform(dir, r.execPath)
	if err != nil {
		return nil, nil, fmt.Errorf("new client for %s: %w", dir, err)
	}
	var log bytes.Buffer
	tf.SetStdout(&log)
	tf.SetStderr(&log)
	return tf, &log, nil
}

// run drives one plan/apply/destroy invocation through the full
// pending -> running -> succeeded/failed lifecycle: it records and publishes
// the run before and after executing step, and rejects a second concurrent
// invocation for the same deployment with a clear error rather than letting
// terraform's own state-lock error surface (matches the "deterministic
// control plane, no surprise queueing" framing).
func (r *Runner) run(ctx context.Context, deploymentID, cloudAccountID, template, action string, recordState bool, step func(context.Context) (string, error)) (*Run, error) {
	if !r.acquire(deploymentID) {
		return nil, fmt.Errorf("terraform: %s deployment %s: a run is already in progress for this deployment", action, deploymentID)
	}
	defer r.release(deploymentID)

	run := &Run{
		DeploymentID:   deploymentID,
		CloudAccountID: cloudAccountID,
		Template:       template,
		Action:         action,
		Status:         statusRunning,
	}
	if err := r.insertRun(ctx, run); err != nil {
		return nil, err
	}
	r.publish(run)

	output, stepErr := step(ctx)
	run.PlanOutput = output

	if stepErr == nil && recordState {
		path, err := stateFilePath(workspaceDir(r.baseDir, deploymentID))
		if err != nil {
			stepErr = err
		} else {
			run.StateFilePath = path
		}
	}

	if stepErr != nil {
		run.Status = statusFailed
	} else {
		run.Status = statusSucceeded
	}
	if err := r.updateRun(ctx, run); err != nil {
		return run, err
	}
	r.publish(run)

	if stepErr != nil {
		return run, fmt.Errorf("terraform: %s deployment %s: %w", action, deploymentID, stepErr)
	}
	return run, nil
}

func (r *Runner) acquire(deploymentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.inFlight[deploymentID]; busy {
		return false
	}
	r.inFlight[deploymentID] = struct{}{}
	return true
}

func (r *Runner) release(deploymentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, deploymentID)
}

func (r *Runner) insertRun(ctx context.Context, run *Run) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO terraform_runs (deployment_id, cloud_account_id, template, action, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, run.DeploymentID, nullable(run.CloudAccountID), run.Template, run.Action, run.Status).Scan(&run.ID)
	if err != nil {
		return fmt.Errorf("terraform: insert run: %w", err)
	}
	return nil
}

func (r *Runner) updateRun(ctx context.Context, run *Run) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE terraform_runs
		SET status = $2, plan_output = $3, state_file_path = $4, updated_at = now()
		WHERE id = $1
	`, run.ID, run.Status, nullable(run.PlanOutput), nullable(run.StateFilePath))
	if err != nil {
		return fmt.Errorf("terraform: update run %s: %w", run.ID, err)
	}
	return nil
}

func (r *Runner) publish(run *Run) {
	r.hub.Publish(sse.Event{Type: "terraform.run", Payload: run})
}

// nullable converts an empty string to NULL for optional DB columns
// (cloud_account_id is a UUID — "" would fail type conversion, not just the
// NOT NULL check that plain empty strings would trip elsewhere).
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
