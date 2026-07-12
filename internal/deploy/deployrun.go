package deploy

import (
	"context"
	"log/slog"
)

// startDeployRun opens a deploy_runs row for one pipeline execution and returns
// its id. It is best-effort: on any error it logs and returns "", so deploy
// timing (an evaluation concern, see Chapter 5) can never affect the deploy.
func (o *Orchestrator) startDeployRun(ctx context.Context, appID, deploymentID, appName, targetKind string) string {
	var id string
	err := o.pool.QueryRow(ctx, `
		INSERT INTO deploy_runs (app_id, deployment_id, app_name, target_kind)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4)
		RETURNING id::text
	`, appID, deploymentID, appName, targetKind).Scan(&id)
	if err != nil {
		slog.Warn("deploy: open deploy_run", "app_id", appID, "error", err)
		return ""
	}
	return id
}

// finishDeployRun closes a deploy_runs row opened by startDeployRun, stamping
// finished_at and marking it succeeded (runErr == nil) or failed. It is a no-op
// when id is empty (the open failed) and best-effort otherwise.
func (o *Orchestrator) finishDeployRun(ctx context.Context, id string, runErr error) {
	if id == "" {
		return
	}
	status := "succeeded"
	if runErr != nil {
		status = "failed"
	}
	if _, err := o.pool.Exec(ctx,
		`UPDATE deploy_runs SET finished_at = now(), status = $2 WHERE id = $1`,
		id, status,
	); err != nil {
		slog.Warn("deploy: close deploy_run", "id", id, "error", err)
	}
}
