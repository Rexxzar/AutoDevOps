import { useState, useCallback, useEffect, useMemo } from 'react'
import { Boxes, Cpu, Server, X, ChevronDown, ChevronRight, AlertCircle, RefreshCw } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { ProviderTag } from '@/components/meta-tags'
import { HealEventStream } from '@/components/heal-event-stream'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { initialHealEvents, extractAppName } from '@/lib/types'
import type { Pod, HealEvent } from '@/lib/types'

interface ApiCluster {
  id: string
  name: string
  endpoint: string
  status: string
  created_at: string
}

interface ApiPod {
  cluster_id: string
  namespace: string
  pod_name: string
  status: string
  restart_count: number
  reason?: string
}

type PodMap = Record<string, Pod>

function statusToKind(s: string): StatusKind {
  const map: Record<string, StatusKind> = {
    healthy: 'healthy', connected: 'healthy',
    degraded: 'degraded',
    unknown: 'unknown',
    unhealthy: 'critical',
  }
  return map[s] ?? 'unknown'
}

function podStatusToKind(s: string): StatusKind {
  const map: Record<string, StatusKind> = {
    healthy: 'running', unhealthy: 'critical', pending: 'pending',
    terminating: 'warning', unknown: 'unknown', succeeded: 'idle',
  }
  return map[s] ?? 'unknown'
}

function podStatusLabel(status: StatusKind, reason?: string): string {
  if (reason) return reason
  const labels: Record<string, string> = {
    running: 'Running',
    critical: 'Unhealthy',
    pending: 'Pending',
    warning: 'Terminating',
    idle: 'Completed',
    unknown: 'Unknown',
  }
  return labels[status] ?? status
}

const podStatusColor: Record<string, string> = {
  running: 'bg-success',
  healthy: 'bg-success',
  pending: 'bg-info animate-pulse',
  warning: 'bg-yellow-500',
  critical: 'bg-critical animate-pulse',
  failed: 'bg-critical',
  idle: 'bg-muted-foreground/50',
  unknown: 'bg-muted-foreground/50',
}

const podStatusBorder: Record<string, string> = {
  running: 'border-success/30',
  healthy: 'border-success/30',
  pending: 'border-info/30',
  warning: 'border-yellow-500/30',
  critical: 'border-critical/30',
  failed: 'border-critical/30',
  idle: 'border-border',
  unknown: 'border-border',
}

function nowStamp() {
  return new Date().toLocaleTimeString('en-GB', { hour12: false })
}

function StatTile({ icon: Icon, label, value }: { icon: typeof Cpu; label: string; value: string }) {
  return (
    <div className="flex items-center gap-2.5 rounded-lg border border-border bg-background/40 px-3 py-2">
      <Icon className="size-4 text-muted-foreground" />
      <div>
        <div className="text-[11px] uppercase tracking-wide text-muted-foreground">{label}</div>
        <div className="font-mono text-sm font-medium">{value}</div>
      </div>
    </div>
  )
}

function NamespaceLabel({ ns }: { ns: string }) {
  if (ns.startsWith('autodevops-')) {
    const stackName = ns.slice('autodevops-'.length)
    return (
      <span className="inline-flex items-center gap-1.5">
        <span className="font-medium">{stackName}</span>
        <span className="text-[0.6rem] text-muted-foreground uppercase tracking-wider">stack</span>
      </span>
    )
  }
  if (ns.startsWith('resource-')) {
    const resName = ns.slice('resource-'.length)
    return (
      <span className="inline-flex items-center gap-1.5">
        <span className="font-medium">{resName}</span>
        <span className="text-[0.6rem] text-muted-foreground uppercase tracking-wider">resource</span>
      </span>
    )
  }
  return <span className="font-medium">{ns}</span>
}

function PodCard({ pod, onClick }: { pod: Pod; onClick: () => void }) {
  const label = podStatusLabel(pod.status, pod.reason)
  return (
    <button
      onClick={onClick}
      className={cn(
        'flex items-center gap-2 rounded-lg border px-3 py-2 text-left transition-all',
        'hover:shadow-md hover:ring-1 hover:ring-ring/30 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        podStatusBorder[pod.status] ?? 'border-border',
        'bg-card',
      )}
    >
      <span className={cn('size-2.5 shrink-0 rounded-full', podStatusColor[pod.status] ?? 'bg-muted-foreground/50')} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-xs font-medium">{pod.appName || pod.name}</p>
        <p className="truncate text-[0.65rem] text-muted-foreground">
          {label}
          {pod.restarts > 0 && <span className="ml-1.5 text-yellow-600 dark:text-yellow-400">({pod.restarts} restart{pod.restarts !== 1 ? 's' : ''})</span>}
        </p>
      </div>
    </button>
  )
}

function NamespaceGroup({
  namespace,
  pods,
  onSelectPod,
}: {
  namespace: string
  pods: Pod[]
  onSelectPod: (pod: Pod) => void
}) {
  const [collapsed, setCollapsed] = useState(false)
  const healthy = pods.filter((p) => p.status === 'running').length
  const unhealthy = pods.filter((p) => p.status === 'critical').length

  return (
    <div className="rounded-lg border border-border bg-background/40">
      <button
        onClick={() => setCollapsed((v) => !v)}
        className="flex w-full items-center justify-between gap-2 px-3 py-2 text-left hover:bg-muted/30 rounded-t-lg transition-colors"
      >
        <div className="flex items-center gap-2 text-xs">
          {collapsed ? <ChevronRight className="size-3.5 text-muted-foreground" /> : <ChevronDown className="size-3.5 text-muted-foreground" />}
          <NamespaceLabel ns={namespace} />
        </div>
        <div className="flex items-center gap-2 text-[0.65rem] text-muted-foreground">
          <span>{pods.length} pod{pods.length !== 1 ? 's' : ''}</span>
          {healthy > 0 && <span className="text-success">{healthy} healthy</span>}
          {unhealthy > 0 && <span className="text-critical">{unhealthy} unhealthy</span>}
        </div>
      </button>
      {!collapsed && (
        <div className="grid grid-cols-1 gap-1.5 px-3 pb-3 sm:grid-cols-2 lg:grid-cols-3">
          {pods.map((pod) => (
            <PodCard key={pod.name} pod={pod} onClick={() => onSelectPod(pod)} />
          ))}
        </div>
      )}
    </div>
  )
}

function PodDetail({ pod, onClose }: { pod: Pod; onClose: () => void }) {
  const label = podStatusLabel(pod.status, pod.reason)
  const rows: [string, React.ReactNode][] = [
    ['App', pod.appName || extractAppName(pod.name)],
    ['Pod name', <span className="font-mono text-[0.7rem]">{pod.name}</span>],
    ['Namespace', pod.namespace ? <span className="font-mono text-[0.7rem]">{pod.namespace}</span> : '—'],
    ['Status', label],
    ['Ready', pod.ready ? <span className="text-success">Yes</span> : <span className="text-critical">No</span>],
    ['Restarts', pod.restarts > 0 ? (
      <span className="inline-flex items-center gap-1">
        <RefreshCw className="size-3 text-yellow-500" />
        <span>{pod.restarts}</span>
      </span>
    ) : '0'],
  ]
  if (pod.reason) {
    rows.push(['Reason', (
      <span className="inline-flex items-center gap-1 text-critical">
        <AlertCircle className="size-3" />
        {pod.reason}
      </span>
    )])
  }

  return (
    <div className="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onClose} />
      <div className="relative flex h-full w-full max-w-md flex-col border-l border-border bg-card shadow-2xl">
        <div className="flex items-center justify-between border-b border-border px-5 py-4">
          <div className="min-w-0 flex-1">
            <h3 className="truncate text-sm font-semibold">{pod.appName || extractAppName(pod.name)}</h3>
            <p className="truncate font-mono text-[0.65rem] text-muted-foreground mt-0.5">{pod.name}</p>
          </div>
          <button onClick={onClose} className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
            <X className="size-4" />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto p-5">
          <div className="mb-4 flex items-center gap-2">
            <span className={cn('size-3 rounded-full', podStatusColor[pod.status] ?? 'bg-muted-foreground/50')} />
            <span className="text-sm font-medium">{label}</span>
          </div>
          <dl className="divide-y divide-border/60 rounded-lg border border-border">
            {rows.map(([k, v]) => (
              <div key={k as string} className="flex items-center justify-between px-4 py-2.5">
                <dt className="text-xs text-muted-foreground">{k}</dt>
                <dd className="text-sm text-right">{v}</dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
    </div>
  )
}

function PodLegend() {
  const items: { label: string; color: string }[] = [
    { label: 'Running', color: 'bg-success' },
    { label: 'Pending', color: 'bg-info' },
    { label: 'Terminating', color: 'bg-yellow-500' },
    { label: 'Unhealthy', color: 'bg-critical' },
    { label: 'Completed', color: 'bg-muted-foreground/50' },
  ]
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
      {items.map((i) => (
        <div key={i.label} className="flex items-center gap-1.5">
          <span className={cn('h-2.5 w-2.5 rounded-full', i.color)} />
          <span className="text-xs text-muted-foreground">{i.label}</span>
        </div>
      ))}
    </div>
  )
}

export function ClustersPage() {
  const { token } = useAuth()
  const [selectedPod, setSelectedPod] = useState<Pod | null>(null)
  const [podMap, setPodMap] = useState<PodMap>({})
  const [healEvents, setHealEvents] = useState<HealEvent[]>(initialHealEvents)
  const [liveStream, setLiveStream] = useState(true)

  const { data: clusters = [], isLoading } = useQuery({
    queryKey: ['clusters'],
    queryFn: () => apiFetch<ApiCluster[]>('/clusters'),
    refetchInterval: 15_000,
  })

  const { data: podRows } = useQuery({
    queryKey: ['cluster-pods'],
    queryFn: () => apiFetch<ApiPod[]>('/clusters/pods'),
    refetchInterval: 10_000,
  })

  useEffect(() => {
    if (!podRows) return
    const next: PodMap = {}
    for (const p of podRows) {
      const appName = extractAppName(p.pod_name)
      next[`${p.cluster_id}/${p.pod_name}`] = {
        name: p.pod_name,
        status: podStatusToKind(p.status),
        restarts: p.restart_count,
        cpu: 0, memory: 0, node: '', age: '',
        ready: p.status === 'healthy',
        namespace: p.namespace,
        reason: p.reason,
        appName,
      }
    }
    setPodMap(next)
  }, [podRows])

  const handleSSEEvent = useCallback((type: string, data: unknown) => {
    const d = data as Record<string, unknown>

    if (type === 'pod.health') {
      const clusterID = String(d.cluster_id ?? '')
      const podName = String(d.pod_name ?? '')
      const key = `${clusterID}/${podName}`
      const appName = extractAppName(podName)
      setPodMap((prev) => ({
        ...prev,
        [key]: {
          name: podName,
          status: podStatusToKind(String(d.status ?? 'unknown')),
          restarts: Number(d.restart_count ?? 0),
          cpu: 0,
          memory: 0,
          node: '',
          age: '',
          ready: String(d.status) === 'healthy',
          namespace: String(d.namespace ?? ''),
          reason: d.reason ? String(d.reason) : undefined,
          appName,
        },
      }))
    }

    if ((type === 'pod.heal.restart' || type === 'pod.heal.gaveup') && liveStream) {
      const isRestart = type === 'pod.heal.restart'
      const podName = String(d.pod_name ?? '')
      const event: HealEvent = {
        id: `h_${Date.now()}`,
        ts: nowStamp(),
        cluster: String(d.cluster_id ?? ''),
        pod: extractAppName(podName),
        action: isRestart
          ? `Auto-restart (attempt ${d.attempt ?? '?'})`
          : 'Gave up — restart budget exhausted',
        detail: isRestart
          ? `${d.reason ?? 'unknown'} · ${podName}`
          : `Pod exceeded max restart attempts`,
        tone: isRestart ? 'info' : 'warning',
        backoff: isRestart ? undefined : undefined,
      }
      setHealEvents((prev) => [event, ...prev].slice(0, 40))
    }

    // Docker container self-healing — folded into the same live stream.
    if ((type === 'container.heal.restart' || type === 'container.heal.gaveup') && liveStream) {
      const isRestart = type === 'container.heal.restart'
      const name = String(d.name ?? d.container_name ?? '')
      const event: HealEvent = {
        id: `h_${Date.now()}`,
        ts: nowStamp(),
        cluster: 'docker',
        pod: name,
        action: isRestart
          ? `Auto-restart (attempt ${d.attempt ?? '?'})`
          : 'Gave up — restart budget exhausted',
        detail: isRestart
          ? `${d.reason ?? 'unknown'} · ${name}`
          : `Container exceeded max restart attempts`,
        tone: isRestart ? 'info' : 'warning',
      }
      setHealEvents((prev) => [event, ...prev].slice(0, 40))
    }
  }, [liveStream])

  useSSE(token, handleSSEEvent)

  const totalPods = Object.keys(podMap).length

  return (
    <div>
      <PageHeader
        title="Clusters"
        description="Kubernetes self-healing controller — live pod health and autonomous recovery"
      >
        <div className="flex items-center gap-2 rounded-lg border border-border bg-card px-3 py-1.5">
          <span className="text-xs text-muted-foreground">Pods tracked</span>
          <span className="font-mono text-sm font-semibold text-primary">
            {totalPods}
          </span>
        </div>
      </PageHeader>

      <div className="flex flex-col gap-6 p-4 lg:flex-row lg:p-6">
        <div className="flex min-w-0 flex-1 flex-col gap-6">
          <div className="flex items-center justify-between rounded-lg border border-border bg-card px-4 py-3">
            <PodLegend />
          </div>

          <div className="flex flex-col gap-6">
            {isLoading && (
              <p className="text-sm text-muted-foreground">Loading clusters…</p>
            )}
            {!isLoading && clusters.length === 0 && (
              <p className="text-sm text-muted-foreground">
                No clusters registered. Register a cluster via the CLI or API.
              </p>
            )}
            {clusters.map((c) => {
              const pods = Object.entries(podMap)
                .filter(([key]) => key.startsWith(c.id + '/'))
                .map(([, pod]) => pod)
              const readyCount = pods.filter((p) => p.ready).length
              const status = statusToKind(c.status)

              const namespaceGroups = pods.reduce<Record<string, Pod[]>>((acc, pod) => {
                const ns = pod.namespace || 'default'
                if (!acc[ns]) acc[ns] = []
                acc[ns].push(pod)
                return acc
              }, {})
              const sortedNamespaces = Object.keys(namespaceGroups).sort()

              return (
                <Card key={c.id} className="overflow-hidden p-0">
                  <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4">
                    <div className="flex items-center gap-3">
                      <div className="flex size-9 items-center justify-center rounded-lg bg-muted">
                        <Boxes className="size-4 text-primary" />
                      </div>
                      <div>
                        <div className="flex items-center gap-2">
                          <h3 className="font-mono text-sm font-semibold">{c.name}</h3>
                          <ProviderTag provider="kind" />
                        </div>
                        <p className="mt-0.5 font-mono text-xs text-muted-foreground">
                          {c.endpoint}
                        </p>
                      </div>
                    </div>
                    <StatusBadge status={status} pulse={status !== 'healthy'} />
                  </div>

                  <div className="grid grid-cols-2 gap-2 px-5 py-4 sm:grid-cols-3">
                    <StatTile icon={Server} label="Endpoint" value={c.endpoint.length > 20 ? c.endpoint.slice(0, 20) + '…' : c.endpoint} />
                    <StatTile icon={Boxes} label="Pods" value={`${readyCount} healthy / ${pods.length} total`} />
                    <StatTile icon={Cpu} label="Namespaces" value={String(sortedNamespaces.length)} />
                  </div>

                  <div className="border-t border-border px-5 py-4 space-y-2">
                    <div className="mb-2 flex items-center justify-between">
                      <span className="text-xs font-medium text-muted-foreground">Pod health by namespace</span>
                      <span className="font-mono text-xs text-muted-foreground">{pods.length} pods</span>
                    </div>
                    {sortedNamespaces.length === 0 ? (
                      <p className="text-xs text-muted-foreground">No pod data yet — waiting for health events.</p>
                    ) : (
                      sortedNamespaces.map((ns) => (
                        <NamespaceGroup
                          key={ns}
                          namespace={ns}
                          pods={namespaceGroups[ns]}
                          onSelectPod={setSelectedPod}
                        />
                      ))
                    )}
                  </div>
                </Card>
              )
            })}
          </div>
        </div>

        <div className="flex w-full shrink-0 flex-col gap-4 lg:sticky lg:top-6 lg:w-[360px]">
          <HealMetricsCard />
          <Card className="h-[560px] overflow-hidden p-0">
            <HealEventStream
              events={healEvents}
              live={liveStream}
              onToggleLive={() => setLiveStream((v) => !v)}
            />
          </Card>
        </div>
      </div>

      {selectedPod && <PodDetail pod={selectedPod} onClose={() => setSelectedPod(null)} />}
    </div>
  )
}

interface HealMetrics {
  overall: {
    total: number
    recovered: number
    unrecovered: number
    recovery_rate: number
    avg_mttd_seconds?: number
    avg_mttr_seconds?: number
  }
}

function fmtHealSeconds(s?: number): string {
  if (s == null) return '—'
  if (s < 1) return '<1s'
  if (s < 60) return `${Math.round(s)}s`
  const m = Math.floor(s / 60)
  const rem = Math.round(s % 60)
  return `${m}m ${rem}s`
}

// HealMetricsCard shows aggregate MTTD/MTTR/recovery rate from /heal/metrics —
// the headline self-healing numbers for the evaluation (covers pods + containers).
function HealMetricsCard() {
  const { data } = useQuery({
    queryKey: ['heal-metrics'],
    queryFn: () => apiFetch<HealMetrics>('/heal/metrics'),
    refetchInterval: 30_000,
  })
  const o = data?.overall
  return (
    <Card className="p-4">
      <h3 className="text-sm font-semibold">Self-healing metrics</h3>
      <div className="mt-3 grid grid-cols-3 gap-3">
        <HealStat label="Recovery rate" value={o && o.total > 0 ? `${Math.round(o.recovery_rate * 100)}%` : '—'} />
        <HealStat label="Avg MTTD" value={fmtHealSeconds(o?.avg_mttd_seconds)} />
        <HealStat label="Avg MTTR" value={fmtHealSeconds(o?.avg_mttr_seconds)} />
      </div>
      <p className="mt-2 text-[0.65rem] text-muted-foreground">
        {o && o.total > 0 ? `${o.recovered}/${o.total} episodes recovered (pods + containers)` : 'No healing episodes recorded yet'}
      </p>
    </Card>
  )
}

function HealStat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-[0.6rem] uppercase tracking-wide text-muted-foreground">{label}</p>
      <p className="font-mono text-base font-semibold">{value}</p>
    </div>
  )
}
