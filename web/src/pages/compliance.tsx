import { useState, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ShieldCheck, ShieldAlert, RefreshCw, Filter } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'

export interface ComplianceFinding {
  id: string
  deployment_id: string
  deployment_name: string
  scanner: 'hadolint' | 'trivy' | 'tfsec' | 'gitleaks'
  severity: 'critical' | 'high' | 'medium' | 'low' | 'info'
  rule_id: string
  message: string
  file_path: string
  line_number: number
  created_at: string
}

const severityOrder = ['critical', 'high', 'medium', 'low', 'info'] as const
type Severity = typeof severityOrder[number]

const severityStyle: Record<Severity, string> = {
  critical: 'text-critical border-critical/25 bg-critical/10',
  high:     'text-warning border-warning/25 bg-warning/10',
  medium:   'text-info border-info/25 bg-info/10',
  low:      'text-muted-foreground border-border bg-muted/50',
  info:     'text-muted-foreground border-border bg-muted/30',
}

const scanners = ['all', 'hadolint', 'trivy', 'tfsec', 'gitleaks'] as const

function SummaryPill({ severity, count }: { severity: Severity; count: number }) {
  return (
    <div className={cn('flex items-center gap-2 rounded-lg border px-3 py-2', severityStyle[severity])}>
      <span className="text-[0.7rem] font-semibold uppercase tracking-wide">{severity}</span>
      <span className="font-mono text-lg font-bold">{count}</span>
    </div>
  )
}

export function CompliancePage() {
  const { token } = useAuth()
  const [findings, setFindings] = useState<ComplianceFinding[]>([])
  const [scannerFilter, setScannerFilter] = useState<typeof scanners[number]>('all')
  const [severityFilter, setSeverityFilter] = useState<Severity | 'all'>('all')

  const { isLoading, refetch, isFetching } = useQuery({
    queryKey: ['compliance-findings'],
    queryFn: async () => {
      const data = await apiFetch<ComplianceFinding[]>('/compliance/findings')
      setFindings(data)
      return data
    },
    refetchInterval: 15_000,
  })

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type !== 'compliance.finding') return
    const f = data as ComplianceFinding
    setFindings((prev) => [f, ...prev].slice(0, 500))
  }, []))

  const counts = Object.fromEntries(
    severityOrder.map((s) => [s, findings.filter((f) => f.severity === s).length])
  ) as Record<Severity, number>

  const filtered = findings.filter((f) => {
    if (scannerFilter !== 'all' && f.scanner !== scannerFilter) return false
    if (severityFilter !== 'all' && f.severity !== severityFilter) return false
    return true
  })

  const totalActionable = (counts.critical ?? 0) + (counts.high ?? 0)

  return (
    <div>
      <PageHeader
        title="Compliance"
        description="Hadolint, Trivy, tfsec and GitLeaks findings with severity-threshold gating."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-4">
        {/* Summary */}
        <div className="flex flex-wrap gap-3">
          {severityOrder.map((s) => (
            <button key={s} onClick={() => setSeverityFilter(severityFilter === s ? 'all' : s)}>
              <SummaryPill severity={s} count={counts[s]} />
            </button>
          ))}
          {totalActionable > 0 && (
            <div className="ml-auto flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2">
              <ShieldAlert className="size-4 text-critical" />
              <span className="text-sm font-medium text-critical">{totalActionable} actionable</span>
            </div>
          )}
          {findings.length > 0 && totalActionable === 0 && (
            <div className="ml-auto flex items-center gap-2 rounded-lg border border-success/25 bg-success/8 px-3 py-2">
              <ShieldCheck className="size-4 text-success" />
              <span className="text-sm font-medium text-success">No critical / high findings</span>
            </div>
          )}
        </div>

        {/* Scanner filter */}
        <div className="flex items-center gap-2 overflow-x-auto">
          <Filter className="size-4 shrink-0 text-muted-foreground" />
          {scanners.map((sc) => (
            <button
              key={sc}
              onClick={() => setScannerFilter(sc)}
              className={cn(
                'flex shrink-0 items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                scannerFilter === sc
                  ? 'border-ring/40 bg-accent text-foreground'
                  : 'border-border text-muted-foreground hover:bg-accent/60',
              )}
            >
              {sc}
              <span className="rounded bg-muted px-1 font-mono text-[0.65rem]">
                {sc === 'all' ? findings.length : findings.filter((f) => f.scanner === sc).length}
              </span>
            </button>
          ))}
        </div>

        {/* Findings table */}
        <Card className="overflow-hidden">
          {isLoading && (
            <div className="py-12 text-center text-sm text-muted-foreground">Loading findings…</div>
          )}
          {!isLoading && filtered.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-16 text-center">
              <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                <ShieldCheck className="size-4 text-muted-foreground" />
              </div>
              <p className="text-sm text-muted-foreground max-w-xs text-pretty">
                {findings.length === 0
                  ? 'No findings yet. Trigger a deployment to run the compliance scanners.'
                  : 'No findings match the current filters.'}
              </p>
            </div>
          )}
          {filtered.length > 0 && (
            <div className="divide-y divide-border">
              {filtered.map((f) => (
                <div key={f.id} className="flex flex-col gap-1.5 px-5 py-3.5 hover:bg-accent/30 transition-colors sm:flex-row sm:items-start sm:gap-4">
                  <div className="flex shrink-0 items-center gap-2">
                    <span className={cn('rounded border px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase', severityStyle[f.severity])}>
                      {f.severity}
                    </span>
                    <Badge variant="mono">{f.scanner}</Badge>
                  </div>

                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium">{f.message}</p>
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5">
                      {f.file_path && (
                        <span className="font-mono text-xs text-muted-foreground">
                          {f.file_path}{f.line_number ? `:${f.line_number}` : ''}
                        </span>
                      )}
                      <span className="text-xs text-muted-foreground">
                        deployment: <span className="text-foreground">{f.deployment_name}</span>
                      </span>
                    </div>
                  </div>

                  <span className="shrink-0 font-mono text-[0.65rem] text-muted-foreground">{f.rule_id}</span>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}
