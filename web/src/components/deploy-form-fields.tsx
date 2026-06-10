import { cn } from '@/lib/utils'

export const inputCls =
  'h-10 w-full rounded-lg border border-border bg-card px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/30 transition-colors'

export const selectCls = cn(inputCls, 'cursor-pointer')

export function TargetPill({ label, sub, active, onClick }: {
  label: string; sub?: string; active: boolean; onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex flex-1 flex-col items-center justify-center gap-0.5 rounded-lg border py-3 text-sm font-medium transition-colors',
        active ? 'border-ring/50 bg-accent text-foreground shadow-sm' : 'border-border text-muted-foreground hover:bg-accent/60',
      )}
    >
      {label}
      {sub && <span className="text-[0.65rem] font-normal text-muted-foreground">{sub}</span>}
    </button>
  )
}

export function FormField({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between">
        <label className="text-xs font-medium text-muted-foreground">{label}</label>
        {hint && <span className="text-[0.65rem] text-muted-foreground/60">{hint}</span>}
      </div>
      {children}
    </div>
  )
}
