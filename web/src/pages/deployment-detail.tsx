import { useState, useCallback, useEffect, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowLeft, Plus, Trash2, Loader2, RefreshCw, Download, AlertCircle, CheckCircle2, ExternalLink, X, FileText, Database, Eye, EyeOff, Copy, Check,
} from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { targetLabel, frameworkLabel } from '@/components/meta-tags'
import { TargetPill, FormField, DomainField, K8sOptions, type K8sConfigValue, inputCls, selectCls } from '@/components/deploy-form-fields'
import { Pipeline } from '@/components/pipeline'
import { SetupGuide } from '@/components/setup-guide'
import { RepoEditor } from '@/components/repo-editor'
import { AddDatabaseDialog } from '@/components/add-database-dialog'
import { apiFetch } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useSSE } from '@/lib/use-sse'
import { formatRelativeTime, provisionedTypeLabel, type ProvisionedResource } from '@/lib/resources'
import { cn } from '@/lib/utils'
import type { Target, PipelineStage } from '@/lib/types'
import { diagnoseApp } from '@/lib/diagnose'
import { DECLARED_FRAMEWORK_LABELS, DECLARED_FRAMEWORK_HINTS, DECLARED_FRAMEWORK_OPTIONS } from '@/lib/frameworks'

interface AppLink {
  id: string
  alias: string
  depends_on_app_id?: string
  depends_on_app_name?: string
  depends_on_resource_id?: string
  depends_on_resource_name?: string
  port?: number
}

interface AppDetail {
  id: string
  name: string
  kind: string
  role?: string
  repo_url?: string
  branch: string
  framework?: string
  declared_framework?: string
  target_type: string // "k8s" | "container" | "vm" | "ecs" | "aci"
  target_resource_id?: string
  namespace?: string
  image_ref?: string
  port?: number
  domain?: string
  host_port?: number
  status: string
  created_at: string
  updated_at: string
  volume_path?: string
  secret_file_paths?: string[]
  compliance_enforce?: boolean
  tls_status?: string
  tls_error?: string
  k8s_config?: {
    replicas?: number
    cpu_request?: string
    cpu_limit?: string
    mem_request?: string
    mem_limit?: string
    hpa?: { min: number; max: number; target_cpu: number }
    liveness?: { path: string; port: number }
    readiness?: { path: string; port: number }
    rolling_update?: { max_surge: string; max_unavailable: string }
  }
  has_git_credentials: boolean
  has_image_env: boolean
  links: AppLink[]
  // Hydrates the deploy-progress pipeline on page load / SSE reconnect — see
  // DeployStepEvent and internal/api/handlers.go's handleGetDeployment.
  steps?: DeployStepEvent[]
}

interface StackDetail {
  id: string
  name: string
  namespace_override?: string
  created_at: string
  updated_at: string
  apps: AppDetail[]
}

interface ComplianceFinding {
  id: string
  deployment_id: string // actually the app id (compliance_findings.app_id)
  deployment_name: string
  scanner: string
  severity: 'critical' | 'high' | 'medium' | 'low' | 'info'
  rule_id: string
  message: string
  file_path?: string
  line_number?: number
  created_at: string
}

// deploy.status SSE payload (internal/deploy.Deployment) — id is the app id,
// deployment_id is the stack id.
interface DeployStatusEvent {
  id: string
  deployment_id: string
  status: string
  image_ref?: string
  namespace?: string
  updated_at: string
}

// deploy.step / deploy.log SSE payloads (internal/deploy.StepEvent / LogEvent).
interface DeployStepEvent {
  deployment_app_id: string
  step: string
  status: 'active' | 'done' | 'failed' | 'skipped'
  at: string
}

interface DeployLogEvent {
  deployment_app_id: string
  line: string
}

// STEP_ORDER mirrors internal/deploy/progress.go's Step* consts, in the order
// ProcessPush walks them — the deploy-progress Pipeline for "git"-kind apps.
const STEP_ORDER = ['clone', 'detect', 'build', 'push', 'compliance', 'deploy', 'tls'] as const
const STEP_LABELS: Record<string, string> = {
  clone: 'Clone',
  detect: 'Detect',
  build: 'Build',
  push: 'Push',
  compliance: 'Compliance',
  deploy: 'Deploy',
  tls: 'TLS',
}

function initialPipelineStages(): PipelineStage[] {
  return STEP_ORDER.map((step) => ({ name: STEP_LABELS[step], status: 'pending' }))
}

// Builds pipeline stages from a snapshot of deploy.step events (AppDetail.steps),
// applying the same per-step status mapping as the live deploy.step SSE handler —
// used to hydrate stepsByApp when a page loads (or its SSE stream reconnects)
// mid-deploy, since deploy.step events aren't replayed on reconnect.
function stagesFromStepEvents(events: DeployStepEvent[]): PipelineStage[] {
  const stages = initialPipelineStages()
  for (const e of events) {
    const idx = STEP_ORDER.indexOf(e.step as (typeof STEP_ORDER)[number])
    if (idx === -1) continue
    stages[idx] = { ...stages[idx], status: e.status }
  }
  return stages
}

// MAX_LOG_LINES caps the in-memory deploy.log buffer per app — enough for a
// "show verbose" scrollback without unbounded growth on long builds.
const MAX_LOG_LINES = 500

const appStatusMap: Record<string, StatusKind> = {
  pending: 'pending',
  building: 'building',
  deploying: 'building',
  running: 'ready',
  failed: 'failed',
  destroyed: 'idle',
}

const targetMap: Record<string, Target> = {
  k8s: 'kubernetes',
  container: 'docker',
  vm: 'vm',
}

// Presets for "image"-kind apps (Phase 4 — db containers etc.), mirroring
// new-resource.tsx's LOCAL_IMAGE_PRESETS pattern but paired with each image's
// default port so picking a preset also fills in the Container port field.
const IMAGE_APP_PRESETS: { image: string; port: number }[] = [
  { image: 'postgres:16', port: 5432 },
  { image: 'mysql:8', port: 3306 },
  { image: 'redis:7-alpine', port: 6379 },
  { image: 'mongo:7', port: 27017 },
]

const severityStyle: Record<string, string> = {
  critical: 'text-critical border-critical/25 bg-critical/10',
  high: 'text-warning border-warning/25 bg-warning/10',
  medium: 'text-info border-info/25 bg-info/10',
  low: 'text-muted-foreground border-border bg-muted/50',
  info: 'text-muted-foreground border-border bg-muted/30',
}

// fileToBase64 reads a File's contents and resolves to its base64 encoding
// (stripping the "data:...;base64," prefix from FileReader.readAsDataURL) —
// used to build the secret_files payload for createApp/createImageApp and
// handleUpdateSecretFiles.
function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result as string
      resolve(result.slice(result.indexOf(',') + 1))
    }
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

// fileInputCls styles a native file picker to roughly match inputCls's
// text-input look (Tailwind's file: variant targets the inner button).
const fileInputCls = cn(
  inputCls,
  'cursor-pointer file:mr-2 file:rounded-md file:border-0 file:bg-accent file:px-2 file:py-1 file:text-xs file:text-foreground',
)

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium text-right min-w-0 truncate">{value}</span>
    </div>
  )
}

function TLSFixBanner({ fixCmd }: { fixCmd: string }) {
  const [copied, setCopied] = useState(false)
  function handleCopy() {
    navigator.clipboard.writeText(fixCmd).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }
  return (
    <div className="mt-1 rounded-md border border-yellow-400/30 bg-yellow-50/60 dark:bg-yellow-950/30 p-2.5 text-xs space-y-1.5">
      <p className="font-medium text-yellow-800 dark:text-yellow-300 flex items-center gap-1">
        <AlertCircle className="size-3 shrink-0" /> Run this on the server to fix:
      </p>
      <pre className="overflow-x-auto rounded bg-background border border-border px-2 py-1.5 font-mono text-[0.7rem] whitespace-pre-wrap break-all">{fixCmd}</pre>
      <button
        type="button"
        onClick={handleCopy}
        className="text-[0.7rem] text-yellow-700 dark:text-yellow-400 hover:underline"
      >
        {copied ? '✓ Copied' : 'Copy to clipboard'}
      </button>
    </div>
  )
}

export function DeploymentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { token } = useAuth()
  const navigate = useNavigate()
  const [stack, setStack] = useState<StackDetail | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [addDbOpen, setAddDbOpen] = useState(false)
  const [actingId, setActingId] = useState<string | null>(null)
  const [confirmTarget, setConfirmTarget] = useState<{ kind: 'app'; app: AppDetail } | { kind: 'stack' } | null>(null)
  const [confirmLoading, setConfirmLoading] = useState(false)
  const [actionError, setActionError] = useState('')
  const [guideDismissed, setGuideDismissed] = useState(() => localStorage.getItem(`autodevops_guide_dismissed_${id}`) === 'true')
  // Per-app live deploy-progress state, keyed by deployment_app_id — fed by
  // Live container health — overlays DB status with real-time Docker state.
  // Keyed by app id; values: "running" | "restarting" | "stopped" | "failed" | "missing".
  const [containerHealthByApp, setContainerHealthByApp] = useState<Record<string, string>>({})

  // deploy.step/deploy.log SSE events, rendered as <Pipeline> in AppCard.
  const [stepsByApp, setStepsByApp] = useState<Record<string, PipelineStage[]>>({})
  const [logsByApp, setLogsByApp] = useState<Record<string, string[]>>({})

  const { isLoading, error, refetch } = useQuery({
    queryKey: ['deployment', id],
    queryFn: async () => {
      const data = await apiFetch<StackDetail>(`/deployments/${id}`)
      setStack(data)
      // Hydrate the pipeline for apps with a recorded step snapshot, but only
      // the first time — once live deploy.step SSE events start arriving for
      // an app, those take over and this snapshot must not overwrite them.
      setStepsByApp((prev) => {
        let next = prev
        for (const app of data.apps) {
          if (!app.steps?.length || prev[app.id]) continue
          if (next === prev) next = { ...prev }
          next[app.id] = stagesFromStepEvents(app.steps)
        }
        return next
      })
      return data
    },
    enabled: !!id,
    refetchInterval: 10_000,
  })

  const { data: findings = [] } = useQuery({
    queryKey: ['compliance-findings'],
    queryFn: () => apiFetch<ComplianceFinding[]>('/compliance/findings'),
    refetchInterval: 30_000,
  })

  // ingress_domain is server config (INGRESS_DOMAIN) — fetched once and
  // reused to build app URLs below, so they match whatever domain the
  // backend actually generated Ingress hosts for. public_host is the
  // server's PUBLIC_IP (or request-derived address) for container-target
  // app links (see internal/api/handlers.go handleHealth).
  const { data: health } = useQuery({
    queryKey: ['healthz'],
    queryFn: () => apiFetch<{ ingress_domain: string; public_host: string }>('/healthz'),
    staleTime: Infinity,
  })

  // Applied provisioned resources (Resources tab) — offered as link targets
  // alongside sibling apps in AddLinkDialog (Phase 5a).
  const { data: allResources = [] } = useQuery({
    queryKey: ['resources'],
    queryFn: () => apiFetch<ProvisionedResource[]>('/resources'),
    refetchInterval: 30_000,
  })
  const linkableResources = allResources.filter((r) => r.status === 'applied')
  // Applied EC2/Azure VM resources — offered as the cloud_vm target picker in
  // AddAppDialog (Phase 5b). resource_type "vm" only exists for the aws/azure
  // providers (see provisionedTypeLabel), so no provider check is needed.
  const vmResources = linkableResources.filter((r) => r.resource_type === 'vm')
  // Applied ECS Fargate / ACI container-group resources — offered as the
  // cloud_ecs/cloud_aci target pickers in AddAppDialog (Phase 8g). resource_type
  // "ecs"/"aci" are provider-specific (aws/azure respectively), so no provider
  // check is needed.
  const ecsResources = linkableResources.filter((r) => r.resource_type === 'ecs')
  const aciResources = linkableResources.filter((r) => r.resource_type === 'aci')

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type === 'deploy.status') {
      const d = data as DeployStatusEvent
      if (d.deployment_id !== id) return
      setStack((prev) => {
        if (!prev) return prev
        const idx = prev.apps.findIndex((a) => a.id === d.id)
        if (idx === -1) return prev
        const next = [...prev.apps]
        next[idx] = {
          ...next[idx],
          status: d.status,
          image_ref: d.image_ref || next[idx].image_ref,
          namespace: d.namespace || next[idx].namespace,
          updated_at: d.updated_at,
        }
        return { ...prev, apps: next }
      })
      return
    }

    if (type === 'deploy.step') {
      const e = data as DeployStepEvent
      const idx = STEP_ORDER.indexOf(e.step as (typeof STEP_ORDER)[number])
      if (idx === -1) return
      // A new "clone: active" marks the start of a fresh pipeline run — reset
      // this app's stages and log buffer so a redeploy doesn't show stale state.
      const isFreshRun = e.step === 'clone' && e.status === 'active'
      setStepsByApp((prev) => {
        const stages = isFreshRun ? initialPipelineStages() : [...(prev[e.deployment_app_id] ?? initialPipelineStages())]
        stages[idx] = { ...stages[idx], status: e.status }
        return { ...prev, [e.deployment_app_id]: stages }
      })
      if (isFreshRun) {
        setLogsByApp((prev) => ({ ...prev, [e.deployment_app_id]: [] }))
      }
      return
    }

    if (type === 'deploy.log') {
      const e = data as DeployLogEvent
      setLogsByApp((prev) => {
        const lines = [...(prev[e.deployment_app_id] ?? []), e.line]
        if (lines.length > MAX_LOG_LINES) lines.splice(0, lines.length - MAX_LOG_LINES)
        return { ...prev, [e.deployment_app_id]: lines }
      })
      return
    }

    if (type === 'container.health') {
      const e = data as { app_id?: string; kind?: string; status?: string }
      if (e.kind === 'deployment' && e.app_id && e.status) {
        setContainerHealthByApp((prev) => ({ ...prev, [e.app_id!]: e.status! }))
      }
      return
    }
  }, [id]))

  async function handleRedeploy(appId: string) {
    if (!id) return
    setActingId(appId)
    try {
      await apiFetch(`/deployments/${id}/apps/${appId}/trigger`, { method: 'POST' })
    } catch { /* status surfaces via deploy.status SSE */ }
    finally { setActingId(null) }
  }

  function dismissGuide() {
    localStorage.setItem(`autodevops_guide_dismissed_${id}`, 'true')
    setGuideDismissed(true)
  }

  function scrollToApp(appId: string) {
    const el = document.getElementById(`app-card-${appId}`)
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'center' })
      el.classList.add('ring-2', 'ring-primary', 'ring-offset-2')
      setTimeout(() => el.classList.remove('ring-2', 'ring-primary', 'ring-offset-2'), 2000)
    }
  }

  function requestDeleteApp(app: AppDetail) {
    setActionError('')
    setConfirmTarget({ kind: 'app', app })
  }

  function requestDestroyStack() {
    setActionError('')
    setConfirmTarget({ kind: 'stack' })
  }

  async function confirmDelete() {
    if (!confirmTarget || !id) return
    setConfirmLoading(true)
    setActionError('')
    try {
      if (confirmTarget.kind === 'app') {
        await apiFetch(`/deployments/${id}/apps/${confirmTarget.app.id}`, { method: 'DELETE' })
        await refetch()
        setConfirmTarget(null)
      } else {
        await apiFetch(`/deployments/${id}`, { method: 'DELETE' })
        navigate('/deployments')
      }
    } catch (err) {
      setActionError(err instanceof Error ? err.message : `Failed to delete ${confirmTarget.kind === 'app' ? 'app' : 'deployment'}`)
    } finally {
      setConfirmLoading(false)
    }
  }

  async function handleDownloadWorkflow(app: AppDetail) {
    if (!id) return
    const res = await fetch(`/api/deployments/${id}/apps/${app.id}/workflow`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    })
    if (!res.ok) return
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `autodevops-${app.name}.yml`
    a.click()
    URL.revokeObjectURL(url)
  }

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  }
  if (error || !stack) {
    return (
      <div className="p-6 space-y-3">
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => navigate('/deployments')}>
          <ArrowLeft className="size-3.5" />
          Back to Deployments
        </Button>
        <p className="text-sm text-muted-foreground">Deployment not found.</p>
      </div>
    )
  }

  const apps = stack.apps.filter((a) => a.status !== 'destroyed')
  const aggregateStatus: StatusKind =
    apps.length === 0 ? 'idle'
      : apps.some((a) => a.status === 'failed') ? 'failed'
        : apps.some((a) => a.status === 'building' || a.status === 'deploying') ? 'building'
          : apps.every((a) => a.status === 'running') ? 'ready'
            : 'pending'

  const confirmInfo = !confirmTarget ? null
    : confirmTarget.kind === 'app'
      ? {
          title: 'Delete app',
          message: `Delete app "${confirmTarget.app.name}"? This permanently removes its running container or pod and stops tracking it in AutoDevOps. This cannot be undone.`,
          confirmLabel: 'Delete app',
        }
      : {
          title: 'Delete deployment',
          message: apps.length === 0
            ? `Remove the empty "${stack.name}" deployment? This cannot be undone.`
            : `Delete the entire "${stack.name}" deployment? This permanently removes all ${apps.length} app${apps.length === 1 ? '' : 's'} — including their running containers, pods, and namespaces. This cannot be undone.`,
          confirmLabel: 'Delete deployment',
        }

  return (
    <div>
      <PageHeader title={stack.name} description={`${apps.length} app${apps.length === 1 ? '' : 's'}`}>
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => navigate('/deployments')}>
          <ArrowLeft className="size-3.5" />
          Back
        </Button>
        <StatusBadge status={aggregateStatus} pulse={aggregateStatus === 'building'} />
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setAddDbOpen(true)}>
          <Database className="size-3.5" />
          Add database
        </Button>
        <Button size="sm" className="gap-1.5" onClick={() => setAddOpen(true)}>
          <Plus className="size-3.5" />
          Add app
        </Button>
        <Button size="sm" variant="destructive" className="gap-1.5" onClick={requestDestroyStack}>
          <Trash2 className="size-3.5" />
          Delete deployment
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-4 max-w-3xl">
        {actionError && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{actionError}
          </div>
        )}
        {!guideDismissed && apps.length >= 2 && (
          <SetupGuide
            apps={apps}
            stackId={stack.id}
            onDismiss={dismissGuide}
            onScrollToApp={scrollToApp}
            onOpenAddDb={() => setAddDbOpen(true)}
            onRedeploy={handleRedeploy}
          />
        )}
        {apps.length === 0 && (
          <Card>
            <CardContent className="py-8 text-center text-sm text-muted-foreground">
              No apps in this stack yet. Add one to get started.
            </CardContent>
          </Card>
        )}
        {apps.map((app) => (
          <AppCard
            key={app.id}
            app={app}
            containerHealth={containerHealthByApp[app.id]}
            stackId={stack.id}
            allApps={apps}
            resources={linkableResources}
            ingressDomain={health?.ingress_domain}
            publicHost={health?.public_host}
            findings={findings.filter((f) => f.deployment_id === app.id)}
            acting={actingId === app.id}
            steps={stepsByApp[app.id]}
            logs={logsByApp[app.id]}
            onRedeploy={() => handleRedeploy(app.id)}
            onDelete={() => requestDeleteApp(app)}
            onDownloadWorkflow={() => handleDownloadWorkflow(app)}
            onLinksChanged={() => refetch()}
            onOpenAddDb={() => setAddDbOpen(true)}
          />
        ))}
        {(() => {
          const linkedResourceIds = new Set<string>()
          apps.forEach((a) => a.links.forEach((l) => { if (l.depends_on_resource_id) linkedResourceIds.add(l.depends_on_resource_id) }))
          const linkedResources = allResources.filter((r) => linkedResourceIds.has(r.id))
          if (linkedResources.length === 0) return null
          return linkedResources.map((res) => {
            const linkedApps = apps.filter((a) => a.links.some((l) => l.depends_on_resource_id === res.id))
            const typeLabel: Record<string, string> = { db: 'Azure PostgreSQL', rds: 'AWS RDS', vm: 'Cloud VM', ecs: 'ECS Service', aci: 'ACI Container' }
            return (
              <Card key={res.id}>
                <CardHeader className="flex-row items-center justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <Database className="size-4 text-muted-foreground shrink-0" />
                    <CardTitle className="truncate">{res.name}</CardTitle>
                  </div>
                  <span className="text-xs rounded-full border px-2 py-0.5 text-muted-foreground shrink-0">
                    {typeLabel[res.resource_type] ?? res.resource_type}
                  </span>
                </CardHeader>
                <CardContent className="space-y-1 text-xs">
                  <div className="flex items-center gap-2">
                    <span className="text-muted-foreground w-20 shrink-0">Provider</span>
                    <span className="uppercase font-medium">{res.provider}</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-muted-foreground w-20 shrink-0">Region</span>
                    <span className="font-mono">{res.region}</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-muted-foreground w-20 shrink-0">Status</span>
                    <StatusBadge status={res.status === 'applied' ? 'running' : 'pending'} />
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-muted-foreground w-20 shrink-0">Used by</span>
                    <span>{linkedApps.map((a) => a.name).join(', ')}</span>
                  </div>
                </CardContent>
              </Card>
            )
          })
        })()}
      </div>

      <AddAppDialog
        stackId={stack.id}
        stackNamespace={stack.namespace_override || `autodevops-${stack.name.toLowerCase().replace(/_/g, '-')}`}
        vmResources={vmResources}
        ecsResources={ecsResources}
        aciResources={aciResources}
        serverIP={health?.public_host}
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onCreated={() => refetch()}
      />

      <AddDatabaseDialog
        stackId={stack.id}
        allApps={apps}
        open={addDbOpen}
        onClose={() => setAddDbOpen(false)}
        onAdded={() => refetch()}
      />

      <Dialog
        open={!!confirmTarget}
        onClose={() => { if (!confirmLoading) setConfirmTarget(null) }}
        title={confirmInfo?.title}
      >
        <div className="p-4 space-y-4">
          <p className="text-sm text-muted-foreground">{confirmInfo?.message}</p>
          {actionError && (
            <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertCircle className="size-4 shrink-0" />{actionError}
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="outline" onClick={() => setConfirmTarget(null)} disabled={confirmLoading}>
              Cancel
            </Button>
            <Button size="sm" variant="destructive" className="gap-1.5" onClick={confirmDelete} disabled={confirmLoading}>
              {confirmLoading && <Loader2 className="size-3.5 animate-spin" />}
              {confirmInfo?.confirmLabel}
            </Button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}

function CopyValue({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => { navigator.clipboard.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
      className="shrink-0 text-muted-foreground hover:text-foreground"
    >
      {copied ? <Check className="size-3 text-green-500" /> : <Copy className="size-3" />}
    </button>
  )
}


function DomainSetter({ stackId, appId, currentDomain, onSet, hint }: { stackId: string; appId: string; currentDomain?: string; onSet: () => void; hint?: string }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(currentDomain ?? '')
  const [saving, setSaving] = useState(false)

  async function save() {
    if (!value.trim()) return
    setSaving(true)
    try {
      await apiFetch(`/deployments/${stackId}/apps/${appId}/domain`, {
        method: 'PUT',
        body: JSON.stringify({ domain: value.trim() }),
      })
      setEditing(false)
      onSet()
    } catch { /* ignore */ } finally { setSaving(false) }
  }

  if (!editing) {
    return (
      <div className="flex items-center gap-2 py-0.5">
        <span className="text-xs text-muted-foreground w-20 shrink-0">Domain</span>
        {currentDomain
          ? <button onClick={() => { setValue(currentDomain); setEditing(true) }} className="text-xs font-mono text-foreground hover:text-primary hover:underline">{currentDomain} ✎</button>
          : <button onClick={() => setEditing(true)} className="text-xs text-primary hover:underline">+ Add custom domain</button>
        }
      </div>
    )
  }

  return (
    <div className="flex items-center gap-2 py-0.5">
      <span className="text-xs text-muted-foreground w-20 shrink-0">Domain</span>
      <input
        value={value} onChange={(e) => setValue(e.target.value)} placeholder="app.example.com"
        className="h-7 flex-1 rounded border bg-background px-2 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
        onKeyDown={(e) => { if (e.key === 'Enter') save() }}
        autoFocus
      />
      <Button size="sm" variant="outline" className="h-7 px-2 text-xs" onClick={save} disabled={saving || !value.trim()}>
        {saving ? <Loader2 className="size-3 animate-spin" /> : 'Set'}
      </Button>
      <Button size="sm" variant="ghost" className="h-7 px-1.5" onClick={() => setEditing(false)}>
        <X className="size-3" />
      </Button>
      {hint && <span className="text-[0.65rem] text-muted-foreground shrink-0">{hint}</span>}
    </div>
  )
}

function AppCard({
  app, containerHealth, stackId, allApps, resources, ingressDomain, publicHost, findings, acting, steps, logs, onRedeploy, onDelete, onDownloadWorkflow, onLinksChanged, onOpenAddDb,
}: {
  app: AppDetail
  containerHealth?: string
  stackId: string
  allApps: AppDetail[]
  resources: ProvisionedResource[]
  ingressDomain?: string
  publicHost?: string
  findings: ComplianceFinding[]
  acting: boolean
  steps?: PipelineStage[]
  logs?: string[]
  onRedeploy: () => void
  onDelete: () => void
  onOpenAddDb: () => void
  onDownloadWorkflow: () => void
  onLinksChanged: () => void
}) {
  const [linkOpen, setLinkOpen] = useState(false)
  const [deletingLinkId, setDeletingLinkId] = useState<string | null>(null)
  const [linkError, setLinkError] = useState('')
  const [secretFileOpen, setSecretFileOpen] = useState(false)
  const [removingSecretPath, setRemovingSecretPath] = useState<string | null>(null)
  const [secretFileError, setSecretFileError] = useState('')
  const [savingEnforce, setSavingEnforce] = useState(false)
  const [showLogs, setShowLogs] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)
  const [manifestsOpen, setManifestsOpen] = useState(false)
  const [envVisible, setEnvVisible] = useState(false)
  const [envData, setEnvData] = useState<Record<string, string> | null>(null)
  const [envLoading, setEnvLoading] = useState(false)
  const [envRevealed, setEnvRevealed] = useState<Set<string>>(new Set())
  const logRef = useRef<HTMLPreElement>(null)
  const siblings = allApps.filter((a) => a.id !== app.id)

  // Container health from SSE overrides stale DB status for live restarts.
  const liveStatus: string = (() => {
    if (!containerHealth || containerHealth === 'running') return app.status
    if (containerHealth === 'restarting') return 'restarting'
    return 'failed'
  })()
  const statusKind = appStatusMap[liveStatus] ?? (liveStatus === 'restarting' ? 'building' : 'unknown')
  const hints = diagnoseApp(app, logs, allApps)
  const isTransitional = app.status === 'building' || app.status === 'deploying'
  // Show the live pipeline while a deploy is in flight, or after a failure so
  // the user can see which step broke — hidden once a redeploy is started
  // (deploy.step resets the stages back to "pending" first).
  const showPipeline = app.kind === 'git' && !!steps?.some((s) => s.status !== 'pending') && (isTransitional || app.status === 'failed')

  useEffect(() => {
    if (showLogs && logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight
    }
  }, [logs, showLogs])

  // Loopback addresses aren't reachable from a browser on another machine —
  // fall back to "localhost" (the historical default, still correct for
  // local dev) rather than showing a meaningless ::1/127.0.0.1 link.
  const containerHost = publicHost && publicHost !== '::1' && publicHost !== '127.0.0.1' ? publicHost : 'localhost'

  const targetResource = app.target_resource_id ? resources.find((r) => r.id === app.target_resource_id) : undefined
  const targetOutputs: Record<string, string> = targetResource?.outputs ? (() => { try { return JSON.parse(targetResource.outputs) } catch { return {} } })() : {}
  const aciPortNum = targetOutputs.aci_port || '8080'
  const aciPort = aciPortNum !== '80' ? `:${aciPortNum}` : ''
  const aciUrl = targetOutputs.aci_fqdn ? `http://${targetOutputs.aci_fqdn}${aciPort}` : undefined
  const ecsUrl = targetOutputs.ecs_alb_dns_name ? `http://${targetOutputs.ecs_alb_dns_name}` : undefined

  const url = app.domain
    ? `https://${app.domain}`
    : app.target_type === 'k8s' && app.namespace && ingressDomain
      ? `https://${app.name}.${ingressDomain}`
      : app.target_type === 'container' && app.host_port
        ? `http://${containerHost}:${app.host_port}`
        : app.target_type === 'aci' && aciUrl
          ? aciUrl
          : app.target_type === 'ecs' && ecsUrl
            ? ecsUrl
            : null

  async function handleDeleteLink(linkId: string) {
    setDeletingLinkId(linkId)
    setLinkError('')
    try {
      await apiFetch(`/deployments/${stackId}/apps/${app.id}/links/${linkId}`, { method: 'DELETE' })
      onLinksChanged()
    } catch (err) {
      setLinkError(err instanceof Error ? err.message : 'Failed to remove connection')
    } finally {
      setDeletingLinkId(null)
    }
  }

  async function handleRemoveSecretFile(path: string) {
    setRemovingSecretPath(path)
    setSecretFileError('')
    try {
      await apiFetch(`/deployments/${stackId}/apps/${app.id}/secret-files`, {
        method: 'PUT', body: JSON.stringify({ remove: [path] }),
      })
      onLinksChanged()
    } catch (err) {
      setSecretFileError(err instanceof Error ? err.message : 'Failed to remove secret file')
    } finally {
      setRemovingSecretPath(null)
    }
  }

  async function toggleEnforce(next: boolean) {
    setSavingEnforce(true)
    try {
      await apiFetch(`/deployments/${stackId}/apps/${app.id}/compliance`, {
        method: 'PUT', body: JSON.stringify({ enforce: next }),
      })
      onLinksChanged()
    } catch {
      // best-effort; the next deployment GET reflects the true state
    } finally {
      setSavingEnforce(false)
    }
  }

  return (
    <Card id={`app-card-${app.id}`} className="transition-shadow">
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="truncate">{app.name}</CardTitle>
          {app.repo_url && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">{app.repo_url}</p>
          )}
        </div>
        <StatusBadge
          status={statusKind}
          pulse={statusKind === 'building'}
          label={liveStatus === 'restarting' ? 'Restarting…' : undefined}
        />
      </CardHeader>
      <CardContent className="space-y-1">
        <InfoRow label="Target" value={targetLabel[targetMap[app.target_type]] ?? app.target_type} />
        {app.kind === 'image' ? (
          <>
            {app.image_ref && (
              <InfoRow label="Image" value={<span className="font-mono text-xs">{app.image_ref}</span>} />
            )}
            {!!app.port && (
              <InfoRow label="Port" value={<span className="font-mono text-xs">{app.port}</span>} />
            )}
            {app.volume_path && (
              <InfoRow label="Volume" value={<span className="font-mono text-xs">{app.volume_path}</span>} />
            )}
            {app.has_image_env && (
              <div className="pt-1">
                <button
                  className="text-xs text-primary hover:underline"
                  onClick={async () => {
                    if (envData) { setEnvVisible((v) => !v); return }
                    setEnvLoading(true)
                    try {
                      const d = await apiFetch<{ env: Record<string, string> }>(`/deployments/${stackId}/apps/${app.id}/env`)
                      setEnvData(d.env)
                      setEnvVisible(true)
                    } finally { setEnvLoading(false) }
                  }}
                >
                  {envLoading ? <Loader2 className="inline size-3 animate-spin mr-1" /> : null}
                  {envVisible ? 'Hide credentials' : 'Show credentials'}
                </button>
                {envVisible && envData && (
                  <div className="mt-2 rounded-lg border divide-y text-xs">
                    {Object.entries(envData).map(([k, v]) => {
                      const isSecret = k.toLowerCase().includes('password') || k.toLowerCase().includes('pass') || k.toLowerCase().includes('secret')
                      const shown = envRevealed.has(k)
                      return (
                        <div key={k} className="flex items-center gap-2 px-3 py-1.5">
                          <span className="font-mono text-muted-foreground shrink-0 w-44 truncate">{k}</span>
                          <span className={`font-mono flex-1 truncate ${isSecret && !shown ? 'blur-sm select-none' : ''}`}>
                            {isSecret && !shown ? '••••••••••' : v}
                          </span>
                          {isSecret && (
                            <button onClick={() => setEnvRevealed((p) => { const n = new Set(p); n.has(k) ? n.delete(k) : n.add(k); return n })} className="shrink-0 text-muted-foreground hover:text-foreground">
                              {shown ? <EyeOff className="size-3" /> : <Eye className="size-3" />}
                            </button>
                          )}
                          <CopyValue value={v} />
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            )}
          </>
        ) : (
          <>
            {app.framework && (
              <InfoRow label="Framework" value={frameworkLabel[app.framework] ?? app.framework} />
            )}
            {app.declared_framework && (
              <InfoRow
                label="Declared"
                value={
                  <span className="inline-flex items-center gap-1">
                    <span>{DECLARED_FRAMEWORK_LABELS[app.declared_framework] ?? app.declared_framework}</span>
                    <span className="text-[0.65rem] text-muted-foreground">
                      {DECLARED_FRAMEWORK_HINTS[app.declared_framework]}
                    </span>
                  </span>
                }
              />
            )}
            <InfoRow label="Branch" value={<span className="font-mono text-xs">{app.branch}</span>} />
            {app.image_ref && (
              <InfoRow label="Image" value={<span className="font-mono text-xs">{app.image_ref}</span>} />
            )}
          </>
        )}
        {app.namespace && (
          <InfoRow label="Namespace" value={<span className="font-mono text-xs">{app.namespace}</span>} />
        )}
        {app.target_type === 'k8s' && app.k8s_config && (
          <div className="mt-2 space-y-1 rounded-lg border border-border bg-muted/30 px-3 py-2">
            <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground mb-1">Kubernetes Config</p>
            {app.k8s_config.replicas != null && (
              <InfoRow label="Replicas" value={<span className="font-mono text-xs">{app.k8s_config.replicas}</span>} />
            )}
            {(app.k8s_config.cpu_request || app.k8s_config.cpu_limit) && (
              <InfoRow
                label="CPU"
                value={
                  <span className="font-mono text-xs">
                    {app.k8s_config.cpu_request || '–'} / {app.k8s_config.cpu_limit || '–'}
                    <span className="text-muted-foreground ml-1">(req / lim)</span>
                  </span>
                }
              />
            )}
            {(app.k8s_config.mem_request || app.k8s_config.mem_limit) && (
              <InfoRow
                label="Memory"
                value={
                  <span className="font-mono text-xs">
                    {app.k8s_config.mem_request || '–'} / {app.k8s_config.mem_limit || '–'}
                    <span className="text-muted-foreground ml-1">(req / lim)</span>
                  </span>
                }
              />
            )}
            {app.k8s_config.hpa && (
              <InfoRow
                label="HPA"
                value={
                  <span className="font-mono text-xs">
                    {app.k8s_config.hpa.min}–{app.k8s_config.hpa.max} pods, target {app.k8s_config.hpa.target_cpu}% CPU
                  </span>
                }
              />
            )}
            {app.k8s_config.liveness && (
              <InfoRow
                label="Liveness"
                value={<span className="font-mono text-xs">{app.k8s_config.liveness.path}:{app.k8s_config.liveness.port}</span>}
              />
            )}
            {app.k8s_config.readiness && (
              <InfoRow
                label="Readiness"
                value={<span className="font-mono text-xs">{app.k8s_config.readiness.path}:{app.k8s_config.readiness.port}</span>}
              />
            )}
            {app.k8s_config.rolling_update && (
              <InfoRow
                label="Rolling"
                value={
                  <span className="font-mono text-xs">
                    surge {app.k8s_config.rolling_update.max_surge}, unavail {app.k8s_config.rolling_update.max_unavailable}
                  </span>
                }
              />
            )}
          </div>
        )}
        {app.kind === 'git' && url && (
          <InfoRow
            label="URL"
            value={
              <a href={url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary hover:underline">
                <span className="font-mono text-xs">{url}</span>
                <ExternalLink className="size-3" />
              </a>
            }
          />
        )}
        {app.kind === 'git' && (
          <DomainSetter stackId={stackId} appId={app.id} currentDomain={app.domain || undefined} onSet={onLinksChanged}
            hint={app.target_type === 'vm' && targetResource ? (() => {
              const o: Record<string, string> = (() => { try { return JSON.parse(targetResource.outputs) } catch { return {} } })()
              return o.vm_public_ip || o.ec2_public_ip ? `Point DNS to ${o.vm_public_ip || o.ec2_public_ip}` : undefined
            })() : (app.target_type === 'ecs' || app.target_type === 'aci') && publicHost
              ? `Point DNS to this server: ${publicHost} (TLS terminates here and proxies to ${app.target_type === 'ecs' ? 'the ECS load balancer' : 'the ACI container'})`
              : undefined}
          />
        )}
        {app.domain && app.tls_status && app.tls_status !== 'none' && (
          <>
            <InfoRow
              label="TLS"
              value={
                app.tls_status === 'active' ? (
                  <span className="inline-flex items-center gap-1 text-xs text-green-600 dark:text-green-400">
                    <CheckCircle2 className="size-3" /> Active
                  </span>
                ) : app.tls_status === 'provisioning' ? (
                  <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                    <Loader2 className="size-3 animate-spin" /> Provisioning…
                  </span>
                ) : app.tls_status === 'dns_failed' ? (
                  <span className="inline-flex items-center gap-1 text-xs text-yellow-600 dark:text-yellow-400">
                    <AlertCircle className="size-3" /> DNS mismatch
                  </span>
                ) : app.tls_status === 'cert_failed' ? (
                  <span className="inline-flex items-center gap-1 text-xs text-red-600 dark:text-red-400">
                    <AlertCircle className="size-3" /> Cert failed
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 text-xs text-red-600 dark:text-red-400">
                    <AlertCircle className="size-3" /> Failed
                  </span>
                )
              }
            />
            {app.tls_error && (
              <TLSFixBanner fixCmd={app.tls_error} />
            )}
          </>
        )}
        <InfoRow label="Updated" value={formatRelativeTime(app.updated_at)} />

        {hints.length > 0 && (
          <div className="mt-2 space-y-2">
            {hints.map((h) => (
              <div
                key={h.id}
                className={cn(
                  'flex items-start gap-2 rounded-lg border px-3 py-2.5 text-xs',
                  h.level === 'error' && 'border-critical/25 bg-critical/8 text-critical',
                  h.level === 'warning' && 'border-yellow-500/25 bg-yellow-500/8 text-yellow-700 dark:text-yellow-400',
                  h.level === 'info' && 'border-info/25 bg-info/8 text-info',
                )}
              >
                <AlertCircle className="size-3.5 mt-0.5 shrink-0" />
                <div className="flex-1 space-y-1.5">
                  <p>{h.message}</p>
                  {h.action === 'add_db' && (
                    <button onClick={onOpenAddDb} className="font-medium underline underline-offset-2 hover:no-underline">
                      {h.actionLabel ?? 'Add database'}
                    </button>
                  )}
                  {h.action === 'edit_files' && (
                    <button onClick={() => setEditorOpen(true)} className="font-medium underline underline-offset-2 hover:no-underline">
                      {h.actionLabel ?? 'Edit files'}
                    </button>
                  )}
                  {h.action === 'add_link' && (
                    <button onClick={() => setLinkOpen(true)} className="font-medium underline underline-offset-2 hover:no-underline">
                      {h.actionLabel ?? 'Add link'}
                    </button>
                  )}
                  {h.action === 'redeploy' && (
                    <button onClick={onRedeploy} className="font-medium underline underline-offset-2 hover:no-underline">
                      {h.actionLabel ?? 'Redeploy'}
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}

        {showPipeline && (
          <div className="space-y-1.5 pt-2">
            <div className="flex items-center justify-between">
              <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Pipeline</p>
              <Button size="sm" variant="ghost" className="h-6 px-2 text-xs" onClick={() => setShowLogs((v) => !v)}>
                  {showLogs ? 'Hide logs' : 'Show logs'}
                </Button>
            </div>
            <Pipeline stages={steps!} compact />
            {showLogs && (
              <pre
                ref={logRef}
                className="max-h-48 overflow-y-auto whitespace-pre-wrap rounded-lg border border-border bg-background px-3 py-2 font-mono text-[0.65rem] text-muted-foreground"
              >
                {logs?.length
                  ? logs.join('\n')
                  : 'No logs captured — logs are only kept while the server is running.\nRedeploy to see fresh output.'}
              </pre>
            )}
          </div>
        )}

        {app.kind === 'git' && (siblings.length > 0 || resources.length > 0 || app.links.length > 0) && (
          <div className="space-y-1.5 pt-2">
            <div className="flex items-center justify-between">
              <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Connections</p>
              {(siblings.length > 0 || resources.length > 0) && (
                <Button size="sm" variant="outline" className="h-6 gap-1 px-2 text-xs" onClick={() => setLinkOpen(true)}>
                  <Plus className="size-3" />
                  Connect
                </Button>
              )}
            </div>
            {linkError && (
              <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2 text-xs text-critical">
                <AlertCircle className="size-3.5 shrink-0" />{linkError}
              </div>
            )}
            {app.links.length === 0 ? (
              <p className="text-xs text-muted-foreground">Not connected to any other app or resource.</p>
            ) : (
              app.links.map((link) => (
                <div key={link.id} className="flex items-center justify-between gap-2 rounded-lg border border-border bg-background px-3 py-2 text-xs">
                  <span className="min-w-0 truncate">
                    <span className="font-mono text-primary">{link.alias}</span>
                    <span className="text-muted-foreground">
                      {' → '}{link.depends_on_app_name || link.depends_on_resource_name}
                      {link.port ? `:${link.port}` : ''}
                    </span>
                  </span>
                  <Button
                    size="sm" variant="ghost" className="h-6 w-6 shrink-0 p-0"
                    disabled={deletingLinkId === link.id}
                    onClick={() => handleDeleteLink(link.id)}
                  >
                    {deletingLinkId === link.id ? <Loader2 className="size-3 animate-spin" /> : <X className="size-3" />}
                  </Button>
                </div>
              ))
            )}
          </div>
        )}

        {(app.target_type === 'k8s' || app.target_type === 'container') && (
          <div className="space-y-1.5 pt-2">
            <div className="flex items-center justify-between">
              <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Secret files</p>
              <Button size="sm" variant="outline" className="h-6 gap-1 px-2 text-xs" onClick={() => setSecretFileOpen(true)}>
                <Plus className="size-3" />
                Add
              </Button>
            </div>
            {secretFileError && (
              <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2 text-xs text-critical">
                <AlertCircle className="size-3.5 shrink-0" />{secretFileError}
              </div>
            )}
            {!app.secret_file_paths?.length ? (
              <p className="text-xs text-muted-foreground">No secret files configured.</p>
            ) : (
              app.secret_file_paths.map((path) => (
                <div key={path} className="flex items-center justify-between gap-2 rounded-lg border border-border bg-background px-3 py-2 text-xs">
                  <span className="min-w-0 truncate font-mono">{path}</span>
                  <Button
                    size="sm" variant="ghost" className="h-6 w-6 shrink-0 p-0"
                    disabled={removingSecretPath === path}
                    onClick={() => handleRemoveSecretFile(path)}
                  >
                    {removingSecretPath === path ? <Loader2 className="size-3 animate-spin" /> : <X className="size-3" />}
                  </Button>
                </div>
              ))
            )}
          </div>
        )}

        {app.kind === 'git' && (
          <div className="flex items-center justify-between gap-3 pt-2">
            <div className="min-w-0">
              <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Compliance gate</p>
              <p className="text-xs text-muted-foreground">
                {app.compliance_enforce
                  ? 'Enforced: blocks on high+ secret, Dockerfile or IaC findings, and critical CVEs in your own dependencies. Base-image CVEs stay informational.'
                  : 'Report-only: findings are recorded and shown, but never block a deploy.'}
              </p>
            </div>
            <label className="flex shrink-0 cursor-pointer items-center gap-1.5 text-xs">
              {savingEnforce && <Loader2 className="size-3 animate-spin" />}
              <span className={cn(app.compliance_enforce ? 'text-foreground font-medium' : 'text-muted-foreground')}>
                {app.compliance_enforce ? 'Enforce' : 'Report-only'}
              </span>
              <input
                type="checkbox"
                checked={!!app.compliance_enforce}
                disabled={savingEnforce}
                onChange={(e) => toggleEnforce(e.target.checked)}
              />
            </label>
          </div>
        )}

        {findings.length > 0 && (
          <div className="space-y-1.5 pt-2">
            <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Compliance findings</p>
            {findings.slice(0, 5).map((f) => (
              <div key={f.id} className="flex items-start gap-2 rounded-lg border border-border bg-background px-3 py-2 text-xs">
                <span className={cn('shrink-0 rounded border px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase', severityStyle[f.severity])}>
                  {f.severity}
                </span>
                <span className="min-w-0 flex-1 truncate">{f.message}</span>
              </div>
            ))}
            {findings.length > 5 && (
              <p className="text-xs text-muted-foreground">+{findings.length - 5} more — see Compliance page</p>
            )}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          {app.kind === 'git' && (
            <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setEditorOpen(true)}>
              <FileText className="size-3.5" />
              Edit files
            </Button>
          )}
          {app.kind === 'git' && (
            <Button size="sm" variant="outline" className="gap-1.5" onClick={onDownloadWorkflow}>
              <Download className="size-3.5" />
              CI/CD workflow
            </Button>
          )}
          {app.target_type === 'k8s' && (
            <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setManifestsOpen(true)}>
              <FileText className="size-3.5" />
              Manifests
            </Button>
          )}
          <Button size="sm" variant="outline" className="gap-1.5" disabled={acting || isTransitional} onClick={onRedeploy}>
            {acting ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
            Redeploy
          </Button>
          <Button size="sm" variant="destructive" className="gap-1.5" disabled={acting} onClick={onDelete}>
            {acting ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
            Delete
          </Button>
        </div>
      </CardContent>

      <AddLinkDialog
        stackId={stackId}
        appId={app.id}
        consumerTarget={app.target_type}
        siblingApps={siblings}
        resources={resources}
        open={linkOpen}
        onClose={() => setLinkOpen(false)}
        onCreated={onLinksChanged}
      />

      <AddSecretFileDialog
        stackId={stackId}
        appId={app.id}
        open={secretFileOpen}
        onClose={() => setSecretFileOpen(false)}
        onAdded={onLinksChanged}
      />

      <RepoEditor
        stackId={stackId}
        appId={app.id}
        appName={app.name}
        hasGitCredentials={app.has_git_credentials}
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        onRedeployNeeded={onRedeploy}
      />

      {app.target_type === 'k8s' && (
        <K8sManifestsDialog
          stackId={stackId}
          appId={app.id}
          appName={app.name}
          open={manifestsOpen}
          onClose={() => setManifestsOpen(false)}
        />
      )}
    </Card>
  )
}

// AddSecretFileDialog is a single-row "path + file" form for
// handleUpdateSecretFiles' {add: {path: base64Content}} request — adding one
// secret file to an existing app and triggering a redeploy so the mount
// takes effect.
function AddSecretFileDialog({
  stackId, appId, open, onClose, onAdded,
}: {
  stackId: string
  appId: string
  open: boolean
  onClose: () => void
  onAdded: () => void
}) {
  const [path, setPath] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  function reset() {
    setPath(''); setFile(null); setError('')
  }

  function handleClose() {
    reset()
    onClose()
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!file) return
    setLoading(true); setError('')
    try {
      const content = await fileToBase64(file)
      await apiFetch(`/deployments/${stackId}/apps/${appId}/secret-files`, {
        method: 'PUT', body: JSON.stringify({ add: { [path]: content } }),
      })
      onAdded()
      handleClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add secret file')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onClose={handleClose} title="Add secret file">
      <form onSubmit={submit} className="p-4 space-y-4">
        <FormField label="Container path" hint="e.g. /opt/secrets/firebase.json">
          <input
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="/opt/secrets/firebase.json"
            required
            className={cn(inputCls, 'font-mono text-xs')}
          />
        </FormField>
        <FormField label="File">
          <input
            type="file"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            required
            className={fileInputCls}
          />
        </FormField>
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="outline" onClick={handleClose} disabled={loading}>Cancel</Button>
          <Button type="submit" size="sm" className="gap-1.5" disabled={loading}>
            {loading && <Loader2 className="size-3.5 animate-spin" />}
            {loading ? 'Adding…' : 'Add'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

// PORTED_RESOURCE_TYPES are provisioned-resource types with no inherent
// default port (a cloud VM, ECS service, or ACI container group could be
// running anything) — AddLinkDialog offers an optional Port field for these,
// matching resolveLinks's setHostPortURL (defaults to 80 if left blank).
// RDS/Azure DB resources have a natural default port and aren't listed here.
const PORTED_RESOURCE_TYPES = new Set(['vm', 'ecs', 'aci'])

function AddLinkDialog({
  stackId, appId, consumerTarget, siblingApps, resources, open, onClose, onCreated,
}: {
  stackId: string
  appId: string
  consumerTarget: string
  siblingApps: AppDetail[]
  resources: ProvisionedResource[]
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [alias, setAlias] = useState('')
  const [target, setTarget] = useState('')
  const [port, setPort] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const defaultTarget = siblingApps.length > 0 ? `app:${siblingApps[0].id}` : resources.length > 0 ? `resource:${resources[0].id}` : ''
  const effectiveTarget = target || defaultTarget
  const [targetKind, targetId] = effectiveTarget.split(':')
  const selectedResource = targetKind === 'resource' ? resources.find((r) => r.id === targetId) : undefined
  const selectedApp = targetKind === 'app' ? siblingApps.find((a) => a.id === targetId) : undefined
  const showPort = !!selectedResource && PORTED_RESOURCE_TYPES.has(selectedResource.resource_type)

  // A cloud-hosted consumer can only reach a LOCAL dependency (k8s/docker pod
  // or container) through that dependency's public domain — without one the
  // backend silently drops the link. Warn at link-creation so it's fixed up
  // front (matches internal/deploy/links.go's cross-target resolution rule).
  const consumerIsCloud = consumerTarget === 'vm' || consumerTarget === 'ecs' || consumerTarget === 'aci'
  const depIsLocal = !!selectedApp && (selectedApp.target_type === 'k8s' || selectedApp.target_type === 'container')
  const unreachableLocalDep = consumerIsCloud && depIsLocal && !selectedApp?.domain
  // aliasPreview drives the live env-var preview below; falls back to a
  // placeholder before the user types so the AUTODEVOPS_<ALIAS>_* shape is clear.
  const aliasPreview = alias || '<ALIAS>'

  function reset() {
    setAlias(''); setTarget(''); setPort(''); setError('')
  }

  function handleClose() {
    reset()
    onClose()
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true); setError('')
    try {
      const body: Record<string, unknown> = { alias }
      if (targetKind === 'resource') {
        body.depends_on_resource_id = targetId
        if (showPort && port) body.port = Number(port)
      } else {
        body.depends_on_app_id = targetId
      }
      await apiFetch(`/deployments/${stackId}/apps/${appId}/links`, {
        method: 'POST',
        body: JSON.stringify(body),
      })
      onCreated()
      handleClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add connection')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onClose={handleClose} title="Connect to an app or resource">
      <form onSubmit={submit} className="p-4 space-y-4">
        <FormField label="Alias" hint="names the env vars your code reads">
          <input
            value={alias}
            onChange={(e) => setAlias(e.target.value.toUpperCase())}
            placeholder="BACKEND"
            required
            pattern="^[A-Za-z][A-Za-z0-9_]*$"
            className={inputCls}
          />
        </FormField>

        {/* Live preview — the alias you type becomes the middle of the
            AUTODEVOPS_<ALIAS>_* env vars injected into this app. The app's code
            must read these exact names to reach the connected target, so showing
            them live removes the guesswork (and the domain/db-creds gotchas). */}
        <div className="rounded-lg border border-border bg-muted/40 px-3 py-2.5 space-y-2">
          <p className="text-[0.7rem] font-medium text-muted-foreground">
            This app will receive these environment variables — read them in your code:
          </p>
          <div className="space-y-0.5 font-mono text-xs text-foreground">
            <p>AUTODEVOPS_{aliasPreview}_URL <span className="font-sans text-muted-foreground/60">— full address</span></p>
            <p>AUTODEVOPS_{aliasPreview}_HOST <span className="font-sans text-muted-foreground/60">— hostname</span></p>
            <p>AUTODEVOPS_{aliasPreview}_PORT <span className="font-sans text-muted-foreground/60">— port</span></p>
          </div>
          <p className="text-[0.65rem] leading-relaxed text-muted-foreground/80">
            <span className="font-medium text-muted-foreground">Browser / frontend code</span> must use{' '}
            <span className="font-mono text-foreground">AUTODEVOPS_{aliasPreview}_DOMAIN</span> (the public https URL) —
            _HOST and _URL are internal and can&apos;t be reached from a browser.{' '}
            <span className="font-medium text-muted-foreground">Databases</span> also expose{' '}
            <span className="font-mono text-foreground">_USER</span>,{' '}
            <span className="font-mono text-foreground">_PASSWORD</span> and{' '}
            <span className="font-mono text-foreground">_DATABASE</span>.
          </p>
        </div>
        <FormField label="Connect to">
          <select
            value={effectiveTarget}
            onChange={(e) => setTarget(e.target.value)}
            className={inputCls}
            required
          >
            {siblingApps.length > 0 && (
              <optgroup label="Apps">
                {siblingApps.map((a) => (
                  <option key={a.id} value={`app:${a.id}`}>{a.name}</option>
                ))}
              </optgroup>
            )}
            {resources.length > 0 && (
              <optgroup label="Resources">
                {resources.map((r) => (
                  <option key={r.id} value={`resource:${r.id}`}>
                    {r.name} ({provisionedTypeLabel[r.provider]?.[r.resource_type] ?? r.resource_type})
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </FormField>
        {unreachableLocalDep && (
          <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2.5 text-xs text-amber-700 dark:text-amber-400">
            <AlertCircle className="size-4 shrink-0 mt-0.5" />
            <span>
              This app runs in the cloud, but <span className="font-medium">{selectedApp?.name}</span> is local with no public domain — the cloud can&apos;t reach it.
              Add a domain to <span className="font-medium">{selectedApp?.name}</span> first so this connection works.
            </span>
          </div>
        )}
        {showPort && (
          <FormField label="Port" hint="defaults to 80 if left blank">
            <input
              type="number"
              min={1}
              max={65535}
              value={port}
              onChange={(e) => setPort(e.target.value)}
              placeholder="80"
              className={inputCls}
            />
          </FormField>
        )}
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="outline" onClick={handleClose} disabled={loading}>Cancel</Button>
          <Button type="submit" size="sm" className="gap-1.5" disabled={loading}>
            {loading && <Loader2 className="size-3.5 animate-spin" />}
            {loading ? 'Connecting…' : 'Connect'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function AddAppDialog({
  stackId, stackNamespace, vmResources, ecsResources, aciResources, serverIP, open, onClose, onCreated,
}: {
  stackId: string
  stackNamespace: string
  vmResources: ProvisionedResource[]
  ecsResources: ProvisionedResource[]
  aciResources: ProvisionedResource[]
  serverIP?: string
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [kind, setKind] = useState<'git' | 'image'>('git')
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [gitToken, setGitToken] = useState('')
  const [declaredFramework, setDeclaredFramework] = useState('')
  const [target, setTarget] = useState<'k8s' | 'container' | 'vm' | 'ecs' | 'aci'>('k8s')
  const [vmResourceId, setVmResourceId] = useState('')
  const [ecsResourceId, setEcsResourceId] = useState('')
  const [aciResourceId, setAciResourceId] = useState('')
  const [domain, setDomain] = useState('')
  const [imageRef, setImageRef] = useState('')
  const [port, setPort] = useState('')
  const [volumePath, setVolumePath] = useState('')
  const [k8sConfig, setK8sConfig] = useState<K8sConfigValue | undefined>(undefined)
  const [envRows, setEnvRows] = useState<{ key: string; value: string }[]>([])
  const [secretFileRows, setSecretFileRows] = useState<{ path: string; file: File | null }[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState<{ webhookURL: string; webhookSecret: string } | null>(null)

  const effectiveVmResourceId = vmResourceId || vmResources[0]?.id || ''
  const effectiveEcsResourceId = ecsResourceId || ecsResources[0]?.id || ''
  const effectiveAciResourceId = aciResourceId || aciResources[0]?.id || ''

  function reset() {
    setKind('git')
    setName(''); setRepo(''); setBranch('main'); setGitToken(''); setDeclaredFramework(''); setTarget('k8s'); setVmResourceId(''); setDomain('')
    setEcsResourceId(''); setAciResourceId('')
    setImageRef(''); setPort(''); setVolumePath(''); setK8sConfig(undefined); setEnvRows([]); setSecretFileRows([])
    setError(''); setSuccess(null)
  }

  // "Cloud VM"/"AWS ECS"/"Azure ACI" targets are only offered for git apps with
  // at least one applied matching resource (Phase 4/8's image-kind deploy path
  // doesn't support cloud_vm/cloud_ecs/cloud_aci — see
  // internal/deploy.DeployImageApp's default case).
  function selectKind(next: 'git' | 'image') {
    setKind(next)
    if (next === 'image' && (target === 'vm' || target === 'ecs' || target === 'aci')) setTarget('k8s')
  }

  function handleClose() {
    reset()
    onClose()
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true); setError('')
    try {
      const secretFiles: Record<string, string> = {}
      for (const row of secretFileRows) {
        if (row.path && row.file) {
          secretFiles[row.path] = await fileToBase64(row.file)
        }
      }

      if (kind === 'image') {
        const env: Record<string, string> = {}
        for (const row of envRows) {
          if (row.key) env[row.key] = row.value
        }
        const body: Record<string, unknown> = {
          name, kind: 'image', image_ref: imageRef, port: Number(port), target_type: target,
        }
        if (Object.keys(env).length > 0) body.env = env
        if (volumePath) body.volume_path = volumePath
        if (Object.keys(secretFiles).length > 0) body.secret_files = secretFiles
        await apiFetch<{ id: string }>(`/deployments/${stackId}/apps`, {
          method: 'POST', body: JSON.stringify(body),
        })
        onCreated()
        handleClose()
        return
      }

      const body: Record<string, unknown> = { name, kind: 'git', repo_url: repo, branch, target_type: target }
      if (target === 'vm') body.target_resource_id = effectiveVmResourceId
      if (target === 'ecs') body.target_resource_id = effectiveEcsResourceId
      if (target === 'aci') body.target_resource_id = effectiveAciResourceId
      if (gitToken) body.git_auth = { type: 'token', token: gitToken }
      if (domain) body.domain = domain
      if (declaredFramework) body.declared_framework = declaredFramework
      if (target === 'k8s' && k8sConfig) body.k8s_config = k8sConfig
      if (Object.keys(secretFiles).length > 0) body.secret_files = secretFiles
      const resp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>(`/deployments/${stackId}/apps`, {
        method: 'POST', body: JSON.stringify(body),
      })
      setSuccess({ webhookURL: resp.webhook_url, webhookSecret: resp.webhook_secret })
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add app')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onClose={handleClose} title="Add app" className="max-w-xl">
      {success ? (
        <div className="p-4 space-y-4">
          <div className="flex items-center gap-2 text-success">
            <CheckCircle2 className="size-5 shrink-0" />
            <p className="text-sm font-medium">App added</p>
          </div>
          <div className="space-y-3 rounded-xl border border-border bg-background p-4">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">GitHub Webhook Setup</p>
            <div className="space-y-1">
              <p className="text-[0.7rem] text-muted-foreground">Payload URL</p>
              <p className="rounded-lg border border-border bg-card px-3 py-2 font-mono text-xs break-all select-all">
                http://YOUR_SERVER:8080{success.webhookURL}
              </p>
            </div>
            <div className="space-y-1">
              <p className="text-[0.7rem] text-muted-foreground">Secret</p>
              <p className="rounded-lg border border-border bg-card px-3 py-2 font-mono text-xs select-all">{success.webhookSecret}</p>
            </div>
          </div>
          <div className="flex justify-end">
            <Button size="sm" onClick={handleClose}>Done</Button>
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="p-4 space-y-4">
          <FormField label="App name">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder={kind === 'image' ? 'db' : 'frontend'} required className={inputCls} />
          </FormField>
          <FormField label="App type">
            <div className="flex gap-2">
              <TargetPill label="Git repo" sub="build from source" active={kind === 'git'} onClick={() => selectKind('git')} />
              <TargetPill label="Container image" sub="e.g. postgres:16" active={kind === 'image'} onClick={() => selectKind('image')} />
            </div>
          </FormField>
          {kind === 'git' ? (
            <>
              <FormField label="Git repository URL">
                <input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/you/my-app" required className={inputCls} />
              </FormField>
              <div className="grid grid-cols-2 gap-3">
                <FormField label="Branch">
                  <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" className={inputCls} />
                </FormField>
                <FormField label="Git token" hint="private repos">
                  <input value={gitToken} onChange={(e) => setGitToken(e.target.value)} placeholder="ghp_…" type="password" className={inputCls} />
                </FormField>
              </div>
              <FormField label="Framework" hint="auto-generates Procfile + DB env vars">
                <div className="flex flex-wrap gap-1.5">
                  {DECLARED_FRAMEWORK_OPTIONS.map((opt) => (
                    <button
                      key={opt.value} type="button"
                      onClick={() => setDeclaredFramework(opt.value)}
                      className={cn(
                        'rounded-full border px-2.5 py-1 text-xs transition-colors',
                        declaredFramework === opt.value
                          ? 'border-primary bg-primary/10 text-primary font-medium'
                          : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground',
                      )}
                    >
                      {opt.label}
                      {opt.sub && <span className="ml-1 opacity-60">{opt.sub}</span>}
                    </button>
                  ))}
                </div>
              </FormField>
            </>
          ) : (
            <>
              <FormField label="Container image">
                <input value={imageRef} onChange={(e) => setImageRef(e.target.value)} placeholder="postgres:16" required className={inputCls} />
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {IMAGE_APP_PRESETS.map((preset) => (
                    <button
                      key={preset.image} type="button"
                      onClick={() => { setImageRef(preset.image); if (!port) setPort(String(preset.port)) }}
                      className="rounded-full border border-border px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                    >
                      {preset.image}
                    </button>
                  ))}
                </div>
              </FormField>
              <FormField label="Container port">
                <input
                  type="number" min={1} max={65535}
                  value={port} onChange={(e) => setPort(e.target.value)}
                  placeholder="5432" required className={inputCls}
                />
              </FormField>
              <FormField label="Environment variables" hint="optional — e.g. POSTGRES_PASSWORD">
                <div className="space-y-2">
                  {envRows.map((row, i) => (
                    <div key={i} className="flex gap-2">
                      <input
                        value={row.key}
                        onChange={(e) => setEnvRows((rows) => rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))}
                        placeholder="POSTGRES_PASSWORD"
                        className={cn(inputCls, 'font-mono text-xs')}
                      />
                      <input
                        value={row.value}
                        onChange={(e) => setEnvRows((rows) => rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
                        placeholder="value"
                        type="password"
                        className={cn(inputCls, 'font-mono text-xs')}
                      />
                      <Button
                        type="button" size="sm" variant="ghost" className="h-9 w-9 shrink-0 p-0"
                        onClick={() => setEnvRows((rows) => rows.filter((_, j) => j !== i))}
                      >
                        <X className="size-3.5" />
                      </Button>
                    </div>
                  ))}
                  <Button
                    type="button" size="sm" variant="outline" className="gap-1.5"
                    onClick={() => setEnvRows((rows) => [...rows, { key: '', value: '' }])}
                  >
                    <Plus className="size-3.5" />
                    Add variable
                  </Button>
                </div>
              </FormField>
              <FormField label="Persistent storage path" hint="optional — e.g. /var/lib/postgresql/data">
                <input value={volumePath} onChange={(e) => setVolumePath(e.target.value)} placeholder="/var/lib/postgresql/data" className={cn(inputCls, 'font-mono text-xs')} />
              </FormField>
            </>
          )}
          <FormField label="Secret files" hint="optional — files mounted read-only into the container, e.g. /opt/secrets/firebase.json">
            <div className="space-y-2">
              {secretFileRows.map((row, i) => (
                <div key={i} className="flex gap-2">
                  <input
                    value={row.path}
                    onChange={(e) => setSecretFileRows((rows) => rows.map((r, j) => (j === i ? { ...r, path: e.target.value } : r)))}
                    placeholder="/opt/secrets/firebase.json"
                    className={cn(inputCls, 'font-mono text-xs')}
                  />
                  <input
                    type="file"
                    onChange={(e) => setSecretFileRows((rows) => rows.map((r, j) => (j === i ? { ...r, file: e.target.files?.[0] ?? null } : r)))}
                    className={cn(fileInputCls, 'text-xs')}
                  />
                  <Button
                    type="button" size="sm" variant="ghost" className="h-9 w-9 shrink-0 p-0"
                    onClick={() => setSecretFileRows((rows) => rows.filter((_, j) => j !== i))}
                  >
                    <X className="size-3.5" />
                  </Button>
                </div>
              ))}
              <Button
                type="button" size="sm" variant="outline" className="gap-1.5"
                onClick={() => setSecretFileRows((rows) => [...rows, { path: '', file: null }])}
              >
                <Plus className="size-3.5" />
                Add secret file
              </Button>
            </div>
          </FormField>
          <FormField label="Target">
            <div className="flex gap-2">
              <TargetPill label="Kubernetes" sub="kind cluster" active={target === 'k8s'} onClick={() => setTarget('k8s')} />
              <TargetPill label="Container" sub="Docker SDK" active={target === 'container'} onClick={() => setTarget('container')} />
              {kind === 'git' && vmResources.length > 0 && (
                <TargetPill label="Cloud VM" sub="SSH deploy" active={target === 'vm'} onClick={() => setTarget('vm')} />
              )}
              {kind === 'git' && ecsResources.length > 0 && (
                <TargetPill label="AWS ECS" sub="Fargate service" active={target === 'ecs'} onClick={() => setTarget('ecs')} />
              )}
              {kind === 'git' && aciResources.length > 0 && (
                <TargetPill label="Azure ACI" sub="container group" active={target === 'aci'} onClick={() => setTarget('aci')} />
              )}
            </div>
          </FormField>
          {target === 'vm' && (
            <FormField label="VM resource" hint="from the Resources page">
              <select value={effectiveVmResourceId} onChange={(e) => setVmResourceId(e.target.value)} className={selectCls} required>
                {vmResources.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({provisionedTypeLabel[r.provider]?.[r.resource_type] ?? r.resource_type})
                  </option>
                ))}
              </select>
            </FormField>
          )}
          {target === 'ecs' && (
            <FormField label="ECS service" hint="from the Resources page">
              <select value={effectiveEcsResourceId} onChange={(e) => setEcsResourceId(e.target.value)} className={selectCls} required>
                {ecsResources.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({provisionedTypeLabel[r.provider]?.[r.resource_type] ?? r.resource_type})
                  </option>
                ))}
              </select>
            </FormField>
          )}
          {target === 'aci' && (
            <FormField label="ACI container group" hint="from the Resources page">
              <select value={effectiveAciResourceId} onChange={(e) => setAciResourceId(e.target.value)} className={selectCls} required>
                {aciResources.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({provisionedTypeLabel[r.provider]?.[r.resource_type] ?? r.resource_type})
                  </option>
                ))}
              </select>
            </FormField>
          )}
          {(target === 'ecs' || target === 'aci') && (
            <div className="rounded-lg border border-blue-500/20 bg-blue-500/5 px-3.5 py-2.5 text-xs text-muted-foreground space-y-1.5">
              <p className="font-medium text-foreground">How {target === 'ecs' ? 'ECS' : 'ACI'} deployment works</p>
              <ol className="list-decimal list-inside space-y-0.5">
                <li>Provision a <strong>{target === 'ecs' ? 'ECR registry' : 'ACR registry'}</strong> from the Resources page</li>
                <li>Provision an <strong>{target === 'ecs' ? 'ECS service' : 'ACI container group'}</strong> (any placeholder image is fine)</li>
                <li>Link the registry to the {target === 'ecs' ? 'ECS service' : 'ACI container group'} on its detail page</li>
                <li>Deploy here — AutoDevOps builds your app, pushes to the registry, and redeploys</li>
              </ol>
            </div>
          )}
          {kind === 'git' && (() => {
            let vmTargetIP: string | undefined
            if (target === 'vm') {
              const vmRes = vmResources.find((r) => r.id === effectiveVmResourceId)
              const vmOutputs: Record<string, string> = vmRes?.outputs ? (() => { try { return JSON.parse(vmRes.outputs) } catch { return {} } })() : {}
              vmTargetIP = vmOutputs.vm_public_ip || vmOutputs.ec2_public_ip
            }
            return <DomainField value={domain} onChange={setDomain} targetIP={vmTargetIP} />
          })()}
          {kind === 'git' && (target === 'aci' || target === 'ecs') && !domain && (
            <p className="text-xs text-muted-foreground -mt-1">
              Optional — {target === 'aci' ? 'ACI' : 'ECS'} gets a public URL automatically. Add a domain above for HTTPS via your own domain name.
            </p>
          )}
          {kind === 'git' && (target === 'ecs' || target === 'aci') && serverIP && (
            <p className="text-xs text-muted-foreground -mt-1">
              For HTTPS, point your domain to this server: <code className="rounded bg-muted px-1 font-mono">{serverIP}</code>{' '}
              — TLS terminates here and proxies to {target === 'ecs' ? 'the ECS load balancer' : 'the ACI container'} (not the {target === 'ecs' ? 'ALB' : 'ACI'} IP directly).
            </p>
          )}
          {kind === 'git' && target === 'vm' && (() => {
            const vmRes = vmResources.find((r) => r.id === effectiveVmResourceId)
            const vmOutputs: Record<string, string> = vmRes?.outputs ? (() => { try { return JSON.parse(vmRes.outputs) } catch { return {} } })() : {}
            const vmIP = vmOutputs.vm_public_ip || vmOutputs.ec2_public_ip
            return vmIP ? (
              <p className="text-xs text-muted-foreground -mt-1">
                Point your domain to the VM&apos;s IP: <code className="rounded bg-muted px-1 font-mono">{vmIP}</code>
              </p>
            ) : null
          })()}
          {kind === 'git' && target === 'k8s' && (
            <>
              <FormField label="Namespace" hint="set on the stack">
                <input value={stackNamespace} disabled className={cn(inputCls, 'opacity-60')} />
              </FormField>
              <K8sOptions onChange={setK8sConfig} />
            </>
          )}
          {error && (
            <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertCircle className="size-4 shrink-0" />{error}
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="outline" onClick={handleClose} disabled={loading}>Cancel</Button>
            <Button type="submit" size="sm" className="gap-1.5" disabled={loading}>
              {loading && <Loader2 className="size-3.5 animate-spin" />}
              {loading ? 'Adding…' : 'Add app'}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}

function K8sManifestsDialog({
  stackId, appId, appName, open, onClose,
}: {
  stackId: string
  appId: string
  appName: string
  open: boolean
  onClose: () => void
}) {
  const [yaml, setYaml] = useState('')
  const [editedYaml, setEditedYaml] = useState('')
  const [mode, setMode] = useState<'live' | 'template'>('live')
  const [loading, setLoading] = useState(false)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState('')
  const [applyResult, setApplyResult] = useState('')
  const [isEditing, setIsEditing] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const fetchManifests = useCallback(async (forceTemplate = false) => {
    setLoading(true)
    setError('')
    setApplyResult('')
    try {
      const url = `/deployments/${stackId}/apps/${appId}/k8s-manifests${forceTemplate ? '?mode=template' : ''}`
      const data = await apiFetch<{ yaml: string; mode: 'live' | 'template' }>(url)
      setYaml(data.yaml)
      setEditedYaml(data.yaml)
      setMode(data.mode)
      setIsEditing(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load manifests')
    } finally {
      setLoading(false)
    }
  }, [stackId, appId])

  useEffect(() => {
    if (open) fetchManifests()
  }, [open, fetchManifests])

  async function handleApply() {
    setApplying(true)
    setError('')
    setApplyResult('')
    try {
      const data = await apiFetch<{ applied: number; message: string }>(
        `/deployments/${stackId}/apps/${appId}/k8s-apply`,
        { method: 'POST', body: JSON.stringify({ yaml: editedYaml }) },
      )
      setApplyResult(data.message)
      setIsEditing(false)
      setTimeout(() => fetchManifests(), 1000)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to apply manifests')
    } finally {
      setApplying(false)
    }
  }

  function handleDownload() {
    const blob = new Blob([isEditing ? editedYaml : yaml], { type: 'text/yaml' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${appName}-k8s-manifests.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  function handleClose() {
    setYaml('')
    setEditedYaml('')
    setError('')
    setApplyResult('')
    setIsEditing(false)
    onClose()
  }

  return (
    <Dialog open={open} onClose={handleClose} title={`K8s Manifests — ${appName}`} className="max-w-3xl">
      <div className="p-4 space-y-3">
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <div className="flex items-center gap-2">
            <span className={cn(
              'inline-flex items-center rounded-full px-2 py-0.5 text-[0.65rem] font-medium',
              mode === 'live'
                ? 'bg-green-500/15 text-green-700 dark:text-green-400'
                : 'bg-blue-500/15 text-blue-700 dark:text-blue-400',
            )}>
              {mode === 'live' ? 'Live cluster state' : 'Generated template'}
            </span>
            {mode === 'live' && (
              <button
                className="text-xs text-primary hover:underline"
                onClick={() => fetchManifests(true)}
                disabled={loading}
              >
                Switch to template
              </button>
            )}
            {mode === 'template' && (
              <button
                className="text-xs text-primary hover:underline"
                onClick={() => fetchManifests(false)}
                disabled={loading}
              >
                Switch to live
              </button>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button size="sm" variant="outline" className="gap-1.5" onClick={handleDownload} disabled={loading || !yaml}>
              <Download className="size-3" />
              Download
            </Button>
            {!isEditing ? (
              <Button size="sm" variant="outline" className="gap-1.5" onClick={() => { setIsEditing(true); setEditedYaml(yaml) }} disabled={loading || !yaml}>
                <FileText className="size-3" />
                Edit
              </Button>
            ) : (
              <>
                <Button size="sm" variant="outline" className="gap-1.5" onClick={() => { setIsEditing(false); setEditedYaml(yaml) }}>
                  Cancel
                </Button>
                <Button size="sm" className="gap-1.5" onClick={handleApply} disabled={applying || !editedYaml.trim()}>
                  {applying ? <Loader2 className="size-3 animate-spin" /> : <CheckCircle2 className="size-3" />}
                  {applying ? 'Applying…' : 'Apply to cluster'}
                </Button>
              </>
            )}
          </div>
        </div>

        {isEditing && (
          <div className="flex items-center gap-2 rounded-lg border border-yellow-500/25 bg-yellow-500/8 px-3 py-2 text-xs text-yellow-700 dark:text-yellow-400">
            <AlertCircle className="size-3.5 shrink-0" />
            Manual edits will be overwritten on the next git push or redeploy.
          </div>
        )}

        {loading ? (
          <div className="flex items-center justify-center py-12">
            <Loader2 className="size-5 animate-spin text-muted-foreground" />
          </div>
        ) : isEditing ? (
          <textarea
            ref={textareaRef}
            value={editedYaml}
            onChange={(e) => setEditedYaml(e.target.value)}
            className="w-full min-h-[28rem] rounded-lg border border-border bg-card p-4 text-xs font-mono leading-relaxed whitespace-pre resize-y focus:outline-none focus:ring-2 focus:ring-ring"
            spellCheck={false}
          />
        ) : (
          <pre className="max-h-[28rem] overflow-auto rounded-lg border border-border bg-card p-4 text-xs font-mono leading-relaxed whitespace-pre">
            {yaml || 'No manifests available'}
          </pre>
        )}

        {applyResult && (
          <div className="flex items-center gap-2 rounded-lg border border-green-500/25 bg-green-500/8 px-3 py-2.5 text-xs text-green-700 dark:text-green-400">
            <CheckCircle2 className="size-3.5 shrink-0" />{applyResult}
          </div>
        )}
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-xs text-critical">
            <AlertCircle className="size-3.5 shrink-0" />{error}
          </div>
        )}
      </div>
    </Dialog>
  )
}
