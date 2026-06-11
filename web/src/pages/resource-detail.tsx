import { useState, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Play, Trash2, X, Loader2, Copy, Check, AlertTriangle, FileCode, Download, Pencil } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { ProviderTag } from '@/components/meta-tags'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import type { Provider } from '@/lib/mock-data'
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

export function ResourceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { token } = useAuth()
  const navigate = useNavigate()
  const [resource, setResource] = useState<ProvisionedResource | null>(null)
  const [runs, setRuns] = useState<TerraformRun[]>([])
  const [acting, setActing] = useState<'apply' | 'destroy' | null>(null)

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
    }
  }, [id]))

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
      navigate('/resources')
    } catch { /* leave on the page if dismiss failed */ }
  }

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">Loading…</div>
  }
  if (error || !resource) {
    return (
      <div className="p-6 space-y-3">
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => navigate('/resources')}>
          <ArrowLeft className="size-3.5" />
          Back to Resources
        </Button>
        <p className="text-sm text-muted-foreground">Resource not found.</p>
      </div>
    )
  }

  const outputs = parseOutputs(resource.outputs)
  const meta = provisionedStatusMeta[resource.status] ?? { kind: 'unknown' as StatusKind, label: resource.status }
  const isTransitional = resource.status === 'pending' || resource.status === 'applying' || resource.status === 'destroying' || resource.status === 'planning'
  const latestRun = [...runs].filter((r) => r.resource_id === id).sort((a, b) => b.created_at.localeCompare(a.created_at))[0]
  const typeLabel = provisionedTypeLabel[resource.provider]?.[resource.resource_type] ?? resource.resource_type
  const hasOutputs = Object.keys(outputs).length > 0

  return (
    <div>
      <PageHeader title={resource.name} description={`${typeLabel} · ${resource.region}`}>
        <Button size="sm" variant="outline" className="gap-1.5" onClick={() => navigate('/resources')}>
          <ArrowLeft className="size-3.5" />
          Back
        </Button>
        {!isTransitional && (
          <>
            <Button size="sm" variant="outline" className="gap-1.5" onClick={openTerraform}>
              <FileCode className="size-3.5" />
              View Terraform
            </Button>
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

        {hasOutputs && (
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
