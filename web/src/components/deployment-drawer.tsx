import { useEffect } from 'react'
import {
  X,
  GitBranch,
  GitCommit,
  ExternalLink,
  ShieldCheck,
  ShieldAlert,
  Server,
  Layers,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { StatusBadge } from '@/components/ui/status'
import { Pipeline } from '@/components/pipeline'
import { ProviderTag, targetLabel, frameworkLabel } from '@/components/meta-tags'
import { cn } from '@/lib/utils'
import type { Deployment } from '@/lib/mock-data'

const severityClass: Record<string, string> = {
  critical: 'text-critical border-critical/25 bg-critical/10',
  high: 'text-warning border-warning/25 bg-warning/10',
  medium: 'text-info border-info/25 bg-info/10',
  low: 'text-muted-foreground border-border bg-muted/50',
}

export function DeploymentDrawer({
  deployment,
  onClose,
}: {
  deployment: Deployment | null
  onClose: () => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  if (!deployment) return null
  const d = deployment

  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <div
        className="absolute inset-0 bg-background/60 backdrop-blur-sm"
        onClick={onClose}
        aria-hidden
      />
      <div className="stream-in relative flex h-full w-full max-w-lg flex-col border-l border-border bg-card shadow-2xl">
        <div className="flex items-center justify-between border-b border-border px-5 py-4">
          <div className="flex items-center gap-2.5">
            <StatusBadge status={d.status} pulse={d.status === 'building'} />
            <h2 className="text-base font-semibold">{d.name}</h2>
          </div>
          <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close">
            <X className="size-4" />
          </Button>
        </div>

        <div className="flex-1 space-y-6 overflow-y-auto p-5">
          {/* Meta grid */}
          <div className="grid grid-cols-2 gap-3">
            <MetaItem icon={Server} label="Target">
              {targetLabel[d.target] ?? d.target}
              <ProviderTag provider={d.provider} className="ml-1" />
            </MetaItem>
            <MetaItem icon={Layers} label="Framework">
              {frameworkLabel[d.framework] ?? d.framework}
            </MetaItem>
            <MetaItem icon={GitBranch} label="Branch">
              <span className="font-mono text-xs">{d.branch}</span>
            </MetaItem>
            <MetaItem icon={GitCommit} label="Commit">
              <span className="font-mono text-xs">{d.commit || '—'}</span>
            </MetaItem>
          </div>

          {d.url && (
            <a
              href={`https://${d.url}`}
              target="_blank"
              rel="noreferrer"
              className="flex items-center gap-2 rounded-lg border border-border bg-muted/40 px-3 py-2.5 text-sm transition-colors hover:border-ring/40"
            >
              <span className="size-2 rounded-full bg-success" />
              <span className="flex-1 truncate font-mono text-xs">{d.url}</span>
              <ExternalLink className="size-3.5 text-muted-foreground" />
            </a>
          )}

          {/* Pipeline */}
          {d.stages.length > 0 && (
            <Section title="Pipeline">
              <div className="overflow-x-auto rounded-lg border border-border bg-muted/20 px-4 py-5">
                <Pipeline stages={d.stages} />
              </div>
            </Section>
          )}

          {/* Replicas */}
          <Section title="Replicas">
            <div className="flex items-center gap-3 rounded-lg border border-border px-4 py-3">
              <div className="flex flex-1 items-center gap-1.5">
                {Array.from({ length: Math.max(d.replicas.desired, 1) }).map((_, i) => (
                  <span
                    key={i}
                    className={cn(
                      'h-6 flex-1 rounded',
                      i < d.replicas.ready ? 'bg-success/60' : 'bg-muted',
                    )}
                  />
                ))}
              </div>
              <span className="font-mono text-sm">
                {d.replicas.ready}/{d.replicas.desired}
              </span>
            </div>
          </Section>

          {/* Compliance gate */}
          <Section title="Compliance gate">
            <div
              className={cn(
                'flex items-center gap-2.5 rounded-lg border px-4 py-3',
                d.complianceGate === 'passed'
                  ? 'border-success/25 bg-success/8'
                  : 'border-critical/25 bg-critical/8',
              )}
            >
              {d.complianceGate === 'passed' ? (
                <ShieldCheck className="size-4 text-success" />
              ) : (
                <ShieldAlert className="size-4 text-critical" />
              )}
              <span className="text-sm font-medium">
                {d.complianceGate === 'passed'
                  ? 'All scanners passed severity threshold'
                  : 'Gate blocked — findings exceed threshold'}
              </span>
            </div>

            {d.findings.length > 0 && (
              <ul className="mt-2 space-y-2">
                {d.findings.map((f, i) => (
                  <li
                    key={i}
                    className="rounded-lg border border-border bg-muted/20 p-3"
                  >
                    <div className="flex items-center gap-2">
                      <span
                        className={cn(
                          'rounded border px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase',
                          severityClass[f.severity],
                        )}
                      >
                        {f.severity}
                      </span>
                      <Badge variant="mono">{f.scanner}</Badge>
                      <span className="ml-auto font-mono text-[0.65rem] text-muted-foreground">
                        {f.rule}
                      </span>
                    </div>
                    <p className="mt-1.5 text-sm">{f.message}</p>
                    <p className="mt-0.5 font-mono text-xs text-muted-foreground">
                      {f.location}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </Section>
        </div>

        <div className="flex items-center gap-2 border-t border-border p-4">
          <Button variant="outline" size="sm" className="flex-1">
            View logs
          </Button>
          <Button variant="outline" size="sm" className="flex-1">
            Redeploy
          </Button>
          <Button variant="destructive" size="sm" className="flex-1">
            Destroy
          </Button>
        </div>
      </div>
    </div>
  )
}

function Section({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div>
      <h3 className="mb-2 text-[0.7rem] font-semibold uppercase tracking-wider text-muted-foreground">
        {title}
      </h3>
      {children}
    </div>
  )
}

function MetaItem({
  icon: Icon,
  label,
  children,
}: {
  icon: typeof Server
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="rounded-lg border border-border bg-muted/20 px-3 py-2.5">
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Icon className="size-3.5" />
        <span className="text-[0.7rem] font-medium uppercase tracking-wide">
          {label}
        </span>
      </div>
      <div className="mt-1 flex items-center text-sm font-medium">{children}</div>
    </div>
  )
}
