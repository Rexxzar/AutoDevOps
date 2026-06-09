import { useMemo, useState } from 'react'
import {
  RefreshCw,
  GitCommit,
  ShieldCheck,
  ShieldAlert,
  Loader2,
  Filter,
} from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/layout/page-header'
import { Button } from '@/components/ui/button'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { ProviderTag, targetLabel, frameworkLabel } from '@/components/meta-tags'
import { DeploymentDrawer } from '@/components/deployment-drawer'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import type { Deployment, Provider, Target } from '@/lib/mock-data'

interface ApiDeployment {
  id: string
  name: string
  repo_url: string
  branch: string
  framework: string
  target_type: string
  status: string
  image_ref: string
  created_at: string
  updated_at: string
}

function apiToDeployment(d: ApiDeployment): Deployment {
  const statusMap: Record<string, StatusKind> = {
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

  const diff = Date.now() - new Date(d.created_at).getTime()
  const s = Math.floor(diff / 1000)
  let createdAt = 'just now'
  if (s >= 60) createdAt = `${Math.floor(s / 60)}m ago`
  if (s >= 3600) createdAt = `${Math.floor(s / 3600)}h ago`
  if (s >= 86400) createdAt = `${Math.floor(s / 86400)}d ago`

  const commitSha = d.image_ref?.split(':').pop()?.slice(0, 7) ?? ''

  return {
    id: d.id,
    name: d.name,
    repo: d.repo_url,
    branch: d.branch,
    commit: commitSha,
    commitMessage: d.branch,
    author: '—',
    framework: d.framework || 'unknown',
    target: targetMap[d.target_type] ?? 'kubernetes',
    provider: 'kind' as Provider,
    status: statusMap[d.status] ?? 'unknown',
    createdAt,
    durationMs: 0,
    stages: [],
    complianceGate: d.status === 'failed' ? 'failed' : 'passed',
    findings: [],
    replicas: { ready: d.status === 'running' ? 1 : 0, desired: 1 },
  }
}

const filters: { label: string; value: 'all' | StatusKind }[] = [
  { label: 'All', value: 'all' },
  { label: 'Ready', value: 'ready' },
  { label: 'Building', value: 'building' },
  { label: 'Pending', value: 'pending' },
  { label: 'Failed', value: 'failed' },
]

export function DeploymentsPage() {
  const [active, setActive] = useState<'all' | StatusKind>('all')
  const [selected, setSelected] = useState<Deployment | null>(null)

  const { data: raw = [], isLoading, refetch, isFetching } = useQuery({
    queryKey: ['deployments'],
    queryFn: () => apiFetch<ApiDeployment[]>('/deployments'),
    refetchInterval: 8_000,
  })

  const deployments = useMemo(() => raw.map(apiToDeployment), [raw])

  const filtered = useMemo(
    () =>
      active === 'all'
        ? deployments
        : deployments.filter((d) => d.status === active),
    [active, deployments],
  )

  return (
    <div>
      <PageHeader
        title="Deployments"
        description="Git-to-deploy pipelines across Kubernetes, Docker and VM targets."
      >
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => refetch()}
          disabled={isFetching}
        >
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6">
        {/* Filter bar */}
        <div className="mb-4 flex items-center gap-2 overflow-x-auto">
          <Filter className="size-4 shrink-0 text-muted-foreground" />
          {filters.map((f) => {
            const count =
              f.value === 'all'
                ? deployments.length
                : deployments.filter((d) => d.status === f.value).length
            return (
              <button
                key={f.value}
                onClick={() => setActive(f.value)}
                className={cn(
                  'flex shrink-0 items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                  active === f.value
                    ? 'border-ring/40 bg-accent text-foreground'
                    : 'border-border text-muted-foreground hover:bg-accent/60',
                )}
              >
                {f.label}
                <span className="rounded bg-muted px-1 font-mono text-[0.65rem]">
                  {count}
                </span>
              </button>
            )
          })}
        </div>

        {/* List */}
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          {isLoading && (
            <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Loading deployments…
            </div>
          )}
          {!isLoading && filtered.length === 0 && (
            <div className="py-12 text-center text-sm text-muted-foreground">
              No deployments{active !== 'all' ? ` with status "${active}"` : ''}.
            </div>
          )}
          {filtered.map((d, i) => (
            <button
              key={d.id}
              onClick={() => setSelected(d)}
              className={cn(
                'flex w-full flex-col gap-3 px-4 py-4 text-left transition-colors hover:bg-accent/40 lg:flex-row lg:items-center lg:gap-4 lg:px-5',
                i !== filtered.length - 1 && 'border-b border-border',
              )}
            >
              {/* name + status */}
              <div className="flex min-w-0 items-center gap-3 lg:w-64">
                <StatusBadge status={d.status} pulse={d.status === 'building'} />
                <div className="min-w-0">
                  <p className="truncate text-sm font-semibold">{d.name}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {d.repo}
                  </p>
                </div>
              </div>

              {/* commit */}
              <div className="flex min-w-0 flex-1 items-center gap-2 text-xs text-muted-foreground">
                <GitCommit className="size-3.5 shrink-0" />
                {d.commit && (
                  <span className="font-mono text-foreground">{d.commit}</span>
                )}
                <span className="truncate">{d.branch}</span>
              </div>

              {/* meta */}
              <div className="flex shrink-0 items-center gap-3">
                <div className="hidden items-center gap-1.5 text-xs text-muted-foreground sm:flex">
                  {targetLabel[d.target] ?? d.target}
                  <ProviderTag provider={d.provider} />
                </div>
                <span className="hidden text-xs text-muted-foreground md:inline">
                  {frameworkLabel[d.framework] ?? d.framework}
                </span>
                <GateIcon gate={d.complianceGate} />
                <span className="w-16 text-right text-xs text-muted-foreground">
                  {d.createdAt}
                </span>
              </div>
            </button>
          ))}
        </div>
      </div>

      <DeploymentDrawer deployment={selected} onClose={() => setSelected(null)} />
    </div>
  )
}

function GateIcon({ gate }: { gate: Deployment['complianceGate'] }) {
  if (gate === 'running')
    return <Loader2 className="size-4 animate-spin text-info" />
  if (gate === 'passed')
    return <ShieldCheck className="size-4 text-success" />
  return <ShieldAlert className="size-4 text-critical" />
}
