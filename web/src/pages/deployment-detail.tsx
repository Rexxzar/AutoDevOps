import { useState, useCallback } from 'react'
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
import { apiFetch } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useSSE } from '@/lib/use-sse'
import { formatRelativeTime, provisionedTypeLabel, type ProvisionedResource } from '@/lib/resources'
import { cn } from '@/lib/utils'
import type { Target } from '@/lib/mock-data'

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
  links: AppLink[]
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

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium text-right min-w-0 truncate">{value}</span>
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

  const { isLoading, error, refetch } = useQuery({
    queryKey: ['deployment', id],
    queryFn: async () => {
      const data = await apiFetch<StackDetail>(`/deployments/${id}`)
      setStack(data)
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
  // backend actually generated Ingress hosts for (see internal/api/handlers.go
  // handleHealth).
  const { data: health } = useQuery({
    queryKey: ['healthz'],
    queryFn: () => apiFetch<{ ingress_domain: string }>('/healthz'),
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

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type !== 'deploy.status') return
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
            findings={findings.filter((f) => f.deployment_id === app.id)}
            acting={actingId === app.id}
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
  app, stackId, allApps, resources, ingressDomain, findings, acting, onRedeploy, onDelete, onDownloadWorkflow, onLinksChanged,
}: {
  app: AppDetail
  stackId: string
  allApps: AppDetail[]
  resources: ProvisionedResource[]
  ingressDomain?: string
  findings: ComplianceFinding[]
  acting: boolean
  onRedeploy: () => void
  onDelete: () => void
  onDownloadWorkflow: () => void
  onLinksChanged: () => void
}) {
  const [linkOpen, setLinkOpen] = useState(false)
  const [deletingLinkId, setDeletingLinkId] = useState<string | null>(null)
  const [linkError, setLinkError] = useState('')
  const siblings = allApps.filter((a) => a.id !== app.id)

  const statusKind = appStatusMap[app.status] ?? 'unknown'
  const isTransitional = app.status === 'building' || app.status === 'deploying'
  const url = app.domain
    ? `https://${app.domain}`
    : app.target_type === 'k8s' && app.namespace && ingressDomain
      ? `https://${app.name}.${ingressDomain}`
      : app.target_type === 'container' && app.host_port
        ? `http://localhost:${app.host_port}`
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
        <InfoRow label="Updated" value={formatRelativeTime(app.updated_at)} />

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
    </Card>
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
  stackId, vmResources, open, onClose, onCreated,
}: {
  stackId: string
  vmResources: ProvisionedResource[]
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [kind, setKind] = useState<'git' | 'image'>('git')
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [gitToken, setGitToken] = useState('')
  const [target, setTarget] = useState<'k8s' | 'container' | 'vm'>('k8s')
  const [vmResourceId, setVmResourceId] = useState('')
  const [domain, setDomain] = useState('')
  const [imageRef, setImageRef] = useState('')
  const [port, setPort] = useState('')
  const [envRows, setEnvRows] = useState<{ key: string; value: string }[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState<{ webhookURL: string; webhookSecret: string } | null>(null)

  const effectiveVmResourceId = vmResourceId || vmResources[0]?.id || ''

  function reset() {
    setKind('git')
    setName(''); setRepo(''); setBranch('main'); setGitToken(''); setTarget('k8s'); setVmResourceId(''); setDomain('')
    setImageRef(''); setPort(''); setEnvRows([])
    setError(''); setSuccess(null)
  }

  // "Cloud VM" target is only offered for git apps with at least one applied
  // EC2/Azure VM resource (Phase 4's image-kind deploy path doesn't support
  // cloud_vm — see internal/deploy.DeployImageApp's default case).
  function selectKind(next: 'git' | 'image') {
    setKind(next)
    if (next === 'image' && target === 'vm') setTarget('k8s')
  }

  function handleClose() {
    reset()
    onClose()
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true); setError('')
    try {
      if (kind === 'image') {
        const env: Record<string, string> = {}
        for (const row of envRows) {
          if (row.key) env[row.key] = row.value
        }
        const body: Record<string, unknown> = {
          name, kind: 'image', image_ref: imageRef, port: Number(port), target_type: target,
        }
        if (Object.keys(env).length > 0) body.env = env
        await apiFetch<{ id: string }>(`/deployments/${stackId}/apps`, {
          method: 'POST', body: JSON.stringify(body),
        })
        onCreated()
        handleClose()
        return
      }

      const body: Record<string, unknown> = { name, kind: 'git', repo_url: repo, branch, target_type: target }
      if (target === 'vm') body.target_resource_id = effectiveVmResourceId
      if (gitToken) body.git_auth = { type: 'token', token: gitToken }
      if (domain) body.domain = domain
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
            </>
          )}
          <FormField label="Target">
            <div className="flex gap-2">
              <TargetPill label="Kubernetes" sub="kind cluster" active={target === 'k8s'} onClick={() => setTarget('k8s')} />
              <TargetPill label="Container" sub="Docker SDK" active={target === 'container'} onClick={() => setTarget('container')} />
              {kind === 'git' && vmResources.length > 0 && (
                <TargetPill label="Cloud VM" sub="SSH deploy" active={target === 'vm'} onClick={() => setTarget('vm')} />
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
