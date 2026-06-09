import { Activity, RotateCcw, ShieldAlert, CheckCircle2, Info } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { HealEvent } from '@/lib/mock-data'

const toneStyles: Record<HealEvent['tone'], { icon: typeof Info; text: string }> = {
  success: { icon: CheckCircle2, text: 'text-success' },
  warning: { icon: ShieldAlert, text: 'text-warning' },
  critical: { icon: ShieldAlert, text: 'text-critical' },
  info: { icon: Info, text: 'text-info' },
}

interface Props {
  events: HealEvent[]
  live: boolean
  onToggleLive: () => void
}

export function HealEventStream({ events, live, onToggleLive }: Props) {
  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <div className="flex items-center gap-2">
          <Activity className="size-4 text-primary" />
          <h3 className="text-sm font-semibold">Self-Healing Stream</h3>
        </div>
        <button
          onClick={onToggleLive}
          className={cn(
            'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors',
            live
              ? 'border-success/30 bg-success/10 text-success'
              : 'border-border bg-muted text-muted-foreground',
          )}
        >
          <span className={cn('size-1.5 rounded-full', live ? 'bg-success pulse-dot' : 'bg-muted-foreground')} />
          {live ? 'Live' : 'Paused'}
        </button>
      </div>

      <div className="flex-1 overflow-y-auto">
        <ul className="divide-y divide-border/60">
          {events.map((e) => {
            const tone = toneStyles[e.tone]
            const Icon = tone.icon
            return (
              <li key={e.id} className="stream-in flex gap-3 px-4 py-3">
                <Icon className={cn('mt-0.5 size-4 shrink-0', tone.text)} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className={cn('truncate text-sm font-medium', tone.text)}>{e.action}</span>
                    {e.backoff && (
                      <span className="inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                        <RotateCcw className="size-2.5" />
                        {e.backoff}
                      </span>
                    )}
                  </div>
                  <p className="mt-0.5 text-xs text-muted-foreground">{e.detail}</p>
                  <div className="mt-1 flex items-center gap-2 font-mono text-[11px] text-muted-foreground/70">
                    <span>{e.ts}</span>
                    <span className="text-border">|</span>
                    <span className="truncate">{e.cluster}</span>
                    <span className="text-border">/</span>
                    <span className="truncate">{e.pod}</span>
                  </div>
                </div>
              </li>
            )
          })}
        </ul>
      </div>
    </div>
  )
}
