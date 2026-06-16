import { useState, useCallback, useEffect, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowLeft, Plus, Trash2, Loader2, RefreshCw, Download, AlertCircle, CheckCircle2, ExternalLink, X,
} from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { targetLabel, frameworkLabel } from '@/components/meta-tags'
import { TargetPill, FormField, inputCls, selectCls } from '@/components/deploy-form-fields'
import { Pipeline } from '@/components/pipeline'
import { apiFetch } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useSSE } from '@/lib/use-sse'
import { formatRelativeTime, provisionedTypeLabel, type ProvisionedResource } from '@/lib/resources'
import { cn } from '@/lib/utils'
import type { Target, PipelineStage } from '@/lib/mock-data'

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
  target_type: string // "k8s" | "container" | "vm"
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
  tls_status?: string
  tls_error?: string
  links: AppLink[]
  // Hydrates the deploy-progress pipeline on page load / SSE reconnect — see
  // DeployStepEvent and internal/api/handlers.go's handleGetDeployment.
  steps?: DeployStepEvent[]
}

interface StackDetail {
  id: string
  name: string
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
  const [actingId, setActingId] = useState<string | null>(null)
  const [confirmTarget, setConfirmTarget] = useState<{ kind: 'app'; app: AppDetail } | { kind: 'stack' } | null>(null)
  const [confirmLoading, setConfirmLoading] = useState(false)
  const [actionError, setActionError] = useState('')
  // Per-app live deploy-progress state, keyed by deployment_app_id — fed by
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
  }, [id]))

  async function handleRedeploy(appId: string) {
    if (!id) return
    setActingId(appId)
    try {
      await apiFetch(`/deployments/${id}/apps/${appId}/trigger`, { method: 'POST' })
    } catch { /* status surfaces via deploy.status SSE */ }
    finally { setActingId(null) }
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
          />
        ))}
      </div>

      <AddAppDialog
        stackId={stack.id}
        vmResources={vmResources}
        ecsResources={ecsResources}
        aciResources={aciResources}
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onCreated={() => refetch()}
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

function AppCard({
  app, stackId, allApps, resources, ingressDomain, publicHost, findings, acting, steps, logs, onRedeploy, onDelete, onDownloadWorkflow, onLinksChanged,
}: {
  app: AppDetail
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
  onDownloadWorkflow: () => void
  onLinksChanged: () => void
}) {
  const [linkOpen, setLinkOpen] = useState(false)
  const [deletingLinkId, setDeletingLinkId] = useState<string | null>(null)
  const [linkError, setLinkError] = useState('')
  const [secretFileOpen, setSecretFileOpen] = useState(false)
  const [removingSecretPath, setRemovingSecretPath] = useState<string | null>(null)
  const [secretFileError, setSecretFileError] = useState('')
  const [showLogs, setShowLogs] = useState(false)
  const logRef = useRef<HTMLPreElement>(null)
  const siblings = allApps.filter((a) => a.id !== app.id)

  const statusKind = appStatusMap[app.status] ?? 'unknown'
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

  const url = app.domain
    ? `https://${app.domain}`
    : app.target_type === 'k8s' && app.namespace && ingressDomain
      ? `https://${app.name}.${ingressDomain}`
      : app.target_type === 'container' && app.host_port
        ? `http://${containerHost}:${app.host_port}`
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

  return (
    <Card>
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="truncate">{app.name}</CardTitle>
          {app.repo_url && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">{app.repo_url}</p>
          )}
        </div>
        <StatusBadge status={statusKind} pulse={statusKind === 'building'} />
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
          </>
        ) : (
          <>
            {app.framework && (
              <InfoRow label="Framework" value={frameworkLabel[app.framework] ?? app.framework} />
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

        {showPipeline && (
          <div className="space-y-1.5 pt-2">
            <div className="flex items-center justify-between">
              <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Pipeline</p>
              {!!logs?.length && (
                <Button size="sm" variant="ghost" className="h-6 px-2 text-xs" onClick={() => setShowLogs((v) => !v)}>
                  {showLogs ? 'Hide logs' : 'Show logs'}
                </Button>
              )}
            </div>
            <Pipeline stages={steps!} compact />
            {showLogs && !!logs?.length && (
              <pre
                ref={logRef}
                className="max-h-48 overflow-y-auto whitespace-pre-wrap rounded-lg border border-border bg-background px-3 py-2 font-mono text-[0.65rem] text-muted-foreground"
              >
                {logs.join('\n')}
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
            <Button size="sm" variant="outline" className="gap-1.5" onClick={onDownloadWorkflow}>
              <Download className="size-3.5" />
              CI/CD workflow
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
  stackId, appId, siblingApps, resources, open, onClose, onCreated,
}: {
  stackId: string
  appId: string
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
  const showPort = !!selectedResource && PORTED_RESOURCE_TYPES.has(selectedResource.resource_type)

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
        <FormField label="Alias" hint="used as AUTODEVOPS_<ALIAS>_URL etc.">
          <input
            value={alias}
            onChange={(e) => setAlias(e.target.value.toUpperCase())}
            placeholder="BACKEND"
            required
            pattern="^[A-Za-z][A-Za-z0-9_]*$"
            className={inputCls}
          />
        </FormField>
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
  stackId, vmResources, ecsResources, aciResources, open, onClose, onCreated,
}: {
  stackId: string
  vmResources: ProvisionedResource[]
  ecsResources: ProvisionedResource[]
  aciResources: ProvisionedResource[]
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [kind, setKind] = useState<'git' | 'image'>('git')
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [gitToken, setGitToken] = useState('')
  const [target, setTarget] = useState<'k8s' | 'container' | 'vm' | 'ecs' | 'aci'>('k8s')
  const [vmResourceId, setVmResourceId] = useState('')
  const [ecsResourceId, setEcsResourceId] = useState('')
  const [aciResourceId, setAciResourceId] = useState('')
  const [domain, setDomain] = useState('')
  const [imageRef, setImageRef] = useState('')
  const [port, setPort] = useState('')
  const [volumePath, setVolumePath] = useState('')
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
    setName(''); setRepo(''); setBranch('main'); setGitToken(''); setTarget('k8s'); setVmResourceId(''); setDomain('')
    setEcsResourceId(''); setAciResourceId('')
    setImageRef(''); setPort(''); setVolumePath(''); setEnvRows([]); setSecretFileRows([])
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
    <Dialog open={open} onClose={handleClose} title="Add app">
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
          {kind === 'git' && (
            <FormField label="Custom domain" hint="optional">
              <input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" className={inputCls} />
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
              {loading ? 'Adding…' : 'Add app'}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}
