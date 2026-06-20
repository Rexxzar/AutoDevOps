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
	StepTLS        = "tls" // cert + nginx vhost provisioning (all targets with domain)
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

// publishStep broadcasts a pipeline-step transition for appID and records it
// as that app's current step state (see CurrentSteps).
func (o *Orchestrator) publishStep(appID, step, status string) {
	e := StepEvent{AppID: appID, Step: step, Status: status, At: time.Now()}
	o.recordStep(e)
	o.hub.Publish(sse.Event{Type: "deploy.step", Payload: e})
}

// recordStep updates appID's current step state. A fresh "clone: active"
// marks the start of a new pipeline run and clears any prior run's state
// first, mirroring the frontend's isFreshRun reset.
func (o *Orchestrator) recordStep(e StepEvent) {
	o.stepsMu.Lock()
	defer o.stepsMu.Unlock()
	if e.Step == StepClone && e.Status == StepActive {
		o.steps[e.AppID] = nil
	}
	if o.steps[e.AppID] == nil {
		o.steps[e.AppID] = make(map[string]StepEvent)
	}
	o.steps[e.AppID][e.Step] = e
}

// CurrentSteps returns appID's latest recorded step states, ordered per
// STEP_ORDER (web/src/pages/deployment-detail.tsx). It lets a dashboard page
// that loads (or reconnects its SSE stream) mid-deploy hydrate its pipeline
// view, since deploy.step events aren't replayed on reconnect. Returns nil
// if no pipeline run has been recorded for appID since server start.
func (o *Orchestrator) CurrentSteps(appID string) []StepEvent {
	o.stepsMu.RLock()
	defer o.stepsMu.RUnlock()
	steps := o.steps[appID]
	if len(steps) == 0 {
		return nil
	}
	order := []string{StepClone, StepDetect, StepBuild, StepPush, StepCompliance, StepDeploy, StepTLS}
	out := make([]StepEvent, 0, len(steps))
	for _, name := range order {
		if e, ok := steps[name]; ok {
			out = append(out, e)
		}
	}
	return out
}

// publishLog broadcasts a single build-output line for appID.
func (o *Orchestrator) publishLog(appID, line string) {
	o.hub.Publish(sse.Event{Type: "deploy.log", Payload: LogEvent{AppID: appID, Line: line}})
}
