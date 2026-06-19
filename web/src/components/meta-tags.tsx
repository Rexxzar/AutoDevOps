import { cn } from '@/lib/utils'
import type { Provider, Target, Framework } from '@/lib/types'

const providerMeta: Record<Provider, { label: string; className: string }> = {
  aws: { label: 'AWS', className: 'text-aws border-aws/25 bg-aws/10' },
  azure: { label: 'Azure', className: 'text-azure border-azure/25 bg-azure/10' },
  kind: { label: 'kind', className: 'text-muted-foreground border-border bg-muted/50' },
  local: { label: 'Local', className: 'text-muted-foreground border-border bg-muted/50' },
}

export const targetLabel: Record<Target, string> = {
  kubernetes: 'Kubernetes',
  docker: 'Docker',
  vm: 'VM',
}

export const frameworkLabel: Record<string, string> = {
  node: 'Node.js',
  go: 'Go',
  python: 'Python',
  java: 'Java',
  static: 'Static',
  ruby: 'Ruby',
  php: 'PHP',
  unknown: 'Unknown',
}

export function ProviderTag({
  provider,
  className,
}: {
  provider: Provider
  className?: string
}) {
  const meta = providerMeta[provider] ?? { label: provider, className: 'text-muted-foreground border-border bg-muted/50' }
  return (
    <span
      className={cn(
        'inline-flex items-center rounded border px-1.5 py-0.5 text-[0.7rem] font-medium',
        meta.className,
        className,
      )}
    >
      {meta.label}
    </span>
  )
}

export function TargetLabel({ target }: { target: Target }) {
  return <span>{targetLabel[target]}</span>
}

export function FrameworkLabel({ framework }: { framework: Framework }) {
  return <span>{frameworkLabel[framework] ?? framework}</span>
}

export { providerMeta }
