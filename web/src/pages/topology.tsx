import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Map as MapIcon,
  LayoutGrid,
  Server,
  Database,
  Boxes,
  Box,
  Package,
  Cloud,
  Globe,
  X,
} from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { StatusBadge, StatusDot, type StatusKind } from '@/components/ui/status'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { WORLD_PATH, WORLD_W, WORLD_H, project } from '@/lib/world-map'

// ---- API shape (mirrors internal/api/topology.go) ----
interface TopoApp {
  id: string
  name: string
  kind: string
  framework?: string
  target_type: string
  status: string
  deployment_id: string
  pods_ready?: number
  pods_total?: number
}
interface TopoNode {
  id: string
  kind: string
  region_key: string
  provider: string
  name: string
  resource_type?: string
  status: string
  apps: TopoApp[]
}
interface TopoEdge {
  id: string
  from_node: string
  to_node: string
  from_region: string
  to_region: string
  alias?: string
  port?: number
  cross_region: boolean
}
interface TopoRegion {
  key: string
  provider: string
  region?: string
  label: string
  lat: number
  lon: number
  located: boolean
  node_count: number
}
interface TopologyResponse {
  regions: TopoRegion[]
  nodes: TopoNode[]
  edges: TopoEdge[]
}

// Provider identity colours — deliberately distinct from the semantic
// status palette (success/warning/critical). Used for pins, region accents
// and cross-cloud arcs.
const providerColor: Record<string, string> = {
  local: '#34d399', // emerald
  aws: '#f5a524', // amber
  azure: '#38bdf8', // sky
}
function provColor(p: string): string {
  return providerColor[p] ?? '#94a3b8'
}
function providerLabel(p: string): string {
  return p === 'aws' ? 'AWS' : p === 'azure' ? 'Azure' : p === 'local' ? 'Local' : p
}

// Backend status strings → the shared StatusKind palette.
function topoStatusToKind(s: string): StatusKind {
  const map: Record<string, StatusKind> = {
    applied: 'ready',
    running: 'running',
    healthy: 'healthy',
    deploying: 'building',
    applying: 'building',
    building: 'building',
    pending: 'pending',
    planned: 'pending',
    queued: 'queued',
    failed: 'failed',
    destroying: 'idle',
    destroyed: 'idle',
    idle: 'idle',
  }
  return map[s] ?? 'unknown'
}

function resourceIcon(resourceType?: string) {
  switch (resourceType) {
    case 'vm':
    case 'ec2':
      return Server
    case 'rds':
    case 'postgres':
    case 'azure_postgres':
      return Database
    case 'ecs':
    case 'kind':
      return Boxes
    case 'aci':
    case 'docker':
      return Box
    case 'ecr':
    case 'acr':
      return Package
    default:
      return Cloud
  }
}

// ---- arc geometry (map view) ----
// A quadratic curve between two pins, bowed toward the top of the map so
// arcs read as flight-paths rather than straight lines.
function arcPath(x1: number, y1: number, x2: number, y2: number): string {
  const mx = (x1 + x2) / 2
  const my = (y1 + y2) / 2
  const dx = x2 - x1
  const dy = y2 - y1
  const dist = Math.hypot(dx, dy) || 1
  const nx = -dy / dist
  const ny = dx / dist
  const lift = Math.min(dist * 0.3, 130)
  let cx = mx + nx * lift
  let cy = my + ny * lift
  if (cy > my) {
    // ensure the control point bows upward (toward smaller y)
    cx = mx - nx * lift
    cy = my - ny * lift
  }
  return `M ${x1.toFixed(1)} ${y1.toFixed(1)} Q ${cx.toFixed(1)} ${cy.toFixed(1)} ${x2.toFixed(1)} ${y2.toFixed(1)}`
}

export function TopologyPage() {
  const { token } = useAuth()
  const qc = useQueryClient()
  const [view, setView] = useState<'map' | 'grid'>('map')
  const [selectedRegion, setSelectedRegion] = useState<string | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: ['topology'],
    queryFn: () => apiFetch<TopologyResponse>('/topology'),
    refetchInterval: 20_000,
  })

  // Live: any event that could change a node's health or the estate refetches
  // the graph. Cheap (one read-only query) and keeps state in one place.
  const onEvent = useCallback(
    (type: string) => {
      if (
        type === 'resource' ||
        type === 'deploy.status' ||
        type === 'pod.health' ||
        type === 'container.health' ||
        type === 'cloud.resource' ||
        type === 'pod.heal.restart' ||
        type === 'pod.heal.gaveup' ||
        type === 'container.heal.restart' ||
        type === 'container.heal.gaveup'
      ) {
        qc.invalidateQueries({ queryKey: ['topology'] })
      }
    },
    [qc],
  )
  useSSE(token, onEvent)

  const regions = data?.regions ?? []
  const nodes = data?.nodes ?? []
  const edges = data?.edges ?? []

  const nodesByRegion = useMemo(() => {
    const m: Record<string, TopoNode[]> = {}
    for (const n of nodes) (m[n.region_key] ??= []).push(n)
    return m
  }, [nodes])

  const regionByKey = useMemo(() => {
    const m: Record<string, TopoRegion> = {}
    for (const r of regions) m[r.key] = r
    return m
  }, [regions])

  const totalResources = nodes.length
  const crossLinks = edges.filter((e) => e.cross_region).length

  const selected = selectedRegion ? regionByKey[selectedRegion] : undefined

  return (
    <div>
      <PageHeader
        title="Topology"
        description="Every resource AutoDevOps manages, by cloud region, with the live dependency links between apps wherever they run."
      >
        <div className="flex items-center gap-3">
          <div className="hidden items-center gap-3 rounded-lg border border-border bg-card px-3 py-1.5 sm:flex">
            <Stat label="Regions" value={regions.length} />
            <span className="h-4 w-px bg-border" />
            <Stat label="Resources" value={totalResources} />
            <span className="h-4 w-px bg-border" />
            <Stat label="Cross-cloud" value={crossLinks} />
          </div>
          <ViewToggle view={view} onChange={setView} />
        </div>
      </PageHeader>

      <div className="p-4 lg:p-6">
        {isLoading && (
          <p className="text-sm text-muted-foreground">Loading topology…</p>
        )}
        {error && (
          <p className="text-sm text-critical">
            Failed to load topology: {(error as Error).message}
          </p>
        )}
        {!isLoading && !error && totalResources === 0 && <EmptyState />}

        {!isLoading && !error && totalResources > 0 && view === 'map' && (
          <div className="flex flex-col gap-4 lg:flex-row">
            <div className="min-w-0 flex-1">
              <WorldMap
                regions={regions}
                edges={edges}
                selected={selectedRegion}
                onSelect={(key) =>
                  setSelectedRegion((cur) => (cur === key ? null : key))
                }
              />
              <MapLegend regions={regions} />
            </div>
            {selected && (
              <aside className="w-full shrink-0 lg:w-[380px]">
                <div className="lg:sticky lg:top-6">
                  <div className="mb-2 flex items-center justify-between">
                    <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                      Region detail
                    </span>
                    <button
                      onClick={() => setSelectedRegion(null)}
                      className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                      aria-label="Close region detail"
                    >
                      <X className="size-4" />
                    </button>
                  </div>
                  <RegionSquare
                    region={selected}
                    nodes={nodesByRegion[selected.key] ?? []}
                  />
                </div>
              </aside>
            )}
          </div>
        )}

        {!isLoading && !error && totalResources > 0 && view === 'grid' && (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {regions.map((r) => (
              <RegionSquare
                key={r.key}
                region={r}
                nodes={nodesByRegion[r.key] ?? []}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex flex-col leading-none">
      <span className="font-mono text-sm font-semibold">{value}</span>
      <span className="text-[0.6rem] uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
    </div>
  )
}

function ViewToggle({
  view,
  onChange,
}: {
  view: 'map' | 'grid'
  onChange: (v: 'map' | 'grid') => void
}) {
  return (
    <div className="inline-flex rounded-lg border border-border bg-card p-0.5">
      {(
        [
          { key: 'map', label: 'Map', icon: MapIcon },
          { key: 'grid', label: 'Grid', icon: LayoutGrid },
        ] as const
      ).map((opt) => (
        <button
          key={opt.key}
          onClick={() => onChange(opt.key)}
          className={cn(
            'flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
            view === opt.key
              ? 'bg-muted text-foreground'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          <opt.icon className="size-3.5" />
          {opt.label}
        </button>
      ))}
    </div>
  )
}

function EmptyState() {
  return (
    <Card className="flex flex-col items-center gap-3 p-10 text-center">
      <Globe className="size-8 text-muted-foreground" />
      <div>
        <p className="text-sm font-medium">Nothing on the map yet</p>
        <p className="mt-1 text-sm text-muted-foreground">
          Provision a cloud resource or deploy an app and it will appear here,
          plotted by region.
        </p>
      </div>
      <div className="flex gap-2">
        <Link
          to="/new-resource"
          className="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground"
        >
          New resource
        </Link>
        <Link
          to="/new-deployment"
          className="rounded-md border border-border px-3 py-1.5 text-xs font-medium hover:bg-muted"
        >
          New deployment
        </Link>
      </div>
    </Card>
  )
}

// ---- World map (SVG) ----
function WorldMap({
  regions,
  edges,
  selected,
  onSelect,
}: {
  regions: TopoRegion[]
  edges: TopoEdge[]
  selected: string | null
  onSelect: (key: string) => void
}) {
  const located = regions.filter((r) => r.located)
  const posByKey: Record<string, { x: number; y: number }> = {}
  for (const r of located) posByKey[r.key] = project(r.lat, r.lon)

  // One arc per unique cross-region pair; direction from → to.
  const arcs = useMemo(() => {
    const seen = new Set<string>()
    const out: {
      key: string
      d: string
      color: string
      from: string
      to: string
    }[] = []
    for (const e of edges) {
      if (!e.cross_region) continue
      const a = posByKey[e.from_region]
      const b = posByKey[e.to_region]
      if (!a || !b) continue
      const pairKey = `${e.from_region}->${e.to_region}`
      if (seen.has(pairKey)) continue
      seen.add(pairKey)
      const prov = e.from_region.split(':')[0]
      out.push({
        key: pairKey,
        d: arcPath(a.x, a.y, b.x, b.y),
        color: provColor(prov),
        from: e.from_region,
        to: e.to_region,
      })
    }
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [edges, regions])

  return (
    <div className="overflow-hidden rounded-xl border border-border">
      <svg
        viewBox={`0 0 ${WORLD_W} ${WORLD_H}`}
        preserveAspectRatio="xMidYMid meet"
        className="block h-auto w-full"
        role="img"
        aria-label="World map of managed regions"
        style={{ background: '#0c1830' }}
      >
        <defs>
          <marker
            id="topo-arrow"
            markerWidth="7"
            markerHeight="7"
            refX="5.5"
            refY="3"
            orient="auto"
          >
            <path d="M0,0 L6,3 L0,6 Z" fill="#dbe6f5" />
          </marker>
        </defs>

        <path d={WORLD_PATH} fill="#22405f" />

        {/* arcs (under pins) */}
        {arcs.map((a) => (
          <path
            key={a.key}
            d={a.d}
            fill="none"
            stroke={a.color}
            strokeWidth={1.4}
            strokeDasharray="5 5"
            markerEnd="url(#topo-arrow)"
            opacity={0.9}
          >
            <animate
              attributeName="stroke-dashoffset"
              from="0"
              to="-20"
              dur="1.1s"
              repeatCount="indefinite"
            />
          </path>
        ))}

        {/* pins */}
        {located.map((r) => {
          const p = posByKey[r.key]
          const c = provColor(r.provider)
          const isSel = selected === r.key
          const labelRight = p.x < WORLD_W * 0.82
          return (
            <g
              key={r.key}
              className="cursor-pointer"
              onClick={() => onSelect(r.key)}
            >
              <circle cx={p.x} cy={p.y} r={isSel ? 13 : 10} fill={c} opacity={0.16} />
              <circle
                cx={p.x}
                cy={p.y}
                r={isSel ? 8 : 6.5}
                fill="none"
                stroke={c}
                strokeWidth={isSel ? 2 : 1.3}
              />
              <circle cx={p.x} cy={p.y} r={4} fill={c} />
              {/* count badge */}
              <circle cx={p.x + 6} cy={p.y - 6} r={5.5} fill={c} />
              <text
                x={p.x + 6}
                y={p.y - 6}
                textAnchor="middle"
                dominantBaseline="central"
                fontSize={7}
                fontWeight="700"
                fill="#0c1830"
                style={{ pointerEvents: 'none' }}
              >
                {r.node_count}
              </text>
              <text
                x={labelRight ? p.x + 14 : p.x - 14}
                y={p.y + 3.5}
                textAnchor={labelRight ? 'start' : 'end'}
                fontSize={10}
                fontWeight="600"
                fill="#eaf2ff"
                style={{ pointerEvents: 'none' }}
              >
                {providerLabel(r.provider)}
              </text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}

function MapLegend({ regions }: { regions: TopoRegion[] }) {
  const providers = Array.from(new Set(regions.map((r) => r.provider)))
  const unlocated = regions.filter((r) => !r.located)
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-muted-foreground">
      {providers.map((p) => (
        <span key={p} className="inline-flex items-center gap-1.5">
          <span
            className="size-2.5 rounded-full"
            style={{ background: provColor(p) }}
          />
          {providerLabel(p)}
        </span>
      ))}
      <span className="inline-flex items-center gap-1.5">
        <span className="inline-block w-5 border-t border-dashed border-foreground/60" />
        cross-cloud link
      </span>
      {unlocated.length > 0 && (
        <span className="text-muted-foreground/70">
          {unlocated.length} region{unlocated.length !== 1 ? 's' : ''} without a
          known location (see Grid view)
        </span>
      )}
    </div>
  )
}

// ---- Region square (shared by Map dock + Grid) ----
function RegionSquare({
  region,
  nodes,
}: {
  region: TopoRegion
  nodes: TopoNode[]
}) {
  const c = provColor(region.provider)
  return (
    <Card className="overflow-hidden p-0" style={{ borderTopColor: c, borderTopWidth: 3 }}>
      <div className="flex items-center gap-2.5 border-b border-border px-4 py-3">
        <span
          className="size-2.5 rounded-full"
          style={{ background: c }}
        />
        <span className="text-sm font-semibold">
          {providerLabel(region.provider)}
        </span>
        {region.region && (
          <span className="font-mono text-xs text-muted-foreground">
            {region.region}
          </span>
        )}
        <span className="ml-auto font-mono text-xs text-muted-foreground">
          {nodes.length} resource{nodes.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div className="flex flex-col gap-2.5 p-3">
        {nodes.length === 0 && (
          <p className="px-1 py-2 text-xs text-muted-foreground">
            No resources in this region.
          </p>
        )}
        {nodes.map((n) => (
          <NodeCard key={n.id} node={n} accent={c} />
        ))}
      </div>
    </Card>
  )
}

function NodeCard({ node, accent }: { node: TopoNode; accent: string }) {
  const Icon = resourceIcon(node.resource_type)
  const status = topoStatusToKind(node.status)
  const isData =
    node.apps.length === 0 &&
    ['rds', 'postgres', 'azure_postgres'].includes(node.resource_type ?? '')
  return (
    <div className="rounded-lg border border-border bg-background/40 p-3">
      <div className="flex items-center gap-2">
        <span
          className="flex size-6 items-center justify-center rounded-md"
          style={{ background: `${accent}22`, color: accent }}
        >
          <Icon className="size-3.5" />
        </span>
        <span className="text-[0.65rem] font-semibold uppercase tracking-wider" style={{ color: accent }}>
          {node.resource_type || node.kind}
        </span>
        <div className="ml-auto">
          <StatusBadge status={status} pulse={status === 'building'} />
        </div>
      </div>
      <p className="mt-2 truncate font-mono text-[0.8rem] font-semibold">
        {node.name}
      </p>
      {isData && (
        <p className="mt-1.5 text-[0.7rem] text-muted-foreground">
          managed database · no shell
        </p>
      )}
      {node.apps.map((a) => (
        <AppChip key={a.id} app={a} />
      ))}
    </div>
  )
}

function AppChip({ app }: { app: TopoApp }) {
  const status = topoStatusToKind(app.status)
  return (
    <div className="mt-2 flex items-center gap-2 rounded-md bg-muted/40 px-2 py-1.5">
      <StatusDot status={status} pulse={status === 'building'} />
      <span className="truncate font-mono text-[0.7rem] font-medium">
        {app.name}
      </span>
      {app.framework && (
        <span className="rounded border border-border px-1.5 py-0.5 text-[0.6rem] text-foreground">
          {app.framework}
        </span>
      )}
      <span className="rounded border border-border px-1.5 py-0.5 text-[0.6rem] text-muted-foreground">
        {app.target_type}
      </span>
      {app.target_type === 'k8s' && (app.pods_total ?? 0) > 0 && (
        <span className="ml-auto font-mono text-[0.65rem] font-semibold text-success">
          {app.pods_ready}/{app.pods_total}
        </span>
      )}
    </div>
  )
}
