import { Check, X, Loader2, Minus } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { PipelineStage } from '@/lib/mock-data'

function fmt(ms?: number) {
  if (!ms) return ''
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

export function Pipeline({
  stages,
  compact,
}: {
  stages: PipelineStage[]
  compact?: boolean
}) {
  return (
    <div className="flex items-center">
      {stages.map((stage, i) => (
        <div key={stage.name} className="flex items-center">
          <div className="flex flex-col items-center gap-1">
            <div
              className={cn(
                'flex items-center justify-center rounded-full border transition-colors',
                compact ? 'size-5' : 'size-6',
                stage.status === 'done' && 'border-success/40 bg-success/15 text-success',
                stage.status === 'active' && 'border-info/40 bg-info/15 text-info',
                stage.status === 'failed' && 'border-critical/40 bg-critical/15 text-critical',
                stage.status === 'pending' && 'border-border bg-muted text-muted-foreground',
                stage.status === 'skipped' && 'border-border bg-transparent text-muted-foreground/50',
              )}
            >
              {stage.status === 'done' && <Check className={compact ? 'size-3' : 'size-3.5'} strokeWidth={3} />}
              {stage.status === 'active' && <Loader2 className={cn(compact ? 'size-3' : 'size-3.5', 'animate-spin')} />}
              {stage.status === 'failed' && <X className={compact ? 'size-3' : 'size-3.5'} strokeWidth={3} />}
              {stage.status === 'pending' && <span className="size-1 rounded-full bg-current" />}
              {stage.status === 'skipped' && <Minus className={compact ? 'size-3' : 'size-3.5'} />}
            </div>
            {!compact && (
              <div className="flex flex-col items-center">
                <span
                  className={cn(
                    'text-[0.7rem] font-medium',
                    stage.status === 'active' ? 'text-foreground' : 'text-muted-foreground',
                  )}
                >
                  {stage.name}
                </span>
                <span className="font-mono text-[0.6rem] text-muted-foreground/70">
                  {fmt(stage.durationMs)}
                </span>
              </div>
            )}
          </div>
          {i < stages.length - 1 && (
            <div
              className={cn(
                'mx-1 h-px',
                compact ? 'w-3' : 'mb-5 w-5',
                stage.status === 'done' ? 'bg-success/40' : 'bg-border',
              )}
            />
          )}
        </div>
      ))}
    </div>
  )
}
