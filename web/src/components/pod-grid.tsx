import { cn } from '@/lib/utils'
import type { Pod } from '@/lib/types'
import type { StatusKind } from '@/components/ui/status'

const stateColor: Record<string, string> = {
  running: 'bg-success',
  healthy: 'bg-success',
  ready: 'bg-success',
  pending: 'bg-info animate-pulse',
  building: 'bg-info animate-pulse',
  warning: 'bg-warning animate-pulse',
  degraded: 'bg-warning',
  critical: 'bg-critical animate-pulse',
  failed: 'bg-critical',
  queued: 'bg-muted-foreground/50',
  idle: 'bg-muted-foreground/50',
  unknown: 'bg-muted-foreground/50',
}

export function PodGrid({ pods, onSelect }: { pods: Pod[]; onSelect?: (pod: Pod) => void }) {
  if (!pods.length) {
    return (
      <p className="text-xs text-muted-foreground">No pod data yet, waiting for health events.</p>
    )
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {pods.map((pod) => (
        <button
          key={pod.name}
          onClick={() => onSelect?.(pod)}
          title={`${pod.name} · ${pod.status}${pod.restarts ? ` · ${pod.restarts} restarts` : ''}`}
          className={cn(
            'h-6 w-6 rounded-[4px] border border-border/60 transition-transform hover:scale-110 hover:ring-2 hover:ring-ring/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring',
            stateColor[pod.status] ?? 'bg-muted-foreground/50',
          )}
          aria-label={`Pod ${pod.name}, status ${pod.status}`}
        />
      ))}
    </div>
  )
}

export function PodLegend() {
  const items: { label: string; status: StatusKind }[] = [
    { label: 'Running', status: 'running' },
    { label: 'Pending', status: 'pending' },
    { label: 'Warning', status: 'warning' },
    { label: 'Critical', status: 'critical' },
    { label: 'Idle', status: 'idle' },
  ]
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
      {items.map((i) => (
        <div key={i.label} className="flex items-center gap-1.5">
          <span className={cn('h-2.5 w-2.5 rounded-[3px]', stateColor[i.status])} />
          <span className="text-xs text-muted-foreground">{i.label}</span>
        </div>
      ))}
    </div>
  )
}
