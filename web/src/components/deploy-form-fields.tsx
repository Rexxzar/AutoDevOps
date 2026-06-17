import { useState, useRef } from 'react'
import { Globe, Loader2, CheckCircle2, AlertCircle, RefreshCw } from 'lucide-react'
import { apiFetch } from '@/lib/api'
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

// ─── Domain field with inline DNS checker ────────────────────────
type DNSStatus = 'idle' | 'checking' | 'ok' | 'fail' | 'error'

export function DomainField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [status, setStatus] = useState<DNSStatus>('idle')
  const [info, setInfo] = useState<{ resolved_ips?: string[]; server_ip?: string; error?: string } | null>(null)
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null)

  async function check(domain: string) {
    if (!domain) { setStatus('idle'); setInfo(null); return }
    setStatus('checking'); setInfo(null)
    try {
      const res = await apiFetch<{ ok: boolean; resolved_ips?: string[]; server_ip?: string; error?: string }>(
        `/dns/check?domain=${encodeURIComponent(domain)}`
      )
      setStatus(res.ok ? 'ok' : 'fail')
      setInfo({ resolved_ips: res.resolved_ips, server_ip: res.server_ip, error: res.error })
    } catch {
      setStatus('error')
      setInfo(null)
    }
  }

  function handleChange(v: string) {
    onChange(v)
    setStatus('idle'); setInfo(null)
    if (debounce.current) clearTimeout(debounce.current)
    if (v.includes('.')) {
      debounce.current = setTimeout(() => check(v), 800)
    }
  }

  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between">
        <label className="text-xs font-medium text-muted-foreground">
          Custom domain <span className="text-muted-foreground/60">(optional)</span>
        </label>
        {status !== 'idle' && (
          <button type="button" onClick={() => check(value)} className="flex items-center gap-1 text-[0.65rem] text-muted-foreground hover:text-foreground">
            <RefreshCw className="size-2.5" /> re-check
          </button>
        )}
      </div>

      <div className="relative">
        <Globe className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <input
          value={value}
          onChange={e => handleChange(e.target.value)}
          placeholder="app.example.com"
          className={cn(inputCls, 'pl-9 pr-9')}
        />
        <div className="absolute right-3 top-1/2 -translate-y-1/2">
          {status === 'checking' && <Loader2 className="size-4 animate-spin text-muted-foreground" />}
          {status === 'ok'       && <CheckCircle2 className="size-4 text-success" />}
          {status === 'fail'     && <AlertCircle className="size-4 text-warning" />}
          {status === 'error'    && <AlertCircle className="size-4 text-critical" />}
        </div>
      </div>

      {status === 'ok' && (
        <p className="flex items-center gap-1.5 text-xs text-success">
          <CheckCircle2 className="size-3 shrink-0" />
          Domain resolves to this server — TLS cert will be issued automatically
        </p>
      )}
      {status === 'fail' && info && (
        <div className="rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 space-y-1">
          <p className="text-xs font-medium text-warning">Domain not pointing to this server yet</p>
          <p className="text-xs text-muted-foreground">
            Add an <span className="font-mono font-medium text-foreground">A record</span> pointing to{' '}
            <span className="font-mono font-medium text-foreground">{info.server_ip}</span>
          </p>
          {info.resolved_ips && info.resolved_ips.length > 0 && (
            <p className="text-xs text-muted-foreground">
              Currently resolves to: <span className="font-mono">{info.resolved_ips.join(', ')}</span>
            </p>
          )}
          <p className="text-[0.65rem] text-muted-foreground/70">DNS changes can take up to 24h. You can still proceed and set up DNS later.</p>
        </div>
      )}
      {status === 'error' && info?.error && (
        <p className="text-xs text-critical">{info.error}</p>
      )}
    </div>
  )
}
