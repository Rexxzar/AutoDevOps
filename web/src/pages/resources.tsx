import { Fragment, useState, useCallback } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Cloud, RefreshCw, Server, Database, Plus, Play, Trash2, ChevronDown, ChevronRight, Loader2, X, AlertTriangle } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ProviderTag } from '@/components/meta-tags'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import type { Provider } from '@/lib/mock-data'
import {
  type ProvisionedResource,
  type TerraformRun,
  provisionedTypeLabel,
  provisionedStatusMeta,
  formatRelativeTime,
  friendlyTerraformError,
} from '@/lib/resources'

interface CloudResource {
  id: string
  provider: string
  account_name: string
  resource_type: string
  resource_id: string
  region: string
  name: string
  state: string
  polled_at: string
}

const resourceIcon: Record<string, typeof Server> = {
  ec2_instance: Server,
  rds_instance: Database,
  ecs_service: Cloud,
  virtual_machine: Server,
  container_instance: Cloud,
  postgresql_flexible_server: Database,
}

const typeLabel: Record<string, string> = {
  ec2_instance: 'EC2 Instance',
  rds_instance: 'RDS Database',
  ecs_service: 'ECS Service',
  virtual_machine: 'Virtual Machine',
  container_instance: 'Container Instance',
  postgresql_flexible_server: 'PostgreSQL Database',
}

function stateColor(state: string): string {
  try {
    const s = JSON.parse(state) as Record<string, string>
    const raw = (s.state ?? s.status ?? s.power_state ?? s.provisioning_state ?? '').toLowerCase()
    if (['running', 'available', 'started', 'ready', 'active', 'succeeded'].some((v) => raw.includes(v))) return 'bg-success'
    if (['stopped', 'deallocated'].some((v) => raw.includes(v))) return 'bg-muted-foreground/50'
    if (['pending', 'starting', 'creating', 'updating', 'deleting'].some((v) => raw.includes(v))) return 'bg-info animate-pulse'
    if (['terminated', 'failed'].some((v) => raw.includes(v))) return 'bg-critical'
  } catch { /* ignore */ }
  return 'bg-muted-foreground/50'
}

function stateLabel(state: string): string {
  try {
    const s = JSON.parse(state) as Record<string, string>
    return s.state ?? s.status ?? s.power_state ?? s.provisioning_state ?? '—'
  } catch { return '—' }
}

// isDismissable mirrors stateColor's classification — only resources that are
// no longer actively running (terminated/failed or stopped/deallocated) can
// be dismissed from the Cloud Inventory.
function isDismissable(state: string): boolean {
  const c = stateColor(state)
  return c === 'bg-critical' || c === 'bg-muted-foreground/50'
}

const providers = ['all', 'aws', 'azure'] as const

export function ResourcesPage() {
  const { token } = useAuth()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [filter, setFilter] = useState<'all' | 'aws' | 'azure'>('all')
  const [resources, setResources] = useState<CloudResource[]>([])
  const [provisioned, setProvisioned] = useState<ProvisionedResource[]>([])
  const [runs, setRuns] = useState<TerraformRun[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)
  const [actingOn, setActingOn] = useState<Record<string, 'apply' | 'destroy'>>({})

  const { isLoading, refetch, isFetching } = useQuery({
    queryKey: ['cloud-resources'],
    queryFn: async () => {
      const data = await apiFetch<CloudResource[]>('/cloud/resources')
      setResources(data)
      return data
    },
    refetchInterval: 60_000,
  })

  const { isLoading: provisionedLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: async () => {
      const data = await apiFetch<ProvisionedResource[]>('/resources')
      setProvisioned(data)
      return data
    },
    refetchInterval: 30_000,
  })

  useQuery({
    queryKey: ['terraform-runs'],
    queryFn: async () => {
      const data = await apiFetch<TerraformRun[]>('/terraform/runs')
      setRuns(data)
      return data
    },
    refetchInterval: 30_000,
  })

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type === 'cloud.resource') {
      // Poller payloads don't carry the DB row id, account name, or
      // dedupe info that the table needs — just refetch the inventory.
      qc.invalidateQueries({ queryKey: ['cloud-resources'] })
    } else if (type === 'resource') {
      const r = data as ProvisionedResource
      setProvisioned((prev) => {
        const idx = prev.findIndex((x) => x.id === r.id)
        if (idx >= 0) {
          const next = [...prev]
          next[idx] = r
          return next
        }
        return [r, ...prev]
      })
    } else if (type === 'terraform.run') {
      const run = data as TerraformRun
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
  }, [qc]))

  async function handleApply(id: string) {
    setActingOn((prev) => ({ ...prev, [id]: 'apply' }))
    try {
      await apiFetch(`/resources/${id}/apply`, { method: 'POST' })
    } catch { /* status surfaces via "resource" SSE event */ }
    finally {
      setActingOn((prev) => { const next = { ...prev }; delete next[id]; return next })
    }
  }

  async function handleDestroy(id: string) {
    setActingOn((prev) => ({ ...prev, [id]: 'destroy' }))
    try {
      await apiFetch(`/resources/${id}/destroy`, { method: 'POST' })
    } catch { /* status surfaces via "resource" SSE event */ }
    finally {
      setActingOn((prev) => { const next = { ...prev }; delete next[id]; return next })
    }
  }

  async function handleDismissResource(id: string) {
    try {
      await apiFetch(`/resources/${id}/dismiss`, { method: 'POST' })
      setProvisioned((prev) => prev.filter((p) => p.id !== id))
    } catch { /* leave it listed if the dismiss failed */ }
  }

  async function handleDismissCloudResource(id: string) {
    try {
      await apiFetch(`/cloud/resources/${id}/dismiss`, { method: 'POST' })
      setResources((prev) => prev.filter((r) => r.id !== id))
    } catch { /* leave it listed if the dismiss failed */ }
  }

  const filtered = filter === 'all' ? resources : resources.filter((r) => r.provider === filter)

  return (
    <div>
      <PageHeader
        title="Resources"
        description="Provision standalone cloud infrastructure and view live AWS + Azure inventory."
      >
        <Button size="sm" className="gap-1.5" onClick={() => navigate('/new-resource')}>
          <Plus className="size-4" />
          New Resource
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-6">
        {/* ── Provisioned Resources ── */}
        <div className="space-y-3">
          <h2 className="text-sm font-semibold">Provisioned Resources</h2>
          <Card className="overflow-hidden">
            {provisionedLoading && (
              <div className="py-12 text-center text-sm text-muted-foreground">Loading resources…</div>
            )}
            {!provisionedLoading && provisioned.length === 0 && (
              <div className="flex flex-col items-center gap-3 py-16 text-center">
                <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                  <Cloud className="size-4 text-muted-foreground" />
                </div>
                <p className="text-sm text-muted-foreground max-w-xs text-pretty">
                  No provisioned resources yet. Click New Resource to plan and apply standalone infrastructure.
                </p>
              </div>
            )}
            {provisioned.length > 0 && (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border">
                    {['', 'Provider', 'Name', 'Type', 'Region', 'Status', 'Updated', 'Actions'].map((h) => (
                      <th key={h} className="px-4 py-3 text-left text-[0.7rem] font-medium uppercase tracking-wide text-muted-foreground">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {provisioned.map((res) => {
                    const meta = provisionedStatusMeta[res.status] ?? { kind: 'unknown' as StatusKind, label: res.status }
                    const action = actingOn[res.id]
                    const isTransitional = res.status === 'pending' || res.status === 'applying' || res.status === 'destroying'
                    const latestRun = runs.find((r) => r.resource_id === res.id)
                    const isExpanded = expanded === res.id
                    return (
                      <Fragment key={res.id}>
                        <tr className="hover:bg-accent/30 transition-colors">
                          <td className="px-2 py-3 w-8">
                            <button
                              onClick={() => setExpanded(isExpanded ? null : res.id)}
                              className="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                              aria-label="Toggle plan output"
                            >
                              {isExpanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                            </button>
                          </td>
                          <td className="px-4 py-3">
                            <ProviderTag provider={res.provider as Provider} />
                          </td>
                          <td className="px-4 py-3 font-medium">
                            <button
                              onClick={() => navigate(`/resources/${res.id}`)}
                              className="hover:underline hover:text-primary transition-colors"
                            >
                              {res.name}
                            </button>
                          </td>
                          <td className="px-4 py-3 text-xs text-muted-foreground">
                            {provisionedTypeLabel[res.provider]?.[res.resource_type] ?? res.resource_type}
                          </td>
                          <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{res.region}</td>
                          <td className="px-4 py-3">
                            <StatusBadge status={meta.kind} label={meta.label} pulse={meta.pulse} />
                          </td>
                          <td className="px-4 py-3 text-xs text-muted-foreground" title={new Date(res.updated_at).toLocaleString()}>
                            {formatRelativeTime(res.updated_at)}
                          </td>
                          <td className="px-4 py-3">
                            {isTransitional ? (
                              <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                                <Loader2 className="size-3.5 animate-spin" />
                                {meta.label}…
                              </span>
                            ) : (
                              <div className="flex items-center gap-2">
                                {res.status === 'planned' && (
                                  <Button
                                    size="sm" variant="outline" className="gap-1.5 h-7 px-2 text-xs"
                                    disabled={!!action}
                                    onClick={() => handleApply(res.id)}
                                  >
                                    {action === 'apply' ? <Loader2 className="size-3 animate-spin" /> : <Play className="size-3" />}
                                    Apply
                                  </Button>
                                )}
                                {(res.status === 'applied' || res.status === 'planned' || res.status === 'failed') && (
                                  <Button
                                    size="sm" variant="destructive" className="gap-1.5 h-7 px-2 text-xs"
                                    disabled={!!action}
                                    onClick={() => handleDestroy(res.id)}
                                  >
                                    {action === 'destroy' ? <Loader2 className="size-3 animate-spin" /> : <Trash2 className="size-3" />}
                                    Destroy
                                  </Button>
                                )}
                                {res.status === 'destroyed' && (
                                  <Button
                                    size="sm" variant="ghost" className="gap-1.5 h-7 px-2 text-xs text-muted-foreground"
                                    onClick={() => handleDismissResource(res.id)}
                                  >
                                    <X className="size-3" />
                                    Dismiss
                                  </Button>
                                )}
                              </div>
                            )}
                          </td>
                        </tr>
                        {isExpanded && (
                          <tr>
                            <td colSpan={8} className="bg-background px-4 py-3">
                              {latestRun ? (
                                <div className="space-y-2">
                                  <p className="text-xs text-muted-foreground">
                                    Last run: <span className="font-mono">{latestRun.action}</span> ·{' '}
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
                                <p className="text-xs text-muted-foreground">No terraform runs yet for this resource.</p>
                              )}
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    )
                  })}
                </tbody>
              </table>
            )}
          </Card>
        </div>

        {/* ── Cloud Resources (live inventory) ── */}
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold">Cloud Inventory</h2>
            <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
              <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
              Refresh
            </Button>
          </div>

          {/* Filter */}
          <div className="flex items-center gap-2">
            {providers.map((p) => (
              <button
                key={p}
                onClick={() => setFilter(p)}
                className={cn(
                  'flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium capitalize transition-colors',
                  filter === p
                    ? 'border-ring/40 bg-accent text-foreground'
                    : 'border-border text-muted-foreground hover:bg-accent/60',
                )}
              >
                {p}
                <span className="rounded bg-muted px-1 font-mono text-[0.65rem]">
                  {p === 'all' ? resources.length : resources.filter((r) => r.provider === p).length}
                </span>
              </button>
            ))}
          </div>

          <Card className="overflow-hidden">
            {isLoading && (
              <div className="py-12 text-center text-sm text-muted-foreground">Loading resources…</div>
            )}
            {!isLoading && filtered.length === 0 && (
              <div className="flex flex-col items-center gap-3 py-16 text-center">
                <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                  <Cloud className="size-4 text-muted-foreground" />
                </div>
                <p className="text-sm text-muted-foreground max-w-xs text-pretty">
                  No cloud resources found. Register an AWS or Azure account in Settings to start polling.
                </p>
              </div>
            )}
            {filtered.length > 0 && (
              <div className="overflow-x-auto">
                <table className="w-full table-fixed text-sm">
                  <thead>
                    <tr className="border-b border-border">
                      {[
                        ['Provider', 'w-24'],
                        ['Account', 'w-28'],
                        ['Type', 'w-36'],
                        ['Name / ID', ''],
                        ['Region', 'w-32'],
                        ['State', 'w-28'],
                        ['Polled', 'w-20'],
                        ['', 'w-10'],
                      ].map(([h, width]) => (
                        <th key={h} className={cn('px-4 py-3 text-left text-[0.7rem] font-medium uppercase tracking-wide text-muted-foreground', width)}>
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {filtered.map((r) => {
                      const Icon = resourceIcon[r.resource_type] ?? Cloud
                      return (
                        <tr key={r.id} className="hover:bg-accent/30 transition-colors">
                          <td className="px-4 py-3">
                            <ProviderTag provider={r.provider as Provider} />
                          </td>
                          <td className="px-4 py-3 truncate text-xs text-muted-foreground">{r.account_name}</td>
                          <td className="px-4 py-3">
                            <div className="flex items-center gap-1.5">
                              <Icon className="size-3.5 shrink-0 text-muted-foreground" />
                              <span className="text-xs">{typeLabel[r.resource_type] ?? r.resource_type}</span>
                            </div>
                          </td>
                          <td className="px-4 py-3 overflow-hidden">
                            <p className="truncate font-medium">{r.name || '—'}</p>
                            <p className="truncate font-mono text-xs text-muted-foreground" title={r.resource_id}>{r.resource_id}</p>
                          </td>
                          <td className="px-4 py-3 truncate font-mono text-xs text-muted-foreground">{r.region}</td>
                          <td className="px-4 py-3">
                            <div className="flex items-center gap-2">
                              <span className={cn('size-2 shrink-0 rounded-full', stateColor(r.state))} />
                              <span className="truncate text-xs capitalize">{stateLabel(r.state)}</span>
                            </div>
                          </td>
                          <td className="px-4 py-3 text-xs text-muted-foreground" title={new Date(r.polled_at).toLocaleString()}>
                            {formatRelativeTime(r.polled_at)}
                          </td>
                          <td className="px-2 py-3 text-right">
                            {isDismissable(r.state) && (
                              <button
                                onClick={() => handleDismissCloudResource(r.id)}
                                className="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                                aria-label="Dismiss"
                                title="Dismiss from inventory"
                              >
                                <X className="size-3.5" />
                              </button>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}
