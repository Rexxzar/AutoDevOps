import { useState, useCallback, useEffect } from 'react'
import { Boxes, Cpu, MemoryStick, Server, X } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import { ProviderTag } from '@/components/meta-tags'
import { PodGrid, PodLegend } from '@/components/pod-grid'
import { HealEventStream } from '@/components/heal-event-stream'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { initialHealEvents } from '@/lib/mock-data'
import type { Pod, HealEvent } from '@/lib/mock-data'

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

function PodDetail({ pod, onClose }: { pod: Pod; onClose: () => void }) {
  const rows: [string, string][] = [
    ['Status', pod.status],
    ['Ready', pod.ready ? 'True' : 'False'],
    ['Restarts', String(pod.restarts)],
    ['Node', pod.node || '—'],
    ['Age', pod.age || '—'],
    ['CPU', pod.cpu ? `${pod.cpu}m` : '—'],
    ['Memory', pod.memory ? `${pod.memory}Mi` : '—'],
  ]
  return (
    <div className="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onClose} />
      <div className="relative flex h-full w-full max-w-md flex-col border-l border-border bg-card shadow-2xl">
        <div className="flex items-center justify-between border-b border-border px-5 py-4">
          <h3 className="truncate font-mono text-sm font-semibold">{pod.name}</h3>
          <button onClick={onClose} className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
            <X className="size-4" />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto p-5">
          <div className="mb-4">
            <StatusBadge status={pod.status} pulse={pod.status === 'critical'} />
          </div>
          <dl className="divide-y divide-border/60 rounded-lg border border-border">
            {rows.map(([k, v]) => (
              <div key={k} className="flex items-center justify-between px-4 py-2.5">
                <dt className="text-xs text-muted-foreground">{k}</dt>
                <dd className="font-mono text-sm">{v}</dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
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

  // Hydrate pod state from the persisted pod_health table so the page shows the
  // real pod set immediately on mount/remount (the watch loop keeps this table
  // current — deleted pods are removed), rather than flickering from empty until
  // the next live pod.health SSE event arrives. SSE events still apply instant
  // deltas on top; each refetch reconciles (and drops pods that have gone away).
  const { data: podRows } = useQuery({
    queryKey: ['cluster-pods'],
    queryFn: () => apiFetch<ApiPod[]>('/clusters/pods'),
    refetchInterval: 10_000,
  })

  useEffect(() => {
    if (!podRows) return
    const next: PodMap = {}
    for (const p of podRows) {
      next[`${p.cluster_id}/${p.pod_name}`] = {
        name: p.pod_name,
        status: podStatusToKind(p.status),
        restarts: p.restart_count,
        cpu: 0, memory: 0, node: '', age: '',
        ready: p.status === 'healthy',
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
        },
      }))
    }

    if ((type === 'pod.heal.restart' || type === 'pod.heal.gaveup') && liveStream) {
      const isRestart = type === 'pod.heal.restart'
      const event: HealEvent = {
        id: `h_${Date.now()}`,
        ts: nowStamp(),
        cluster: String(d.cluster_id ?? ''),
        pod: String(d.pod_name ?? ''),
        action: isRestart
          ? `Auto-restart scheduled (attempt ${d.attempt ?? '?'})`
          : 'Heal gave up — restart budget exhausted',
        detail: isRestart
          ? `Reason: ${d.reason ?? 'unknown'}`
          : `Pod ${d.pod_name} exceeded max restart attempts`,
        tone: isRestart ? 'info' : 'warning',
        backoff: isRestart ? undefined : undefined,
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
                    <StatTile icon={Boxes} label="Pods tracked" value={`${readyCount}/${pods.length}`} />
                    <StatTile icon={Cpu} label="Status" value={c.status} />
                  </div>

                  <div className="border-t border-border px-5 py-4">
                    <div className="mb-3 flex items-center justify-between">
                      <span className="text-xs font-medium text-muted-foreground">Pod health (via SSE)</span>
                      <span className="font-mono text-xs text-muted-foreground">{pods.length} pods</span>
                    </div>
                    <PodGrid pods={pods} onSelect={setSelectedPod} />
                  </div>
                </Card>
              )
            })}
          </div>
        </div>

        <Card className={cn('h-[640px] w-full shrink-0 overflow-hidden p-0 lg:sticky lg:top-6 lg:w-[360px]')}>
          <HealEventStream
            events={healEvents}
            live={liveStream}
            onToggleLive={() => setLiveStream((v) => !v)}
          />
        </Card>
      </div>

      {selectedPod && <PodDetail pod={selectedPod} onClose={() => setSelectedPod(null)} />}
    </div>
  )
}
