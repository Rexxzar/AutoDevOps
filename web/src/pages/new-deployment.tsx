import { useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Loader2, CheckCircle2, AlertCircle, Globe, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import { TargetPill, FormField, inputCls } from '@/components/deploy-form-fields'
import { ComposeImportForm } from '@/components/compose-import-form'

type Step = 'form' | 'success'
type Mode = 'scratch' | 'import'
type LocalTarget = 'k8s' | 'container'

// ─── Domain field with inline DNS checker ────────────────────────
type DNSStatus = 'idle' | 'checking' | 'ok' | 'fail' | 'error'

function DomainField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
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
        <label className="text-xs font-medium text-muted-foreground">Custom domain <span className="text-muted-foreground/60">(optional)</span></label>
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

// ─── Main page ────────────────────────────────────────────────────
export function NewDeploymentPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStep] = useState<Step>('form')
  const [mode, setMode] = useState<Mode>('scratch')

  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [localTarget, setLocalTarget] = useState<LocalTarget>('k8s')
  const [localDomain, setLocalDomain] = useState('')

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState<{ webhookURL: string; webhookSecret: string; name: string } | null>(null)

  async function submit(e: React.FormEvent) {
    e.preventDefault(); setLoading(true); setError('')
    try {
      const body: Record<string, unknown> = { name, repo_url: repo, branch, target_type: localTarget }
      if (token) body.git_auth = { type: 'token', token }
      if (localDomain) body.domain = localDomain
      const resp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>('/deployments', {
        method: 'POST', body: JSON.stringify(body),
      })
      setSuccess({ webhookURL: resp.webhook_url, webhookSecret: resp.webhook_secret, name })
      setStep('success')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create deployment')
    } finally { setLoading(false) }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      {/* Top bar */}
      <div className="flex h-14 items-center gap-3 border-b border-border px-6">
        <button
          onClick={() => step === 'form' ? navigate(-1) : navigate('/deployments')}
          className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
          {step === 'form' ? 'Back' : 'View deployments'}
        </button>
        <span className="text-sm font-semibold">New Deployment</span>
      </div>

      <div className="flex flex-1 items-start justify-center p-8 pt-12">

        {/* ── Form ── */}
        {step === 'form' && (
          <div className="flex w-full max-w-2xl flex-col items-center gap-6">
            <div className="flex w-full max-w-md gap-2">
              <TargetPill label="From scratch" sub="single repo" active={mode === 'scratch'} onClick={() => setMode('scratch')} />
              <TargetPill label="Import docker-compose" sub="multiple services" active={mode === 'import'} onClick={() => setMode('import')} />
            </div>

            {mode === 'scratch' ? (
              <div className="w-full max-w-md space-y-6">
                <div className="space-y-1">
                  <h1 className="text-xl font-bold">New Deployment</h1>
                  <p className="text-sm text-muted-foreground">Connect a Git repo and choose a target</p>
                </div>
                <form onSubmit={submit} className="space-y-4">
                  <FormField label="App name">
                    <input value={name} onChange={e => setName(e.target.value)} placeholder="my-app" required className={inputCls} />
                  </FormField>
                  <FormField label="Git repository URL">
                    <input value={repo} onChange={e => setRepo(e.target.value)} placeholder="https://github.com/you/my-app" required className={inputCls} />
                  </FormField>
                  <div className="grid grid-cols-2 gap-3">
                    <FormField label="Branch">
                      <input value={branch} onChange={e => setBranch(e.target.value)} placeholder="main" className={inputCls} />
                    </FormField>
                    <FormField label="Git token" hint="private repos">
                      <input value={token} onChange={e => setToken(e.target.value)} placeholder="ghp_…" type="password" className={inputCls} />
                    </FormField>
                  </div>
                  <FormField label="Target">
                    <div className="flex gap-2">
                      <TargetPill label="Kubernetes" sub="kind cluster" active={localTarget === 'k8s'} onClick={() => setLocalTarget('k8s')} />
                      <TargetPill label="Container" sub="Docker SDK" active={localTarget === 'container'} onClick={() => setLocalTarget('container')} />
                    </div>
                  </FormField>
                  <DomainField value={localDomain} onChange={setLocalDomain} />
                  {error && (
                    <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
                      <AlertCircle className="size-4 shrink-0" />{error}
                    </div>
                  )}
                  <Button type="submit" className="w-full h-10 gap-2 mt-2" disabled={loading}>
                    {loading && <Loader2 className="size-4 animate-spin" />}
                    {loading ? 'Creating deployment…' : 'Create deployment'}
                  </Button>
                </form>
              </div>
            ) : (
              <ComposeImportForm />
            )}
          </div>
        )}

        {/* ── Success ── */}
        {step === 'success' && success && (
          <div className="w-full max-w-md space-y-6">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-6 text-success shrink-0" />
              <div>
                <p className="font-semibold">Deployment registered</p>
                <p className="text-sm text-muted-foreground font-mono">{success.name}</p>
              </div>
            </div>
            <div className="rounded-xl border border-border bg-card p-5 space-y-4">
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">GitHub Webhook Setup</p>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Payload URL</p>
                <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs break-all select-all">
                  http://YOUR_SERVER:8080{success.webhookURL}
                </p>
              </div>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Secret</p>
                <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs select-all">{success.webhookSecret}</p>
              </div>
              <p className="text-xs text-muted-foreground">
                Content type: <span className="font-mono">application/json</span> · Event: <span className="font-mono">push</span>
              </p>
            </div>
            <p className="text-xs text-muted-foreground">
              Or trigger immediately from CLI:<br />
              <span className="font-mono">autodev deploy --name {success.name} --repo &lt;url&gt; --target {localTarget} --trigger</span>
            </p>
            <div className="flex gap-3">
              <Button variant="outline" className="flex-1" onClick={() => { setStep('form'); setName(''); setRepo(''); setBranch('main'); setToken('') }}>
                New deployment
              </Button>
              <Button className="flex-1" onClick={() => navigate('/deployments')}>View deployments</Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
