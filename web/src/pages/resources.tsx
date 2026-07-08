import { useState, useCallback } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Cloud, RefreshCw, Server, Database, Plus, X } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ProviderTag } from '@/components/meta-tags'
import { ProvisionedTable } from '@/components/provisioned-table'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { useProvisionedResources } from '@/lib/use-provisioned-resources'
import { cn } from '@/lib/utils'
import type { Provider } from '@/lib/types'
import { formatRelativeTime } from '@/lib/resources'

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
    return s.state ?? s.status ?? s.power_state ?? s.provisioning_state ?? '-'
  } catch { return '-' }
}

// isDismissable mirrors stateColor's classification, only resources that are
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

  const {
    provisioned, runs, expanded, setExpanded, actingOn, provisionedLoading,
    handleApply, handleDestroy, handleDismiss,
  } = useProvisionedResources()

  const { isLoading, refetch, isFetching } = useQuery({
    queryKey: ['cloud-resources'],
    queryFn: async () => {
      const data = await apiFetch<CloudResource[]>('/cloud/resources')
      setResources(data)
      return data
    },
    refetchInterval: 60_000,
  })

  useSSE(token, useCallback((type: string) => {
    if (type === 'cloud.resource') {
      // Poller payloads don't carry the DB row id, account name, or
      // dedupe info that the table needs, just refetch the inventory.
      qc.invalidateQueries({ queryKey: ['cloud-resources'] })
    }
  }, [qc]))

  async function handleDismissCloudResource(id: string) {
    try {
      await apiFetch(`/cloud/resources/${id}/dismiss`, { method: 'POST' })
      setResources((prev) => prev.filter((r) => r.id !== id))
    } catch { /* leave it listed if the dismiss failed */ }
  }

  const filtered = filter === 'all' ? resources : resources.filter((r) => r.provider === filter)
  const cloudProvisioned = provisioned.filter((r) => r.provider !== 'local')

  return (
    <div>
      <PageHeader
        title="Cloud Resources"
        description="Provision standalone cloud infrastructure and view live AWS + Azure inventory."
      >
        <Button size="sm" className="gap-1.5" onClick={() => navigate('/new-resource')}>
          <Plus className="size-4" />
          New Resource
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-6">
        {/* ── Provisioned Cloud Resources ── */}
        <div className="space-y-3">
          <h2 className="text-sm font-semibold">Cloud Resources</h2>
          <ProvisionedTable
            items={cloudProvisioned}
            loading={provisionedLoading}
            emptyMessage="No provisioned cloud resources yet. Click New Resource to plan and apply standalone AWS/Azure infrastructure."
            runs={runs}
            expanded={expanded}
            setExpanded={setExpanded}
            actingOn={actingOn}
            onApply={handleApply}
            onDestroy={handleDestroy}
            onDismiss={handleDismiss}
            navigate={navigate}
          />
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
                            <p className="truncate font-medium">{r.name || '-'}</p>
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
