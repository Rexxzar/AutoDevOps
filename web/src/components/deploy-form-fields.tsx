import { useState, useRef, useEffect } from 'react'
import { Globe, Loader2, CheckCircle2, AlertCircle, RefreshCw, Settings2, ChevronDown } from 'lucide-react'
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

// ─── Kubernetes per-app options (local_k8s, git-kind) ────────────────
// Serializes to deployment_apps.k8s_config (JSONB). Every field is optional —
// onChange emits undefined when nothing differs from the platform defaults, so
// no k8s_config is stored and the app behaves exactly as before this feature.
export type K8sConfigValue = {
  replicas?: number
  cpu_request?: string
  cpu_limit?: string
  mem_request?: string
  mem_limit?: string
  hpa?: { min: number; max: number; target_cpu: number }
  liveness?: { path: string; port: number }
  readiness?: { path: string; port: number }
  rolling_update?: { max_surge: string; max_unavailable: string }
}

type Preset = 'default' | 'small' | 'medium' | 'large' | 'custom'

const RESOURCE_PRESETS: Record<Exclude<Preset, 'default' | 'custom'>, {
  cpu_request: string; cpu_limit: string; mem_request: string; mem_limit: string
}> = {
  small:  { cpu_request: '100m', cpu_limit: '250m', mem_request: '128Mi', mem_limit: '256Mi' },
  medium: { cpu_request: '250m', cpu_limit: '500m', mem_request: '256Mi', mem_limit: '512Mi' },
  large:  { cpu_request: '500m', cpu_limit: '1',    mem_request: '512Mi', mem_limit: '1Gi' },
}

const numCls = cn(inputCls, 'h-9')

export function K8sOptions({ onChange }: { onChange: (cfg: K8sConfigValue | undefined) => void }) {
  const [open, setOpen] = useState(false)
  const [replicas, setReplicas] = useState(1)
  const [preset, setPreset] = useState<Preset>('default')
  const [cpuReq, setCpuReq] = useState(''); const [cpuLim, setCpuLim] = useState('')
  const [memReq, setMemReq] = useState(''); const [memLim, setMemLim] = useState('')
  const [hpa, setHpa] = useState(false)
  const [hpaMin, setHpaMin] = useState(2); const [hpaMax, setHpaMax] = useState(5); const [hpaCpu, setHpaCpu] = useState(70)
  const [live, setLive] = useState(false); const [livePath, setLivePath] = useState('/'); const [livePort, setLivePort] = useState(8080)
  const [ready, setReady] = useState(false); const [readyPath, setReadyPath] = useState('/'); const [readyPort, setReadyPort] = useState(8080)
  const [ru, setRu] = useState(false); const [surge, setSurge] = useState('25%'); const [unavail, setUnavail] = useState('25%')

  useEffect(() => {
    const cfg: K8sConfigValue = {}
    if (replicas !== 1 && !hpa) cfg.replicas = replicas
    if (preset === 'custom') {
      if (cpuReq) cfg.cpu_request = cpuReq
      if (cpuLim) cfg.cpu_limit = cpuLim
      if (memReq) cfg.mem_request = memReq
      if (memLim) cfg.mem_limit = memLim
    } else if (preset !== 'default') {
      Object.assign(cfg, RESOURCE_PRESETS[preset])
    }
    if (hpa) cfg.hpa = { min: hpaMin, max: hpaMax, target_cpu: hpaCpu }
    if (live) cfg.liveness = { path: livePath, port: livePort }
    if (ready) cfg.readiness = { path: readyPath, port: readyPort }
    if (ru) cfg.rolling_update = { max_surge: surge, max_unavailable: unavail }
    onChange(Object.keys(cfg).length ? cfg : undefined)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [replicas, preset, cpuReq, cpuLim, memReq, memLim, hpa, hpaMin, hpaMax, hpaCpu, live, livePath, livePort, ready, readyPath, readyPort, ru, surge, unavail])

  return (
    <div className="rounded-lg border border-border">
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="flex w-full items-center justify-between px-3 py-2.5 text-sm font-medium text-foreground"
      >
        <span className="flex items-center gap-2"><Settings2 className="size-4 text-muted-foreground" /> Kubernetes options</span>
        <ChevronDown className={cn('size-4 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>

      {open && (
        <div className="space-y-4 border-t border-border px-3 py-3">
          <FormField label="Replicas" hint={hpa ? 'managed by HPA' : undefined}>
            <input type="number" min={1} value={replicas} disabled={hpa}
              onChange={e => setReplicas(Math.max(1, Number(e.target.value) || 1))}
              className={cn(numCls, hpa && 'opacity-50')} />
          </FormField>

          <FormField label="Resources">
            <div className="grid grid-cols-5 gap-1.5">
              {(['default', 'small', 'medium', 'large', 'custom'] as Preset[]).map(p => (
                <TargetPill key={p} label={p[0].toUpperCase() + p.slice(1)} active={preset === p} onClick={() => setPreset(p)} />
              ))}
            </div>
            {preset === 'custom' && (
              <div className="mt-2 grid grid-cols-2 gap-2">
                <input value={cpuReq} onChange={e => setCpuReq(e.target.value)} placeholder="CPU request (100m)" className={numCls} />
                <input value={cpuLim} onChange={e => setCpuLim(e.target.value)} placeholder="CPU limit (500m)" className={numCls} />
                <input value={memReq} onChange={e => setMemReq(e.target.value)} placeholder="Mem request (128Mi)" className={numCls} />
                <input value={memLim} onChange={e => setMemLim(e.target.value)} placeholder="Mem limit (512Mi)" className={numCls} />
              </div>
            )}
            {preset !== 'default' && preset !== 'custom' && (
              <p className="mt-1.5 text-[0.65rem] text-muted-foreground">
                requests {RESOURCE_PRESETS[preset].cpu_request}/{RESOURCE_PRESETS[preset].mem_request} → limits {RESOURCE_PRESETS[preset].cpu_limit}/{RESOURCE_PRESETS[preset].mem_limit}
              </p>
            )}
          </FormField>

          <label className="flex items-center gap-2 text-sm text-foreground">
            <input type="checkbox" checked={hpa} onChange={e => setHpa(e.target.checked)} />
            Autoscale (HPA) by CPU
          </label>
          {hpa && (
            <div className="grid grid-cols-3 gap-2">
              <FormField label="Min replicas"><input type="number" min={1} value={hpaMin} onChange={e => setHpaMin(Math.max(1, Number(e.target.value) || 1))} className={numCls} /></FormField>
              <FormField label="Max replicas"><input type="number" min={1} value={hpaMax} onChange={e => setHpaMax(Math.max(1, Number(e.target.value) || 1))} className={numCls} /></FormField>
              <FormField label="Target CPU %"><input type="number" min={1} max={100} value={hpaCpu} onChange={e => setHpaCpu(Number(e.target.value) || 70)} className={numCls} /></FormField>
            </div>
          )}

          <label className="flex items-center gap-2 text-sm text-foreground">
            <input type="checkbox" checked={live} onChange={e => setLive(e.target.checked)} />
            Liveness probe (HTTP)
          </label>
          {live && (
            <div className="grid grid-cols-[1fr_auto] gap-2">
              <FormField label="Path"><input value={livePath} onChange={e => setLivePath(e.target.value)} placeholder="/" className={numCls} /></FormField>
              <FormField label="Port"><input type="number" value={livePort} onChange={e => setLivePort(Number(e.target.value) || 8080)} className={cn(numCls, 'w-24')} /></FormField>
            </div>
          )}

          <label className="flex items-center gap-2 text-sm text-foreground">
            <input type="checkbox" checked={ready} onChange={e => setReady(e.target.checked)} />
            Readiness probe (HTTP)
          </label>
          {ready && (
            <div className="grid grid-cols-[1fr_auto] gap-2">
              <FormField label="Path"><input value={readyPath} onChange={e => setReadyPath(e.target.value)} placeholder="/" className={numCls} /></FormField>
              <FormField label="Port"><input type="number" value={readyPort} onChange={e => setReadyPort(Number(e.target.value) || 8080)} className={cn(numCls, 'w-24')} /></FormField>
            </div>
          )}

          <label className="flex items-center gap-2 text-sm text-foreground">
            <input type="checkbox" checked={ru} onChange={e => setRu(e.target.checked)} />
            Rolling-update strategy
          </label>
          {ru && (
            <div className="grid grid-cols-2 gap-2">
              <FormField label="Max surge"><input value={surge} onChange={e => setSurge(e.target.value)} placeholder="25%" className={numCls} /></FormField>
              <FormField label="Max unavailable"><input value={unavail} onChange={e => setUnavail(e.target.value)} placeholder="25%" className={numCls} /></FormField>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
