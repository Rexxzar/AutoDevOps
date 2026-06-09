import { cn } from '@/lib/utils'

export function Badge({
  className,
  variant = 'default',
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & {
  variant?: 'default' | 'outline' | 'mono'
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium',
        variant === 'default' && 'bg-secondary text-secondary-foreground',
        variant === 'outline' && 'border border-border text-muted-foreground',
        variant === 'mono' &&
          'border border-border bg-muted/50 font-mono text-[0.7rem] text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}
