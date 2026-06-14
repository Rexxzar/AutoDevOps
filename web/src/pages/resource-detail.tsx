import { useState, useCallback, useEffect } from 'react'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Play, Trash2, X, Loader2, Copy, Check, AlertTriangle, FileCode, Download, Pencil, SquareTerminal } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { ResourceConsole } from '@/components/resource-console'
import { ProviderTag } from '@/components/meta-tags'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import type { Provider } from '@/lib/mock-data'
import { LocalFields, type LocalResource } from '@/pages/new-resource'
import {
  type ProvisionedResource,
  type TerraformRun,
  provisionedTypeLabel,
  provisionedStatusMeta,
  formatRelativeTime,
  friendlyTerraformError,
  parseOutputs,
} from '@/lib/resources'

function CopyField({ label, value, href }: { label: string; value: string; href?: string }) {
  const [copied, setCopied] = useState(false)

  function copy() {
    navigator.clipboard.writeText(value)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-background px-3 py-2">
      <div className="min-w-0">
        <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">{label}</p>
        {href ? (
          <a href={href} target="_blank" rel="noreferrer" className="truncate font-mono text-sm text-primary hover:underline block">
            {value}
          </a>
        ) : (
          <p className="truncate font-mono text-sm">{value}</p>
        )}
      </div>
      <button
        onClick={copy}
        className="flex size-7 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
        aria-label={`Copy ${label}`}
      >
        {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
      </button>
    </div>
  )
}

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium text-right">{value}</span>
    </div>
  )
}

// dockerHealthKind maps internal/docker.ContainerHealth.Status ("running" |
// "stopped" | "failed" | "missing" | "unknown") to a StatusBadge tone.
function dockerHealthKind(status: string): StatusKind {
  switch (status) {
    case 'running': return 'running'
    case 'stopped': return 'warning'
    case 'failed': return 'failed'
    default: return 'unknown'
  }
}

// podHealthKind maps internal/k8s.PodStatus ("healthy" | "unhealthy" |
// "pending" | "terminating" | "succeeded" | "unknown") to a StatusBadge tone.
function podHealthKind(status: string): StatusKind {
  switch (status) {
    case 'healthy': return 'healthy'
    case 'unhealthy': return 'critical'
    case 'pending': return 'pending'
    case 'terminating': return 'building'
    case 'succeeded': return 'idle'
    default: return 'unknown'
  }
}

// EditResourceDialog lets the user change a local resource's settings (image,
// container port, host port/network for Docker; expose-via-ingress for K8s)
// after creation — reuses new-resource.tsx's LocalFields for the same fields
// shown at creation time, pre-filled from the resource's current outputs.
// Docker edits recreate the container (brief restart); K8s edits patch the
// Deployment/Service/Ingress in place (see internal/api's handleEditResource).
function EditResourceDialog({
  resourceId, resourceType, outputs, open, onClose,
}: {
  resourceId: string
  resourceType: LocalResource
  outputs: Record<string, string>
  open: boolean
  onClose: () => void
}) {
  const [opts, setOpts] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setError('')
    setOpts({
      image: outputs.image ?? '',
      container_port: outputs.container_port ?? outputs.port ?? '',
      host_port: outputs.host_port ?? '',
      network: outputs.network && outputs.network !== 'autodevops' ? outputs.network : '',
      expose_http: outputs.expose_http ?? 'false',
    })
  }, [open, outputs])

  function set(k: string, val: string) {
    setOpts((prev) => ({ ...prev, [k]: val }))
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true); setError('')
    try {
      await apiFetch(`/resources/${resourceId}/edit`, {
        method: 'POST',
        body: JSON.stringify({ resource_opts: opts }),
      })
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update resource')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title="Edit resource">
      <form onSubmit={handleSubmit} className="p-4 space-y-4">
        <LocalFields v={opts} set={set} resourceType={resourceType} />
        {resourceType === 'docker' && (
          <p className="text-xs text-muted-foreground">
            Saving recreates the container with the new settings — it will be briefly unavailable.
          </p>
        )}
        {error && (
          <div className="flex items-start gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertTriangle className="size-4 shrink-0 mt-0.5" />
            <span>{error}</span>
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="outline" onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button type="submit" size="sm" className="gap-1.5" disabled={loading}>
            {loading && <Loader2 className="size-3.5 animate-spin" />}
            {loading ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export function ResourceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { token } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  // Local resources are listed (and linked back to) under /local-resources, so
  // returning there keeps the sidebar's "Local Resources" tab highlighted
  // instead of falling back to /resources ("Cloud Resources").
  const basePath = location.pathname.startsWith('/local-resources') ? '/local-resources' : '/resources'
  const [resource, setResource] = useState<ProvisionedResource | null>(null)
  const [runs, setRuns] = useState<TerraformRun[]>([])
  const [acting, setActing] = useState<'apply' | 'destroy' | null>(null)
  const [containerHealth, setContainerHealth] = useState<{ status: string; reason?: string } | null>(null)
  const [podHealths, setPodHealths] = useState<Record<string, { status: string; reason?: string; restart_count: number }>>({})

  const [consoleOpen, setConsoleOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)

  const [tfOpen, setTfOpen] = useState(false)
  const [tfLoading, setTfLoading] = useState(false)
  const [tfError, setTfError] = useState('')
  const [tfConfig, setTfConfig] = useState('')
  const [tfDraft, setTfDraft] = useState('')
  const [tfEditing, setTfEditing] = useState(false)
  const [tfPlanning, setTfPlanning] = useState(false)

  const { isLoading, error } = useQuery({
    queryKey: ['resource', id],
    queryFn: async () => {
      const data = await apiFetch<ProvisionedResource>(`/resources/${id}`)
      setResource(data)
      return data
    },
    enabled: !!id,
  })

  useQuery({
    queryKey: ['terraform-runs'],
    queryFn: async () => {
      const data = await apiFetch<TerraformRun[]>('/terraform/runs')
      setRuns(data)
      return data
    },
    enabled: !!id,
    refetchInterval: 30_000,
  })

  // outputs is computed here (rather than after the loading/error guards
  // below) so the SSE callback's pod.health filter can read
  // outputs.namespace from the latest resource state.
  const outputs = resource ? parseOutputs(resource.outputs) : {}

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type === 'resource') {
      const r = data as ProvisionedResource
      if (r.id === id) setResource(r)
    } else if (type === 'terraform.run') {
      const run = data as TerraformRun
      if (run.resource_id === id) {
        setRuns((prev) => {
          const idx = prev.findIndex((x) => x.id === run.id)
          if (idx >= 0) {
            const next = [...prev]
            next[idx] = run
            return next
          }
          return [run, ...prev]
        })
      }
    } else if (type === 'container.health') {
      const ch = data as { resource_id?: string; kind: string; status: string; reason?: string }
      if (ch.kind === 'resource' && ch.resource_id === id) {
        setContainerHealth({ status: ch.status, reason: ch.reason })
      }
    } else if (type === 'pod.health') {
      const ph = data as { namespace: string; pod_name: string; status: string; reason?: string; restart_count: number }
      if (outputs.namespace && ph.namespace === outputs.namespace) {
        setPodHealths((prev) => ({ ...prev, [ph.pod_name]: ph }))
      }
    }
  }, [id, outputs.namespace]))

  async function handleApply() {
    if (!id) return
    setActing('apply')
    try {
      await apiFetch(`/resources/${id}/apply`, { method: 'POST' })
    } catch { /* status surfaces via "resource" SSE event */ }
    finally { setActing(null) }
  }

  async function handleDestroy() {
    if (!id) return
    setActing('destroy')
    try {
      await apiFetch(`/resources/${id}/destroy`, { method: 'POST' })
    } catch { /* status surfaces via "resource" SSE event */ }
    finally { setActing(null) }
  }

  async function openTerraform() {
    if (!id) return
    setTfOpen(true)
    setTfEditing(false)
    setTfError('')
    setTfLoading(true)
    try {
      const data = await apiFetch<{ config: string }>(`/resources/${id}/terraform`)
      setTfConfig(data.config)
      setTfDraft(data.config)
    } catch (err) {
      setTfError(err instanceof Error ? err.message : 'Failed to load Terraform config')
    } finally {
      setTfLoading(false)
    }
  }

  function handleDownloadTerraform() {
    const blob = new Blob([tfConfig], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${resource?.name ?? 'resource'}.tf`
    a.click()
    URL.revokeObjectURL(url)
  }

  async function handlePlanChanges() {
    if (!id) return
    setTfPlanning(true)
    setTfError('')
    try {
      await apiFetch(`/resources/${id}/terraform/plan`, {
        method: 'POST',
        body: JSON.stringify({ config: tfDraft }),
      })
      setTfOpen(false)
      setTfEditing(false)
    } catch (err) {
      setTfError(err instanceof Error ? err.message : 'Failed to plan changes')
    } finally {
      setTfPlanning(false)
    }
  }

  async function handleDismiss() {
    if (!id) return
    try {
      await apiFetch(`/resources/${id}/dismiss`, { method: 'POST' })
      navigate(basePath)
    } catch { /* leave on the page if dismiss failed */ }
  }

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  }
  if (error || !resource) {
    return (
      <div className="p-6 space-y-3">
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => navigate(basePath)}>
          <ArrowLeft className="size-3.5" />
          Back to Resources
        </Button>
        <p className="text-sm text-muted-foreground">Resource not found.</p>
      </div>
    )
  }

  const meta = provisionedStatusMeta[resource.status] ?? { kind: 'unknown' as StatusKind, label: resource.status }
  const isTransitional = resource.status === 'pending' || resource.status === 'applying' || resource.status === 'destroying' || resource.status === 'planning'
  const latestRun = [...runs].filter((r) => r.resource_id === id).sort((a, b) => b.created_at.localeCompare(a.created_at))[0]
  const typeLabel = provisionedTypeLabel[resource.provider]?.[resource.resource_type] ?? resource.resource_type
  const hasOutputs = Object.keys(outputs).length > 0
  // Mirrors internal/console/registry.go's Connector dispatch: aws:vm, azure:vm, aws:ecs, azure:aci, local:docker, local:k8s.
  const consoleSupported =
    resource.resource_type === 'vm' ||
    (resource.provider === 'aws' && resource.resource_type === 'ecs') ||
    (resource.provider === 'azure' && resource.resource_type === 'aci') ||
    (resource.provider === 'local' && (resource.resource_type === 'docker' || resource.resource_type === 'k8s'))
  // Mirrors handleEditResource's provider/resource_type + status checks (applied or failed only).
  const editSupported = resource.provider === 'local' && (resource.resource_type === 'docker' || resource.resource_type === 'k8s')
  const canEdit = editSupported && (resource.status === 'applied' || resource.status === 'failed')

  return (
    <div>
      <PageHeader title={resource.name} description={`${typeLabel} · ${resource.region}`}>
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => navigate(basePath)}>
          <ArrowLeft className="size-3.5" />
          Back
        </Button>
        {!isTransitional && (
          <>
            {resource.status === 'applied' && consoleSupported && (
              <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setConsoleOpen(true)}>
                <SquareTerminal className="size-3.5" />
                Connect
              </Button>
            )}
            {canEdit && (
              <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setEditOpen(true)}>
                <Pencil className="size-3.5" />
                Edit
              </Button>
            )}
            {resource.provider !== 'local' && (
              <Button size="sm" variant="outline" className="gap-1.5" onClick={openTerraform}>
                <FileCode className="size-3.5" />
                View Terraform
              </Button>
            )}
            {(resource.status === 'planned') && (
              <Button size="sm" className="gap-1.5" disabled={!!acting} onClick={handleApply}>
                {acting === 'apply' ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                Apply
              </Button>
            )}
            {(resource.status === 'applied' || resource.status === 'planned' || resource.status === 'failed') && (
              <Button size="sm" variant="destructive" className="gap-1.5" disabled={!!acting} onClick={handleDestroy}>
                {acting === 'destroy' ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                Destroy
              </Button>
            )}
            {resource.status === 'destroyed' && (
              <Button size="sm" variant="ghost" className="gap-1.5 text-muted-foreground" onClick={handleDismiss}>
                <X className="size-3.5" />
                Dismiss
              </Button>
            )}
          </>
        )}
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-4 max-w-3xl">
        <Card>
          <CardHeader>
            <CardTitle>Overview</CardTitle>
            <StatusBadge status={meta.kind} label={meta.label} pulse={meta.pulse} />
          </CardHeader>
          <CardContent className="space-y-1">
            <InfoRow label="Provider" value={<ProviderTag provider={resource.provider as Provider} />} />
            <InfoRow label="Type" value={typeLabel} />
            <InfoRow label="Region" value={<span className="font-mono text-xs">{resource.region}</span>} />
            <InfoRow label="Created" value={new Date(resource.created_at).toLocaleString()} />
            <InfoRow label="Updated" value={formatRelativeTime(resource.updated_at)} />
          </CardContent>
        </Card>

        {!hasOutputs && (resource.status === 'applied' || resource.status === 'applying') && (
          <Card>
            <CardContent className="py-4 text-sm text-muted-foreground">
              No connection details yet — they appear here once Apply finishes successfully.
            </CardContent>
          </Card>
        )}

        {hasOutputs && (outputs.vpc_id || outputs.resource_group_name) && (
          <Card>
            <CardHeader>
              <CardTitle>Network</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.vpc_id && <CopyField label="VPC ID" value={outputs.vpc_id} />}
              {outputs.resource_group_name && <CopyField label="Resource Group" value={outputs.resource_group_name} />}
            </CardContent>
          </Card>
        )}

        {/* Local Docker container */}
        {hasOutputs && resource.provider === 'local' && resource.resource_type === 'docker' && (
          <Card>
            <CardHeader>
              <CardTitle>Container</CardTitle>
              {containerHealth && (
                <StatusBadge status={dockerHealthKind(containerHealth.status)} label={containerHealth.status} />
              )}
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.image && <CopyField label="Image" value={outputs.image} />}
              {outputs.container_id && <CopyField label="Container ID" value={outputs.container_id} />}
              {outputs.host_port && <CopyField label="Host Port" value={outputs.host_port} />}
              {outputs.network && <CopyField label="Docker network" value={outputs.network} />}
              {outputs.host_port && (
                <CopyField
                  label="URL"
                  value={`http://localhost:${outputs.host_port}`}
                  href={`http://localhost:${outputs.host_port}`}
                />
              )}
              {outputs.host_port && (
                <CopyField label="Test command" value={`curl http://localhost:${outputs.host_port}`} />
              )}
              {containerHealth?.reason && (
                <p className="text-xs text-muted-foreground">{containerHealth.reason}</p>
              )}
            </CardContent>
          </Card>
        )}

        {/* Local K8s workload */}
        {hasOutputs && resource.provider === 'local' && resource.resource_type === 'k8s' && (
          <Card>
            <CardHeader>
              <CardTitle>Workload</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.image && <CopyField label="Image" value={outputs.image} />}
              {outputs.namespace && <CopyField label="Namespace" value={outputs.namespace} />}
              {outputs.service_ip && <CopyField label="Service ClusterIP" value={outputs.service_ip} />}
              {outputs.ingress_url && (
                <CopyField label="Ingress URL" value={outputs.ingress_url} href={outputs.ingress_url} />
              )}
              {Object.keys(podHealths).length > 0 && (
                <div className="space-y-1.5 pt-1">
                  <p className="text-[0.7rem] uppercase tracking-wide text-muted-foreground">Pods</p>
                  {Object.entries(podHealths).map(([podName, ph]) => (
                    <div key={podName} className="flex items-center justify-between gap-2 rounded-lg border border-border bg-background px-3 py-2">
                      <span className="truncate font-mono text-xs">{podName}</span>
                      <StatusBadge status={podHealthKind(ph.status)} label={ph.status} />
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        )}

        {/* AWS EC2 */}
        {hasOutputs && resource.provider === 'aws' && resource.resource_type === 'vm' && (
          <Card>
            <CardHeader>
              <CardTitle>Instance</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.ec2_instance_id && <CopyField label="Instance ID" value={outputs.ec2_instance_id} />}
              {outputs.ec2_public_ip && <CopyField label="Public IP" value={outputs.ec2_public_ip} />}
              {outputs.ec2_private_ip && <CopyField label="Private IP" value={outputs.ec2_private_ip} />}
              <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-sm text-warning">
                <AlertTriangle className="size-4 shrink-0 mt-0.5" />
                <span>SSH access requires an EC2 key pair, which isn&apos;t configured for this instance yet.</span>
              </div>
            </CardContent>
          </Card>
        )}

        {/* AWS RDS */}
        {hasOutputs && resource.provider === 'aws' && resource.resource_type === 'rds' && (
          <Card>
            <CardHeader>
              <CardTitle>Database</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.rds_endpoint && <CopyField label="Endpoint" value={outputs.rds_endpoint} />}
              {outputs.rds_port && <CopyField label="Port" value={outputs.rds_port} />}
              {outputs.rds_username && <CopyField label="Master Username" value={outputs.rds_username} />}
              {outputs.rds_endpoint && outputs.rds_port && outputs.rds_username && (
                <CopyField
                  label="Connection command"
                  value={`psql "host=${outputs.rds_endpoint} port=${outputs.rds_port} user=${outputs.rds_username} dbname=postgres sslmode=require"`}
                />
              )}
              <p className="text-xs text-muted-foreground">
                The master password is managed by AWS Secrets Manager (auto-rotated, never stored by AutoDevOps) — find it in the
                AWS console under Secrets Manager, secret name starting with <code className="font-mono">rds!db-</code>.
              </p>
            </CardContent>
          </Card>
        )}

        {/* AWS ECS */}
        {hasOutputs && resource.provider === 'aws' && resource.resource_type === 'ecs' && (
          <Card>
            <CardHeader>
              <CardTitle>Service</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.ecs_cluster_name && <CopyField label="Cluster" value={outputs.ecs_cluster_name} />}
              {outputs.ecs_alb_dns_name && (
                <CopyField label="Load Balancer URL" value={`http://${outputs.ecs_alb_dns_name}`} href={`http://${outputs.ecs_alb_dns_name}`} />
              )}
            </CardContent>
          </Card>
        )}

        {/* Azure VM */}
        {hasOutputs && resource.provider === 'azure' && resource.resource_type === 'vm' && (
          <Card>
            <CardHeader>
              <CardTitle>Virtual Machine</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.vm_public_ip && <CopyField label="Public IP" value={outputs.vm_public_ip} />}
              {outputs.vm_private_ip && <CopyField label="Private IP" value={outputs.vm_private_ip} />}
              {outputs.vm_admin_username && <CopyField label="Admin Username" value={outputs.vm_admin_username} />}
              {outputs.vm_public_ip && outputs.vm_admin_username && (
                <CopyField label="SSH command" value={`ssh ${outputs.vm_admin_username}@${outputs.vm_public_ip}`} />
              )}
              <p className="text-xs text-muted-foreground">
                Connect using the private key downloaded when this VM was created — AutoDevOps never stores it.
              </p>
            </CardContent>
          </Card>
        )}

        {/* Azure DB */}
        {hasOutputs && resource.provider === 'azure' && resource.resource_type === 'db' && (
          <Card>
            <CardHeader>
              <CardTitle>Database</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.db_fqdn && <CopyField label="Host (FQDN)" value={outputs.db_fqdn} />}
              {outputs.db_admin_username && <CopyField label="AAD Admin" value={outputs.db_admin_username} />}
              {outputs.db_fqdn && outputs.db_admin_username && (
                <CopyField
                  label="Connection command"
                  value={`psql "host=${outputs.db_fqdn} port=5432 dbname=postgres user=${outputs.db_admin_username} sslmode=require"`}
                />
              )}
              <p className="text-xs text-muted-foreground">
                Authentication is Azure AD only — sign in as the AAD admin above using an access token, no password is stored.
              </p>
            </CardContent>
          </Card>
        )}

        {/* Azure ACI */}
        {hasOutputs && resource.provider === 'azure' && resource.resource_type === 'aci' && (
          <Card>
            <CardHeader>
              <CardTitle>Container Instance</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {outputs.aci_ip_address && <CopyField label="IP Address" value={outputs.aci_ip_address} />}
              {outputs.aci_fqdn && (
                <CopyField label="URL" value={`http://${outputs.aci_fqdn}`} href={`http://${outputs.aci_fqdn}`} />
              )}
            </CardContent>
          </Card>
        )}

        <Card>
          <CardHeader>
            <CardTitle>Latest Run</CardTitle>
          </CardHeader>
          <CardContent>
            {latestRun ? (
              <div className="space-y-2">
                <p className="text-xs text-muted-foreground">
                  <span className="font-mono">{latestRun.action}</span> ·{' '}
                  <span className="font-mono">{latestRun.status}</span> ·{' '}
                  {new Date(latestRun.created_at).toLocaleString()}
                </p>
                {latestRun.status === 'failed' && friendlyTerraformError(latestRun.plan_output) && (
                  <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-sm text-warning">
                    <AlertTriangle className="size-4 shrink-0 mt-0.5" />
                    <span>{friendlyTerraformError(latestRun.plan_output)}</span>
                  </div>
                )}
                <pre className="max-h-80 overflow-auto rounded-lg border border-border bg-card p-3 text-xs whitespace-pre-wrap">
                  {latestRun.plan_output || 'No output recorded.'}
                </pre>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">No terraform runs yet for this resource.</p>
            )}
          </CardContent>
        </Card>
      </div>

      <ResourceConsole
        resourceId={id!}
        resourceName={resource.name}
        open={consoleOpen}
        onClose={() => setConsoleOpen(false)}
      />

      {editSupported && (
        <EditResourceDialog
          resourceId={id!}
          resourceType={resource.resource_type as LocalResource}
          outputs={outputs}
          open={editOpen}
          onClose={() => setEditOpen(false)}
        />
      )}

      <Dialog open={tfOpen} onClose={() => setTfOpen(false)} title="Terraform configuration" className="max-w-3xl">
        <div className="p-4 space-y-3">
          {tfLoading ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : tfError ? (
            <div className="flex items-start gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertTriangle className="size-4 shrink-0 mt-0.5" />
              <span>{tfError}</span>
            </div>
          ) : tfEditing ? (
            <textarea
              value={tfDraft}
              onChange={(e) => setTfDraft(e.target.value)}
              spellCheck={false}
              className="h-[28rem] w-full resize-none rounded-lg border border-border bg-background p-3 font-mono text-xs leading-relaxed focus:outline-none focus:ring-2 focus:ring-primary/50"
            />
          ) : (
            <pre className="max-h-[28rem] overflow-auto rounded-lg border border-border bg-card p-3 text-xs font-mono leading-relaxed whitespace-pre">
              {tfConfig}
            </pre>
          )}

          {tfEditing && (
            <p className="text-xs text-muted-foreground">
              "Plan changes" runs <span className="font-mono">terraform plan</span> against this edited config. Review the new plan output in Latest Run, then Apply to bring the infrastructure in line with it.
            </p>
          )}

          <div className="flex justify-end gap-2">
            <Button size="sm" variant="outline" className="gap-1.5" onClick={handleDownloadTerraform} disabled={tfLoading || !!tfError}>
              <Download className="size-3.5" />
              Download
            </Button>
            {!tfEditing ? (
              <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setTfEditing(true)} disabled={tfLoading || !!tfError}>
                <Pencil className="size-3.5" />
                Edit
              </Button>
            ) : (
              <>
                <Button size="sm" variant="outline" onClick={() => { setTfDraft(tfConfig); setTfEditing(false) }} disabled={tfPlanning}>
                  Cancel
                </Button>
                <Button size="sm" className="gap-1.5" onClick={handlePlanChanges} disabled={tfPlanning || tfDraft === tfConfig}>
                  {tfPlanning && <Loader2 className="size-3.5 animate-spin" />}
                  Plan changes
                </Button>
              </>
            )}
          </div>
        </div>
      </Dialog>
    </div>
  )
}
