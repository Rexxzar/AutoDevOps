package deploy

import (
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
)

// Step identifiers published as "deploy.step" SSE events — the canonical
// pipeline ProcessPush walks through for "git"-kind apps, in order. The
// frontend's deploy-progress UI (web/src/components/pipeline.tsx) renders
// exactly these stages, keyed by deployment_app_id.
const (
	StepClone      = "clone"
	StepDetect     = "detect"
	StepBuild      = "build"
	StepPush       = "push"
	StepCompliance = "compliance"
	StepDeploy     = "deploy"
)

// Step statuses — mirrors web/src/lib/mock-data.ts's PipelineStage.status.
const (
	StepActive  = "active"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// StepEvent is the "deploy.step" SSE payload.
type StepEvent struct {
	AppID  string    `json:"deployment_app_id"`
	Step   string    `json:"step"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

// LogEvent is the "deploy.log" SSE payload — one line of build output, for
// the deploy-progress UI's verbose log toggle.
type LogEvent struct {
	AppID string `json:"deployment_app_id"`
	Line  string `json:"line"`
}

// publishStep broadcasts a pipeline-step transition for appID.
func (o *Orchestrator) publishStep(appID, step, status string) {
	o.hub.Publish(sse.Event{Type: "deploy.step", Payload: StepEvent{AppID: appID, Step: step, Status: status, At: time.Now()}})
}

// publishLog broadcasts a single build-output line for appID.
func (o *Orchestrator) publishLog(appID, line string) {
	o.hub.Publish(sse.Event{Type: "deploy.log", Payload: LogEvent{AppID: appID, Line: line}})
}
