import { useState, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Cloud, RefreshCw, Server, Database } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ProviderTag } from '@/components/meta-tags'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import type { Provider } from '@/lib/mock-data'

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
  azure_vm: Server,
  rds_instance: Database,
  aci_container_group: Cloud,
}

const typeLabel: Record<string, string> = {
  ec2_instance: 'EC2 Instance',
  azure_vm: 'Azure VM',
  rds_instance: 'RDS Instance',
  aci_container_group: 'Container Group',
}

function stateColor(state: string): string {
  try {
    const s = JSON.parse(state) as Record<string, string>
    const raw = (s.state ?? s.status ?? s.power_state ?? '').toLowerCase()
    if (['running', 'available', 'started'].some((v) => raw.includes(v))) return 'bg-success'
    if (['stopped', 'deallocated'].some((v) => raw.includes(v))) return 'bg-muted-foreground/50'
    if (['pending', 'starting'].some((v) => raw.includes(v))) return 'bg-info animate-pulse'
    if (['terminated', 'failed'].some((v) => raw.includes(v))) return 'bg-critical'
  } catch { /* ignore */ }
  return 'bg-muted-foreground/50'
}

function stateLabel(state: string): string {
  try {
    const s = JSON.parse(state) as Record<string, string>
    return s.state ?? s.status ?? s.power_state ?? '—'
  } catch { return '—' }
}

const providers = ['all', 'aws', 'azure'] as const

export function ResourcesPage() {
  const { token } = useAuth()
  const [filter, setFilter] = useState<'all' | 'aws' | 'azure'>('all')
  const [resources, setResources] = useState<CloudResource[]>([])

  const { isLoading, refetch, isFetching } = useQuery({
    queryKey: ['cloud-resources'],
    queryFn: async () => {
      const data = await apiFetch<CloudResource[]>('/cloud/resources')
      setResources(data)
      return data
    },
    refetchInterval: 60_000,
  })

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type !== 'cloud.resource') return
    const r = data as CloudResource
    setResources((prev) => {
      const idx = prev.findIndex((x) => x.id === r.id)
      if (idx >= 0) {
        const next = [...prev]
        next[idx] = r
        return next
      }
      return [r, ...prev]
    })
  }, []))

  const filtered = filter === 'all' ? resources : resources.filter((r) => r.provider === filter)

  return (
    <div>
      <PageHeader
        title="Cloud Resources"
        description="Live AWS + Azure inventory polled via cloud SDKs every 60 seconds."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-4">
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
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border">
                  {['Provider', 'Account', 'Type', 'Name / ID', 'Region', 'State', 'Polled'].map((h) => (
                    <th key={h} className="px-4 py-3 text-left text-[0.7rem] font-medium uppercase tracking-wide text-muted-foreground">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {filtered.map((r) => {
                  const Icon = resourceIcon[r.resource_type] ?? Cloud
                  const diff = Math.floor((Date.now() - new Date(r.polled_at).getTime()) / 60000)
                  return (
                    <tr key={r.id} className="hover:bg-accent/30 transition-colors">
                      <td className="px-4 py-3">
                        <ProviderTag provider={r.provider as Provider} />
                      </td>
                      <td className="px-4 py-3 text-xs text-muted-foreground">{r.account_name}</td>
                      <td className="px-4 py-3">
                        <div className="flex items-center gap-1.5">
                          <Icon className="size-3.5 text-muted-foreground" />
                          <span className="text-xs">{typeLabel[r.resource_type] ?? r.resource_type}</span>
                        </div>
                      </td>
                      <td className="px-4 py-3">
                        <p className="font-medium">{r.name || '—'}</p>
                        <p className="font-mono text-xs text-muted-foreground">{r.resource_id}</p>
                      </td>
                      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{r.region}</td>
                      <td className="px-4 py-3">
                        <div className="flex items-center gap-2">
                          <span className={cn('size-2 rounded-full', stateColor(r.state))} />
                          <span className="text-xs capitalize">{stateLabel(r.state)}</span>
                        </div>
                      </td>
                      <td className="px-4 py-3 text-xs text-muted-foreground">
                        {diff === 0 ? 'just now' : `${diff}m ago`}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </Card>
      </div>
    </div>
  )
}
