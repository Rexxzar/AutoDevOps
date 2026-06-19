import { Fragment } from 'react'
import { Cloud, Play, Trash2, ChevronDown, ChevronRight, Loader2, X, AlertTriangle } from 'lucide-react'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ProviderTag } from '@/components/meta-tags'
import { StatusBadge, type StatusKind } from '@/components/ui/status'
import type { Provider } from '@/lib/types'
import {
  type ProvisionedResource,
  type TerraformRun,
  provisionedTypeLabel,
  provisionedStatusMeta,
  formatRelativeTime,
  friendlyTerraformError,
} from '@/lib/resources'

export interface ProvisionedTableProps {
  items: ProvisionedResource[]
  loading: boolean
  emptyMessage: string
  runs: TerraformRun[]
  expanded: string | null
  setExpanded: (id: string | null) => void
  actingOn: Record<string, 'apply' | 'destroy'>
  onApply: (id: string) => void
  onDestroy: (id: string) => void
  onDismiss: (id: string) => void
  navigate: (path: string) => void
  showRegion?: boolean
  detailBasePath?: string
}

export function ProvisionedTable({
  items, loading, emptyMessage, runs, expanded, setExpanded, actingOn,
  onApply, onDestroy, onDismiss, navigate, showRegion = true, detailBasePath = '/resources',
}: ProvisionedTableProps) {
  const columns = showRegion
    ? ['', 'Provider', 'Name', 'Type', 'Region', 'Status', 'Updated', 'Actions']
    : ['', 'Provider', 'Name', 'Type', 'Status', 'Updated', 'Actions']

  return (
    <Card className="overflow-hidden">
      {loading && (
        <div className="py-12 text-center text-sm text-muted-foreground">Loading resources…</div>
      )}
      {!loading && items.length === 0 && (
        <div className="flex flex-col items-center gap-3 py-16 text-center">
          <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
            <Cloud className="size-4 text-muted-foreground" />
          </div>
          <p className="text-sm text-muted-foreground max-w-xs text-pretty">{emptyMessage}</p>
        </div>
      )}
      {items.length > 0 && (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border">
              {columns.map((h) => (
                <th key={h} className="px-4 py-3 text-left text-[0.7rem] font-medium uppercase tracking-wide text-muted-foreground">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {items.map((res) => {
              const meta = provisionedStatusMeta[res.status] ?? { kind: 'unknown' as StatusKind, label: res.status }
              const action = actingOn[res.id]
              const isTransitional = res.status === 'pending' || res.status === 'applying' || res.status === 'destroying'
              const latestRun = runs.find((r) => r.resource_id === res.id)
              const isExpanded = expanded === res.id
              return (
                <Fragment key={res.id}>
                  <tr className="hover:bg-accent/30 transition-colors">
                    <td className="px-2 py-3 w-8">
                      <button
                        onClick={() => setExpanded(isExpanded ? null : res.id)}
                        className="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label="Toggle plan output"
                      >
                        {isExpanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                      </button>
                    </td>
                    <td className="px-4 py-3">
                      <ProviderTag provider={res.provider as Provider} />
                    </td>
                    <td className="px-4 py-3 font-medium">
                      <button
                        onClick={() => navigate(`${detailBasePath}/${res.id}`)}
                        className="hover:underline hover:text-primary transition-colors"
                      >
                        {res.name}
                      </button>
                    </td>
                    <td className="px-4 py-3 text-xs text-muted-foreground">
                      {provisionedTypeLabel[res.provider]?.[res.resource_type] ?? res.resource_type}
                    </td>
                    {showRegion && (
                      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{res.region}</td>
                    )}
                    <td className="px-4 py-3">
                      <StatusBadge status={meta.kind} label={meta.label} pulse={meta.pulse} />
                    </td>
                    <td className="px-4 py-3 text-xs text-muted-foreground" title={new Date(res.updated_at).toLocaleString()}>
                      {formatRelativeTime(res.updated_at)}
                    </td>
                    <td className="px-4 py-3">
                      {isTransitional ? (
                        <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                          <Loader2 className="size-3.5 animate-spin" />
                          {meta.label}…
                        </span>
                      ) : (
                        <div className="flex items-center gap-2">
                          {res.status === 'planned' && (
                            <Button
                              size="sm" variant="outline" className="gap-1.5 h-7 px-2 text-xs"
                              disabled={!!action}
                              onClick={() => onApply(res.id)}
                            >
                              {action === 'apply' ? <Loader2 className="size-3 animate-spin" /> : <Play className="size-3" />}
                              Apply
                            </Button>
                          )}
                          {(res.status === 'applied' || res.status === 'planned' || res.status === 'failed') && (
                            <Button
                              size="sm" variant="destructive" className="gap-1.5 h-7 px-2 text-xs"
                              disabled={!!action}
                              onClick={() => onDestroy(res.id)}
                            >
                              {action === 'destroy' ? <Loader2 className="size-3 animate-spin" /> : <Trash2 className="size-3" />}
                              Destroy
                            </Button>
                          )}
                          {res.status === 'destroyed' && (
                            <Button
                              size="sm" variant="ghost" className="gap-1.5 h-7 px-2 text-xs text-muted-foreground"
                              onClick={() => onDismiss(res.id)}
                            >
                              <X className="size-3" />
                              Dismiss
                            </Button>
                          )}
                        </div>
                      )}
                    </td>
                  </tr>
                  {isExpanded && (
                    <tr>
                      <td colSpan={columns.length} className="bg-background px-4 py-3">
                        {latestRun ? (
                          <div className="space-y-2">
                            <p className="text-xs text-muted-foreground">
                              Last run: <span className="font-mono">{latestRun.action}</span> ·{' '}
                              <span className="font-mono">{latestRun.status}</span> ·{' '}
                              {new Date(latestRun.created_at).toLocaleString()}
                            </p>
                            {latestRun.status === 'failed' && friendlyTerraformError(latestRun.plan_output) && (
                              <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-sm text-warning">
                                <AlertTriangle className="size-4 shrink-0 mt-0.5" />
                                <span>{friendlyTerraformError(latestRun.plan_output)}</span>
                              </div>
                            )}
                            <pre className="max-h-80 overflow-auto rounded-lg border border-border bg-card p-3 text-xs whitespace-pre-wrap">
                              {latestRun.plan_output || 'No output recorded.'}
                            </pre>
                          </div>
                        ) : (
                          <p className="text-xs text-muted-foreground">No terraform runs yet for this resource.</p>
                        )}
                      </td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      )}
    </Card>
  )
}
