import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
  ZoomIn,
  ZoomOut,
  Maximize2,
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
  image_ref?: string
  port?: number
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
  kind: string // "dependency" | "registry"
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

// Health colour for the small status dot on each map pin (hex, since the map
// surface is a fixed dark navy and shouldn't depend on theme tokens).
function statusColor(k: StatusKind): string {
  switch (k) {
    case 'healthy':
    case 'ready':
    case 'running':
      return '#22c55e'
    case 'warning':
    case 'degraded':
    case 'queued':
      return '#f59e0b'
    case 'critical':
    case 'failed':
      return '#ef4444'
    case 'building':
    case 'pending':
      return '#38bdf8'
    default:
      return '#64748b'
  }
}

// nodeDisplayName is what a pin/card leads with. For the synthetic local host
// nodes (kind cluster / Docker host) the interesting thing is what's running
// on them, so a single-app host shows the app/container name (e.g.
// "ShopHup-Front") rather than the bare "Docker host" label.
function nodeDisplayName(node: TopoNode): string {
  if (node.kind === 'local_cluster' || node.kind === 'docker_host') {
    if (node.apps.length === 1) return node.apps[0].name
    if (node.apps.length > 1) return `${node.name} (${node.apps.length})`
  }
  return node.name
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
          <div className="relative">
            <WorldMap
              regions={regions}
              nodes={nodes}
              edges={edges}
              selected={selectedRegion}
              onSelect={(key) =>
                setSelectedRegion((cur) => (cur === key ? null : key))
              }
            />
            <MapLegend regions={regions} />
            {/* Region detail floats OVER the map (right side) rather than
                shrinking it — selecting a pin must never resize/re-zoom the map. */}
            {selected && (
              <aside className="absolute right-3 top-3 z-20 max-h-[calc(100%-1.5rem)] w-[330px] max-w-[calc(100%-1.5rem)] overflow-y-auto rounded-xl border border-border bg-card/95 shadow-2xl backdrop-blur">
                <div className="flex items-center justify-between border-b border-border px-3 py-2">
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
interface PinPos {
  x: number
  y: number
  node: TopoNode
}
const FULL_VB = { x: 0, y: 0, w: WORLD_W, h: WORLD_H }
const MIN_VB_W = WORLD_W / 9 // max ~9x zoom

function clampPan(x: number, y: number, w: number, h: number): [number, number] {
  const mx = WORLD_W * 0.15
  const my = WORLD_H * 0.15
  return [
    Math.min(Math.max(x, -mx), WORLD_W - w + mx),
    Math.min(Math.max(y, -my), WORLD_H - h + my),
  ]
}

function WorldMap({
  regions,
  nodes,
  edges,
  selected,
  onSelect,
}: {
  regions: TopoRegion[]
  nodes: TopoNode[]
  edges: TopoEdge[]
  selected: string | null
  onSelect: (key: string) => void
}) {
  const svgRef = useRef<SVGSVGElement | null>(null)
  const [vb, setVb] = useState(FULL_VB)
  const dragRef = useRef<{ cx: number; cy: number; vx: number; vy: number } | null>(null)
  const movedRef = useRef(false)

  // k keeps markers a constant on-screen size as the geography zooms.
  const k = vb.w / WORLD_W

  // Fan each region's resources out around its centroid (same-region resources
  // share coordinates, so we spread them on a small ring). A region with more
  // than one resource also gets a labelled boundary drawn around the cluster,
  // so co-located resources read as one place (e.g. "these all run on the
  // local host") instead of loose, scattered dots.
  const { pins, boundaries } = useMemo(() => {
    const byRegion: Record<string, TopoNode[]> = {}
    for (const n of nodes) (byRegion[n.region_key] ??= []).push(n)
    const p: Record<string, PinPos> = {}
    const bnds: {
      key: string
      cx: number
      cy: number
      r: number
      label: string
      color: string
    }[] = []
    for (const r of regions) {
      if (!r.located) continue
      const c = project(r.lat, r.lon)
      const rn = byRegion[r.key] ?? []
      if (rn.length <= 1) {
        if (rn[0]) p[rn[0].id] = { x: c.x, y: c.y, node: rn[0] }
      } else {
        // Tight cluster: same-region (and same-host, for local kind+docker)
        // resources should read as a group sitting together, not flung to
        // opposite sides of a wide ring. Radius grows gently with count so a
        // 2-pin region stays snug while a busy region still separates.
        const radius = 5 + Math.min(rn.length, 12) * 0.7
        rn.forEach((n, i) => {
          const a = (2 * Math.PI * i) / rn.length - Math.PI / 2
          p[n.id] = { x: c.x + radius * Math.cos(a), y: c.y + radius * Math.sin(a), node: n }
        })
        bnds.push({
          key: r.key,
          cx: c.x,
          cy: c.y,
          r: radius + 7,
          label: r.label || providerLabel(r.provider),
          color: provColor(r.provider),
        })
      }
    }
    return { pins: p, boundaries: bnds }
  }, [nodes, regions])

  // One arc per edge whose endpoints both resolved to pins.
  const arcs = useMemo(() => {
    const out: {
      key: string
      d: string
      color: string
      cross: boolean
      registry: boolean
    }[] = []
    for (const e of edges) {
      const a = pins[e.from_node]
      const b = pins[e.to_node]
      if (!a || !b) continue
      const registry = e.kind === 'registry'
      out.push({
        key: e.id,
        d: arcPath(a.x, a.y, b.x, b.y),
        // registry links get their own hue so ECS→ECR / ACI→ACR stands out
        color: registry ? '#a78bfa' : provColor(e.from_region.split(':')[0]),
        cross: e.cross_region,
        registry,
      })
    }
    return out
  }, [edges, pins])

  // Native (non-passive) wheel zoom toward the cursor.
  useEffect(() => {
    const svg = svgRef.current
    if (!svg) return
    function onWheel(e: WheelEvent) {
      e.preventDefault()
      const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
      setVb((prev) => {
        const px = prev.x + ((e.clientX - rect.left) / rect.width) * prev.w
        const py = prev.y + ((e.clientY - rect.top) / rect.height) * prev.h
        let nw = prev.w * (e.deltaY < 0 ? 0.82 : 1.22)
        nw = Math.max(MIN_VB_W, Math.min(WORLD_W, nw))
        const scale = nw / prev.w
        const nh = prev.h * scale
        const [nx, ny] = clampPan(px - (px - prev.x) * scale, py - (py - prev.y) * scale, nw, nh)
        return { x: nx, y: ny, w: nw, h: nh }
      })
    }
    svg.addEventListener('wheel', onWheel, { passive: false })
    return () => svg.removeEventListener('wheel', onWheel)
  }, [])

  function zoomByCentre(factor: number) {
    setVb((prev) => {
      let nw = Math.max(MIN_VB_W, Math.min(WORLD_W, prev.w * factor))
      const scale = nw / prev.w
      const nh = prev.h * scale
      const cx = prev.x + prev.w / 2
      const cy = prev.y + prev.h / 2
      const [nx, ny] = clampPan(cx - nw / 2, cy - nh / 2, nw, nh)
      return { x: nx, y: ny, w: nw, h: nh }
    })
  }

  function onPointerDown(e: React.PointerEvent) {
    // Don't capture the pointer here — doing so steals the click from a pin,
    // so selecting a resource (and its region zone) never fires. We only
    // capture once an actual drag starts (see onPointerMove).
    dragRef.current = { cx: e.clientX, cy: e.clientY, vx: vb.x, vy: vb.y }
    movedRef.current = false
  }
  function onPointerMove(e: React.PointerEvent) {
    const d = dragRef.current
    if (!d) return
    const dist = Math.hypot(e.clientX - d.cx, e.clientY - d.cy)
    if (!movedRef.current && dist <= 3) return // sub-threshold: keep it a click
    if (!movedRef.current) {
      movedRef.current = true
      // real drag now — capture so panning survives the pointer briefly
      // leaving the svg
      ;(e.currentTarget as SVGSVGElement).setPointerCapture(e.pointerId)
    }
    const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
    const dx = ((e.clientX - d.cx) / rect.width) * vb.w
    const dy = ((e.clientY - d.cy) / rect.height) * vb.h
    const [nx, ny] = clampPan(d.vx - dx, d.vy - dy, vb.w, vb.h)
    setVb((prev) => ({ ...prev, x: nx, y: ny }))
  }
  function onPointerUp(e: React.PointerEvent) {
    const wasDragging = movedRef.current
    dragRef.current = null
    if (wasDragging) {
      try {
        ;(e.currentTarget as SVGSVGElement).releasePointerCapture(e.pointerId)
      } catch {
        // pointer wasn't captured; nothing to release
      }
    }
  }

  const showNames = k < 0.55 // reveal per-resource labels once zoomed in
  const zoomed = vb.w < WORLD_W - 1

  return (
    <div className="relative overflow-hidden rounded-xl border border-border">
      <svg
        ref={svgRef}
        viewBox={`${vb.x} ${vb.y} ${vb.w} ${vb.h}`}
        preserveAspectRatio="xMidYMid meet"
        className="block h-auto w-full touch-none select-none"
        role="img"
        aria-label="World map of managed resources"
        style={{ background: '#0c1830', cursor: dragRef.current ? 'grabbing' : 'grab' }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerLeave={onPointerUp}
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

        {/* region zone: only the selected region gets a labelled boundary, so
            clusters of geographically-close regions never overlap each other */}
        {boundaries
          .filter((b) => b.key === selected)
          .map((b) => (
            <g key={b.key} style={{ pointerEvents: 'none' }}>
              <circle
                cx={b.cx}
                cy={b.cy}
                r={b.r}
                fill={b.color}
                fillOpacity={0.08}
                stroke={b.color}
                strokeOpacity={0.65}
                strokeWidth={1.1 * k}
                strokeDasharray={`${3 * k} ${3 * k}`}
              />
              <text
                x={b.cx}
                y={b.cy - b.r - 3 * k}
                textAnchor="middle"
                fontSize={9.5 * k}
                fontWeight="700"
                fill={b.color}
                opacity={0.95}
              >
                {b.label}
              </text>
            </g>
          ))}

        {/* arcs (under pins). Registry links are always drawn prominently so
            the ECS→ECR / ACI→ACR connection is visible even within a region. */}
        {arcs.map((a) =>
          a.cross || a.registry ? (
            <path
              key={a.key}
              d={a.d}
              fill="none"
              stroke={a.color}
              strokeWidth={1.4 * k}
              strokeDasharray={`${5 * k} ${5 * k}`}
              markerEnd="url(#topo-arrow)"
              opacity={0.9}
            >
              <animate
                attributeName="stroke-dashoffset"
                from="0"
                to={-20 * k}
                dur="1.1s"
                repeatCount="indefinite"
              />
            </path>
          ) : (
            <path
              key={a.key}
              d={a.d}
              fill="none"
              stroke="#7f95b3"
              strokeWidth={0.9 * k}
              markerEnd="url(#topo-arrow)"
              opacity={0.5}
            />
          ),
        )}

        {/* one pin per resource */}
        {Object.values(pins).map(({ x, y, node }) => {
          const c = provColor(node.provider)
          const sc = statusColor(topoStatusToKind(node.status))
          const isSel = selected === node.region_key
          const labelRight = x < WORLD_W * 0.85
          return (
            <g
              key={node.id}
              style={{ cursor: 'pointer' }}
              onClick={(e) => {
                if (movedRef.current) return
                e.stopPropagation()
                onSelect(node.region_key)
              }}
            >
              <title>
                {node.name} · {node.resource_type || node.kind} · {node.status}
                {node.apps.length > 0
                  ? `\nrunning: ${node.apps.map((a) => a.name).join(', ')}`
                  : ''}
              </title>
              <circle cx={x} cy={y} r={(isSel ? 11 : 9) * k} fill={c} opacity={0.16} />
              <circle
                cx={x}
                cy={y}
                r={(isSel ? 6.5 : 5) * k}
                fill="none"
                stroke={c}
                strokeWidth={(isSel ? 1.8 : 1.2) * k}
              />
              <circle cx={x} cy={y} r={3.4 * k} fill={c} />
              {/* health dot */}
              <circle cx={x + 4.6 * k} cy={y - 4.6 * k} r={2.4 * k} fill={sc} stroke="#0c1830" strokeWidth={0.6 * k} />
              {showNames && (
                <text
                  x={labelRight ? x + 8 * k : x - 8 * k}
                  y={y + 3 * k}
                  textAnchor={labelRight ? 'start' : 'end'}
                  fontSize={8.5 * k}
                  fontWeight="600"
                  fill="#eaf2ff"
                  style={{ pointerEvents: 'none' }}
                >
                  {nodeDisplayName(node)}
                </text>
              )}
            </g>
          )
        })}
      </svg>

      {/* zoom controls (top-left, so the region-detail overlay on the right
          never covers them) */}
      <div className="absolute left-3 top-3 flex flex-col gap-1">
        <MapBtn label="Zoom in" onClick={() => zoomByCentre(0.7)}>
          <ZoomIn className="size-4" />
        </MapBtn>
        <MapBtn label="Zoom out" onClick={() => zoomByCentre(1.4)}>
          <ZoomOut className="size-4" />
        </MapBtn>
        <MapBtn label="Reset view" onClick={() => setVb(FULL_VB)} disabled={!zoomed}>
          <Maximize2 className="size-4" />
        </MapBtn>
      </div>
      <div className="pointer-events-none absolute bottom-2 left-3 text-[0.65rem] text-white/45">
        scroll to zoom · drag to pan
      </div>
    </div>
  )
}

function MapBtn({
  children,
  label,
  onClick,
  disabled,
}: {
  children: React.ReactNode
  label: string
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      disabled={disabled}
      className="flex size-8 items-center justify-center rounded-md border border-white/15 bg-black/40 text-white/80 backdrop-blur transition-colors hover:bg-black/60 hover:text-white disabled:cursor-not-allowed disabled:opacity-40"
    >
      {children}
    </button>
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
      <span className="inline-flex items-center gap-1.5">
        <span className="inline-block w-5 border-t border-dashed" style={{ borderColor: '#a78bfa' }} />
        registry link
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
        {nodeDisplayName(node)}
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

// shortImage strips the registry host from an image ref, keeping repo:tag so
// the chip shows something readable (e.g. "shophun-backend:a1b2c3d").
function shortImage(ref?: string): string {
  if (!ref) return ''
  const parts = ref.split('/')
  return parts[parts.length - 1]
}

function AppChip({ app }: { app: TopoApp }) {
  const status = topoStatusToKind(app.status)
  const img = shortImage(app.image_ref)
  return (
    <div className="mt-2 rounded-md bg-muted/40 px-2 py-1.5">
      <div className="flex items-center gap-2">
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
      {(img || app.port) && (
        <div className="mt-1 flex items-center gap-2 pl-4 font-mono text-[0.6rem] text-muted-foreground">
          {img && <span className="truncate">{img}</span>}
          {app.port ? <span className="shrink-0">:{app.port}</span> : null}
        </div>
      )}
    </div>
  )
}
