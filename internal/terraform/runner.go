package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run is a persisted record of one terraform_runs row — a single
// plan/apply/destroy invocation against a deployment's workspace, mirroring
// the table created in the T01 schema.
type Run struct {
	ID             string `json:"id"`
	DeploymentID   string `json:"deployment_id,omitempty"`
	ResourceID     string `json:"resource_id,omitempty"`
	CloudAccountID string `json:"cloud_account_id,omitempty"`
	Template       string `json:"template"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	PlanOutput     string `json:"plan_output,omitempty"`
	StateFilePath  string `json:"state_file_path,omitempty"`

	// CreatedAt is set by insertRun from the row's DB-assigned created_at —
	// included in "terraform.run" SSE payloads so the frontend's
	// run-history sort (newest first) works for live-pushed runs the same
	// as for the GET /terraform/runs history (handleListTerraformRuns).
	CreatedAt time.Time `json:"created_at"`

	// Outputs holds the values from any `output` blocks in the rendered
	// config (vpc_id, public IPs, RDS endpoint, etc. — see
	// templates/*/stack.tf.tmpl), captured via `terraform output` after a
	// successful Apply. Populated by Apply only; nil for Plan/Destroy runs.
	Outputs map[string]json.RawMessage `json:"outputs,omitempty"`
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
	execPath       string
	baseDir        string
	pluginCacheDir string
	pool           *pgxpool.Pool
	hub            *sse.Hub
	vault          *secrets.Vault

	mu       sync.Mutex
	inFlight map[string]struct{}
}

// NewRunner builds a Runner. execPath is a resolved terraform binary (see
// EnsureBinary); baseDir is the root directory under which each deployment
// gets its own persistent workspace (one combined stack + state file per
// deployment — see terraform-engine-design memory). vault decrypts
// cloud_accounts credentials for cloudAccountID-scoped runs (see
// providerEnv in credentials.go).
//
// pluginCacheDir is a single shared TF_PLUGIN_CACHE_DIR (see client) so
// `terraform init` downloads each provider version once instead of once per
// workspace — without it, every workspace's .terraform/providers ends up
// with its own ~686MB copy of the AWS provider plugin.
func NewRunner(execPath, baseDir string, pool *pgxpool.Pool, hub *sse.Hub, vault *secrets.Vault) *Runner {
	return &Runner{
		execPath:       execPath,
		baseDir:        baseDir,
		pluginCacheDir: filepath.Join(baseDir, "plugin-cache"),
		pool:           pool,
		hub:            hub,
		vault:          vault,
		inFlight:       make(map[string]struct{}),
	}
}

// Plan renders config into the workspace identified by deploymentID (git
// deployments) or resourceID (standalone Resources — exactly one of the two
// is non-empty), overwriting any previous version — the engine always
// regenerates the full desired state — and runs `terraform init` +
// `terraform plan -out=tfplan`, recording the run with a human-readable
// summary of the plan (`terraform show <planfile>`).
func (r *Runner) Plan(ctx context.Context, deploymentID, resourceID, cloudAccountID, template string, config []byte) (*Run, error) {
	return r.run(ctx, deploymentID, resourceID, cloudAccountID, template, actionPlan, false, func(ctx context.Context) (string, error) {
		dir, err := writeConfig(r.baseDir, workspaceKey(deploymentID, resourceID), config)
		if err != nil {
			return "", err
		}

		tf, log, err := r.client(ctx, dir, cloudAccountID)
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
// workspace, recording the resulting state file path. Returns a friendly
// error if Plan hasn't been run for this workspace yet.
func (r *Runner) Apply(ctx context.Context, deploymentID, resourceID, cloudAccountID, template string) (*Run, error) {
	var outputs map[string]json.RawMessage

	run, err := r.run(ctx, deploymentID, resourceID, cloudAccountID, template, actionApply, true, func(ctx context.Context) (string, error) {
		key := workspaceKey(deploymentID, resourceID)
		dir, err := workspaceDir(r.baseDir, key)
		if err != nil {
			return "", err
		}
		ok, err := hasPlan(dir)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no saved plan for %s — run Plan first", key)
		}

		tf, log, err := r.client(ctx, dir, cloudAccountID)
		if err != nil {
			return "", err
		}
		if err := tf.Init(ctx); err != nil {
			return log.String(), fmt.Errorf("init: %w", err)
		}
		if err := tf.Apply(ctx, tfexec.DirOrPlan(planFilePath(dir))); err != nil {
			return log.String(), fmt.Errorf("apply: %w", err)
		}

		raw, err := tf.Output(ctx)
		if err != nil {
			return log.String(), fmt.Errorf("output: %w", err)
		}
		outputs = make(map[string]json.RawMessage, len(raw))
		for k, v := range raw {
			outputs[k] = v.Value
		}
		return log.String(), nil
	})
	if run != nil {
		run.Outputs = outputs
	}
	return run, err
}

// Destroy tears down every resource in the workspace's existing state.
// Returns a friendly error if the workspace doesn't exist yet (nothing has
// ever been planned/applied).
func (r *Runner) Destroy(ctx context.Context, deploymentID, resourceID, cloudAccountID, template string) (*Run, error) {
	return r.run(ctx, deploymentID, resourceID, cloudAccountID, template, actionDestroy, true, func(ctx context.Context) (string, error) {
		key := workspaceKey(deploymentID, resourceID)
		dir, err := workspaceDir(r.baseDir, key)
		if err != nil {
			return "", err
		}
		ok, err := hasWorkspace(dir)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no workspace for %s — nothing to destroy", key)
		}

		// A workspace can exist with only a rendered (and possibly invalid)
		// main.tf if Plan failed before ever reaching Apply — e.g. a
		// provider-validation error on the generated config. In that case
		// nothing was ever created, so re-running terraform here would just
		// re-validate the same broken config and fail identically. Treat
		// "no state file" as "nothing to destroy" and succeed immediately.
		applied, err := hasState(dir)
		if err != nil {
			return "", err
		}
		if !applied {
			return "no terraform state — nothing was ever provisioned, nothing to destroy", nil
		}

		tf, log, err := r.client(ctx, dir, cloudAccountID)
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

// ReadConfig returns the workspace's current main.tf — the exact HCL last
// planned, whether generated by Engine.Render or hand-edited via the
// resource detail page's "Plan changes" action. Returns a wrapped
// os.ErrNotExist if Plan has never run for this workspace (exactly one of
// deploymentID/resourceID is non-empty, as elsewhere in this package).
func (r *Runner) ReadConfig(deploymentID, resourceID string) ([]byte, error) {
	return readConfig(r.baseDir, workspaceKey(deploymentID, resourceID))
}

// client builds a tfexec client rooted at dir with combined stdout/stderr
// captured into a buffer — used both as the recorded run output on success
// and as failure context when a step errors partway through. When
// cloudAccountID is set, the subprocess env is augmented with that account's
// decrypted provider credentials (see providerEnv) so the AWS/Azure provider
// can authenticate without credentials ever touching the generated HCL.
func (r *Runner) client(ctx context.Context, dir, cloudAccountID string) (*tfexec.Terraform, *bytes.Buffer, error) {
	tf, err := tfexec.NewTerraform(dir, r.execPath)
	if err != nil {
		return nil, nil, fmt.Errorf("new client for %s: %w", dir, err)
	}

	if err := os.MkdirAll(r.pluginCacheDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create plugin cache dir: %w", err)
	}

	env, err := r.providerEnv(ctx, cloudAccountID)
	if err != nil {
		return nil, nil, err
	}
	if env == nil {
		env = processEnv()
	}
	env["TF_PLUGIN_CACHE_DIR"] = r.pluginCacheDir
	if err := tf.SetEnv(env); err != nil {
		return nil, nil, fmt.Errorf("set env for %s: %w", dir, err)
	}

	var log bytes.Buffer
	tf.SetStdout(&log)
	tf.SetStderr(&log)
	return tf, &log, nil
}

// workspaceKey returns the identifier used for the on-disk workspace
// directory and the in-flight lock — exactly one of deploymentID/resourceID
// is non-empty (git deployments vs. standalone Resources), and the workspace
// is keyed off whichever one applies.
func workspaceKey(deploymentID, resourceID string) string {
	if deploymentID != "" {
		return deploymentID
	}
	return resourceID
}

// run drives one plan/apply/destroy invocation through the full
// pending -> running -> succeeded/failed lifecycle: it records and publishes
// the run before and after executing step, and rejects a second concurrent
// invocation for the same workspace with a clear error rather than letting
// terraform's own state-lock error surface (matches the "deterministic
// control plane, no surprise queueing" framing).
func (r *Runner) run(ctx context.Context, deploymentID, resourceID, cloudAccountID, template, action string, recordState bool, step func(context.Context) (string, error)) (*Run, error) {
	key := workspaceKey(deploymentID, resourceID)
	if !r.acquire(key) {
		return nil, fmt.Errorf("terraform: %s %s: a run is already in progress for this workspace", action, key)
	}
	defer r.release(key)

	run := &Run{
		DeploymentID:   deploymentID,
		ResourceID:     resourceID,
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
		dir, err := workspaceDir(r.baseDir, key)
		if err != nil {
			stepErr = err
		} else {
			path, err := stateFilePath(dir)
			if err != nil {
				stepErr = err
			} else {
				run.StateFilePath = path
			}
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
		return run, fmt.Errorf("terraform: %s %s: %w", action, key, stepErr)
	}
	return run, nil
}

func (r *Runner) acquire(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.inFlight[key]; busy {
		return false
	}
	r.inFlight[key] = struct{}{}
	return true
}

func (r *Runner) release(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, key)
}

func (r *Runner) insertRun(ctx context.Context, run *Run) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO terraform_runs (deployment_id, resource_id, cloud_account_id, template, action, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`, nullable(run.DeploymentID), nullable(run.ResourceID), nullable(run.CloudAccountID), run.Template, run.Action, run.Status).Scan(&run.ID, &run.CreatedAt)
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
