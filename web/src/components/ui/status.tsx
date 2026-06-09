import { cn } from '@/lib/utils'

export type StatusKind =
  | 'healthy'
  | 'running'
  | 'ready'
  | 'warning'
  | 'degraded'
  | 'critical'
  | 'failed'
  | 'pending'
  | 'building'
  | 'queued'
  | 'idle'
  | 'unknown'

const statusMeta: Record<
  StatusKind,
  { label: string; tone: 'success' | 'warning' | 'critical' | 'info' | 'muted' }
> = {
  healthy: { label: 'Healthy', tone: 'success' },
  running: { label: 'Running', tone: 'success' },
  ready: { label: 'Ready', tone: 'success' },
  warning: { label: 'Warning', tone: 'warning' },
  degraded: { label: 'Degraded', tone: 'warning' },
  critical: { label: 'Critical', tone: 'critical' },
  failed: { label: 'Failed', tone: 'critical' },
  pending: { label: 'Pending', tone: 'info' },
  building: { label: 'Building', tone: 'info' },
  queued: { label: 'Queued', tone: 'muted' },
  idle: { label: 'Idle', tone: 'muted' },
  unknown: { label: 'Unknown', tone: 'muted' },
}

const toneDot: Record<string, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  critical: 'bg-critical',
  info: 'bg-info',
  muted: 'bg-muted-foreground',
}

const toneBadge: Record<string, string> = {
  success: 'bg-success/12 text-success border-success/20',
  warning: 'bg-warning/12 text-warning border-warning/20',
  critical: 'bg-critical/12 text-critical border-critical/20',
  info: 'bg-info/12 text-info border-info/20',
  muted: 'bg-muted text-muted-foreground border-border',
}

export function StatusDot({
  status,
  pulse,
  className,
}: {
  status: StatusKind
  pulse?: boolean
  className?: string
}) {
  const tone = statusMeta[status]?.tone ?? 'muted'
  return (
    <span
      className={cn(
        'inline-block size-2 shrink-0 rounded-full',
        toneDot[tone],
        pulse && tone !== 'muted' && 'pulse-dot',
        className,
      )}
      style={
        pulse
          ? ({ '--ring-pulse-color': `var(--${tone})` } as React.CSSProperties)
          : undefined
      }
    />
  )
}

export function StatusBadge({
  status,
  label,
  pulse,
  className,
}: {
  status: StatusKind
  label?: string
  pulse?: boolean
  className?: string
}) {
  const meta = statusMeta[status] ?? statusMeta.unknown
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium',
        toneBadge[meta.tone],
        className,
      )}
    >
      <StatusDot status={status} pulse={pulse} />
      {label ?? meta.label}
    </span>
  )
}
