import { Link } from 'react-router-dom'
import { Rocket, Boxes, ShieldCheck, Cloud, ArrowUpRight } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { StatusDot } from '@/components/ui/status'
import { apiFetch } from '@/lib/api'
import type { StatusKind } from '@/components/ui/status'

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

interface ApiCluster {
  id: string
  name: string
  endpoint: string
  status: string
  created_at: string
}

interface ApiCloudResource {
  id: string
  provider: string
}

function backendStatusToKind(s: string): StatusKind {
  const map: Record<string, StatusKind> = {
    pending: 'pending',
    building: 'building',
    deploying: 'building',
    running: 'ready',
    failed: 'failed',
    destroyed: 'idle',
    connected: 'healthy',
    unknown: 'unknown',
    degraded: 'degraded',
    healthy: 'healthy',
  }
  return map[s] ?? 'unknown'
}

function relativeTime(iso: string) {
  const diff = Date.now() - new Date(iso).getTime()
  const s = Math.floor(diff / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

export function OverviewPage() {
  const deploymentsQ = useQuery({
    queryKey: ['deployments'],
    queryFn: () => apiFetch<ApiDeployment[]>('/deployments'),
    refetchInterval: 10_000,
  })

  const clustersQ = useQuery({
    queryKey: ['clusters'],
    queryFn: () => apiFetch<ApiCluster[]>('/clusters'),
    refetchInterval: 15_000,
  })

  const cloudResourcesQ = useQuery({
    queryKey: ['cloud-resources'],
    queryFn: () => apiFetch<ApiCloudResource[]>('/cloud/resources'),
    refetchInterval: 60_000,
  })

  const deps = deploymentsQ.data ?? []
  const clusters = clustersQ.data ?? []
  const cloudResources = cloudResourcesQ.data ?? []

  const building = deps.filter((d) => d.status === 'building' || d.status === 'deploying').length
  const degradedClusters = clusters.filter((c) => c.status === 'degraded').length
  const awsResources = cloudResources.filter((r) => r.provider === 'aws').length
  const azureResources = cloudResources.filter((r) => r.provider === 'azure').length

  const stats = [
    {
      label: 'Active deployments',
      value: String(deps.length),
      icon: Rocket,
      to: '/deployments',
      sub: building > 0 ? `${building} building` : 'all idle',
    },
    {
      label: 'Clusters',
      value: String(clusters.length),
      icon: Boxes,
      to: '/clusters',
      sub: degradedClusters > 0 ? `${degradedClusters} degraded` : 'all healthy',
    },
    {
      label: 'Compliance gate',
      value: '—',
      icon: ShieldCheck,
      to: '/compliance',
      sub: 'see compliance page',
    },
    {
      label: 'Cloud resources',
      value: String(cloudResources.length),
      icon: Cloud,
      to: '/resources',
      sub: `${awsResources} AWS · ${azureResources} Azure`,
    },
  ]

  return (
    <div>
      <PageHeader
        title="Overview"
        description="Unified view of your self-healing, multi-cloud control plane."
      />
      <div className="space-y-6 p-4 lg:p-6">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {stats.map((s) => (
            <Link key={s.label} to={s.to}>
              <Card className="p-4 transition-colors hover:border-ring/40">
                <div className="flex items-start justify-between">
                  <s.icon className="size-4 text-muted-foreground" />
                  <ArrowUpRight className="size-4 text-muted-foreground" />
                </div>
                <p className="mt-3 text-2xl font-semibold tracking-tight">
                  {s.value}
                </p>
                <p className="text-sm text-foreground">{s.label}</p>
                <p className="text-xs text-muted-foreground">{s.sub}</p>
              </Card>
            </Link>
          ))}
        </div>

        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <Card>
            <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
              <h3 className="text-sm font-semibold">Recent deployments</h3>
              <Link to="/deployments" className="text-xs text-primary hover:underline">
                View all
              </Link>
            </div>
            <div className="divide-y divide-border">
              {deps.length === 0 && (
                <p className="px-5 py-4 text-sm text-muted-foreground">
                  {deploymentsQ.isLoading ? 'Loading…' : 'No deployments yet.'}
                </p>
              )}
              {deps.slice(0, 4).map((d) => {
                const status = backendStatusToKind(d.status)
                const commitSha = d.image_ref?.split(':').pop()?.slice(0, 7) ?? '—'
                return (
                  <div key={d.id} className="flex items-center gap-3 px-5 py-3">
                    <StatusDot status={status} pulse={status === 'building'} />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{d.name}</p>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {commitSha} · {d.branch}
                      </p>
                    </div>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {relativeTime(d.created_at)}
                    </span>
                  </div>
                )
              })}
            </div>
          </Card>

          <Card>
            <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
              <h3 className="text-sm font-semibold">Cluster health</h3>
              <Link to="/clusters" className="text-xs text-primary hover:underline">
                View all
              </Link>
            </div>
            <div className="divide-y divide-border">
              {clusters.length === 0 && (
                <p className="px-5 py-4 text-sm text-muted-foreground">
                  {clustersQ.isLoading ? 'Loading…' : 'No clusters registered.'}
                </p>
              )}
              {clusters.map((c) => {
                const status = backendStatusToKind(c.status)
                return (
                  <div key={c.id} className="flex items-center gap-3 px-5 py-3">
                    <StatusDot status={status} pulse={status !== 'healthy'} />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{c.name}</p>
                      <p className="truncate text-xs text-muted-foreground">
                        {c.endpoint}
                      </p>
                    </div>
                  </div>
                )
              })}
            </div>
          </Card>
        </div>
      </div>
    </div>
  )
}
