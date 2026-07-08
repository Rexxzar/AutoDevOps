import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { RefreshCw, Search, ScrollText, Pause, Play } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import { inputCls } from '@/components/deploy-form-fields'

interface LogEntry {
  id: number
  level: string
  source: string
  message: string
  attrs: Record<string, unknown>
  created_at: string
}

const levels = ['ALL', 'ERROR', 'WARN', 'INFO', 'DEBUG'] as const
type Level = typeof levels[number]

const levelStyle: Record<string, string> = {
  ERROR: 'text-critical border-critical/25 bg-critical/10',
  WARN: 'text-warning border-warning/25 bg-warning/10',
  INFO: 'text-info border-info/25 bg-info/10',
  DEBUG: 'text-muted-foreground border-border bg-muted/40',
}

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function attrsLine(attrs: Record<string, unknown>): string {
  const keys = Object.keys(attrs ?? {})
  if (keys.length === 0) return ''
  return keys.map((k) => `${k}=${typeof attrs[k] === 'object' ? JSON.stringify(attrs[k]) : String(attrs[k])}`).join('  ')
}

export function LogsPage() {
  const [level, setLevel] = useState<Level>('ALL')
  const [source, setSource] = useState('')
  const [search, setSearch] = useState('')
  const [live, setLive] = useState(true)

  const { data: sources = [] } = useQuery({
    queryKey: ['log-sources'],
    queryFn: () => apiFetch<string[]>('/logs/sources'),
    refetchInterval: 30_000,
  })

  const params = new URLSearchParams()
  if (level !== 'ALL') params.set('level', level)
  if (source) params.set('source', source)
  if (search) params.set('q', search)
  params.set('limit', '300')

  const { data: logs = [], isLoading, refetch, isFetching } = useQuery({
    queryKey: ['logs', level, source, search],
    queryFn: () => apiFetch<LogEntry[]>(`/logs?${params.toString()}`),
    refetchInterval: live ? 4_000 : false,
  })

  return (
    <div>
      <PageHeader
        title="Logs"
        description="System log, every event under the hood, streamed from the control plane with filters."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => setLive((v) => !v)}>
          {live ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
          {live ? 'Live' : 'Paused'}
        </Button>
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
      </PageHeader>

      <div className="space-y-4 p-4 lg:p-6">
        {/* Filters */}
        <div className="flex flex-wrap items-center gap-2">
          {levels.map((l) => (
            <button
              key={l}
              onClick={() => setLevel(l)}
              className={cn(
                'rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                level === l
                  ? 'border-ring/40 bg-accent text-foreground'
                  : 'border-border text-muted-foreground hover:bg-accent/60',
              )}
            >
              {l}
            </button>
          ))}

          <select value={source} onChange={(e) => setSource(e.target.value)} className={cn(inputCls, 'h-8 w-40 text-xs')}>
            <option value="">All sources</option>
            {sources.map((s) => (
              <option key={s} value={s}>{s}</option>
            ))}
          </select>

          <div className="relative ml-auto">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search messages…"
              className={cn(inputCls, 'h-8 w-56 pl-8 text-xs')}
            />
          </div>
        </div>

        {/* Log table */}
        <Card className="overflow-hidden">
          {isLoading && <div className="py-12 text-center text-sm text-muted-foreground">Loading logs…</div>}
          {!isLoading && logs.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-16 text-center">
              <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                <ScrollText className="size-4 text-muted-foreground" />
              </div>
              <p className="max-w-xs text-sm text-muted-foreground text-pretty">
                No logs match the current filters.
              </p>
            </div>
          )}
          {logs.length > 0 && (
            <div className="divide-y divide-border font-mono text-xs">
              {logs.map((l) => (
                <div key={l.id} className="flex items-start gap-3 px-4 py-2 hover:bg-accent/30">
                  <span className="shrink-0 text-muted-foreground/70">{fmtTime(l.created_at)}</span>
                  <span className={cn('w-14 shrink-0 rounded border px-1 text-center text-[0.6rem] font-semibold uppercase', levelStyle[l.level] ?? levelStyle.DEBUG)}>
                    {l.level}
                  </span>
                  <span className="w-20 shrink-0 truncate text-primary/80" title={l.source}>{l.source}</span>
                  <span className="min-w-0 flex-1 break-words">
                    <span className="text-foreground">{l.message}</span>
                    {attrsLine(l.attrs) && <span className="ml-2 text-muted-foreground">{attrsLine(l.attrs)}</span>}
                  </span>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}
