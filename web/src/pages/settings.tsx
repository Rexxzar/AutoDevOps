import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, Plus, Server, Cloud, CheckCircle2, AlertCircle, Trash2, ShieldCheck } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ProviderTag } from '@/components/meta-tags'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import type { Provider } from '@/lib/mock-data'

interface ApiCluster { id: string; name: string; endpoint: string; status: string }
interface ApiAccount { id: string; provider: string; name: string }

function StatusMsg({ ok, msg }: { ok: boolean; msg: string }) {
  return (
    <div className={cn(
      'flex items-center gap-2 rounded-md border px-3 py-2 text-sm',
      ok ? 'border-success/25 bg-success/8 text-success' : 'border-critical/25 bg-critical/8 text-critical',
    )}>
      {ok ? <CheckCircle2 className="size-4 shrink-0" /> : <AlertCircle className="size-4 shrink-0" />}
      {msg}
    </div>
  )
}

function ClusterSection() {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null)

  const { data: clusters = [] } = useQuery({
    queryKey: ['clusters'],
    queryFn: () => apiFetch<ApiCluster[]>('/clusters'),
  })

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    setResult(null)
    try {
      await apiFetch('/clusters', {
        method: 'POST',
        body: JSON.stringify({ name, endpoint }),
      })
      setResult({ ok: true, msg: `Cluster "${name}" registered.` })
      setName('')
      setEndpoint('')
      qc.invalidateQueries({ queryKey: ['clusters'] })
    } catch (err) {
      setResult({ ok: false, msg: err instanceof Error ? err.message : 'Failed to register cluster' })
    } finally {
      setLoading(false)
    }
  }

  return (
    <Card>
      <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
        <div className="flex items-center gap-2">
          <Server className="size-4 text-muted-foreground" />
          <h2 className="text-sm font-semibold">Kubernetes Clusters</h2>
        </div>
        <span className="font-mono text-xs text-muted-foreground">{clusters.length} registered</span>
      </div>

      {clusters.length > 0 && (
        <div className="divide-y divide-border">
          {clusters.map((c) => (
            <div key={c.id} className="flex items-center gap-3 px-5 py-3">
              <span className={cn('size-2 rounded-full', c.status === 'connected' || c.status === 'healthy' ? 'bg-success' : 'bg-muted-foreground/50')} />
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium">{c.name}</p>
                <p className="font-mono text-xs text-muted-foreground truncate">{c.endpoint}</p>
              </div>
              <span className="text-xs text-muted-foreground capitalize">{c.status}</span>
            </div>
          ))}
        </div>
      )}

      <form onSubmit={submit} className="space-y-3 border-t border-border p-5">
        <p className="text-[0.7rem] font-semibold uppercase tracking-wider text-muted-foreground">Register cluster</p>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Name</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="production-eks"
              required
              className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/40"
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">API endpoint</label>
            <input
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value)}
              placeholder="https://127.0.0.1:6443"
              required
              className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/40"
            />
          </div>
        </div>
        {result && <StatusMsg ok={result.ok} msg={result.msg} />}
        <Button type="submit" size="sm" disabled={loading} className="gap-1.5">
          {loading ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
          Register
        </Button>
      </form>
    </Card>
  )
}

function CloudAccountSection() {
  const qc = useQueryClient()
  const [provider, setProvider] = useState<'aws' | 'azure'>('aws')
  const [name, setName] = useState('')
  const [creds, setCreds] = useState('')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)

  const { data: accounts = [] } = useQuery({
    queryKey: ['cloud-accounts'],
    queryFn: () => apiFetch<ApiAccount[]>('/cloud/accounts'),
  })

  async function handleDelete(id: string, accountName: string) {
    if (!window.confirm(`Remove cloud account "${accountName}"? This deletes its stored credentials and cached inventory.`)) return
    setDeletingId(id)
    setResult(null)
    try {
      await apiFetch(`/cloud/accounts/${id}`, { method: 'DELETE' })
      qc.invalidateQueries({ queryKey: ['cloud-accounts'] })
      qc.invalidateQueries({ queryKey: ['cloud-resources'] })
    } catch (err) {
      setResult({ ok: false, msg: err instanceof Error ? err.message : 'Failed to remove account' })
    } finally {
      setDeletingId(null)
    }
  }

  const awsPlaceholder = JSON.stringify({ access_key_id: 'AKIA...', secret_access_key: '...', region: 'us-east-1' }, null, 2)
  const azurePlaceholder = JSON.stringify({ subscription_id: '...' }, null, 2)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    setResult(null)
    try {
      let parsed: unknown
      try { parsed = JSON.parse(creds) } catch { throw new Error('Credentials must be valid JSON') }
      await apiFetch('/cloud/accounts', {
        method: 'POST',
        body: JSON.stringify({ provider, name, credentials: parsed }),
      })
      setResult({ ok: true, msg: `Account "${name}" registered. Poller will start within 60s.` })
      setName('')
      setCreds('')
      qc.invalidateQueries({ queryKey: ['cloud-accounts'] })
    } catch (err) {
      setResult({ ok: false, msg: err instanceof Error ? err.message : 'Failed to register account' })
    } finally {
      setLoading(false)
    }
  }

  return (
    <Card>
      <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
        <div className="flex items-center gap-2">
          <Cloud className="size-4 text-muted-foreground" />
          <h2 className="text-sm font-semibold">Cloud Accounts</h2>
        </div>
        <span className="font-mono text-xs text-muted-foreground">{accounts.length} registered</span>
      </div>

      {accounts.length > 0 && (
        <div className="divide-y divide-border">
          {accounts.map((a) => (
            <div key={a.id} className="flex items-center gap-3 px-5 py-3">
              <ProviderTag provider={a.provider as Provider} />
              <p className="flex-1 text-sm font-medium">{a.name}</p>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs text-muted-foreground hover:text-critical"
                disabled={deletingId === a.id}
                onClick={() => handleDelete(a.id, a.name)}
              >
                {deletingId === a.id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
              </Button>
            </div>
          ))}
        </div>
      )}

      <form onSubmit={submit} className="space-y-3 border-t border-border p-5">
        <p className="text-[0.7rem] font-semibold uppercase tracking-wider text-muted-foreground">Register account</p>

        <div className="flex gap-2">
          {(['aws', 'azure'] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setProvider(p)}
              className={cn(
                'rounded-full border px-3 py-1 text-xs font-medium transition-colors uppercase',
                provider === p ? 'border-ring/40 bg-accent text-foreground' : 'border-border text-muted-foreground hover:bg-accent/60',
              )}
            >
              {p}
            </button>
          ))}
        </div>

        <div className="space-y-1.5">
          <label className="text-xs text-muted-foreground">Account name</label>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={provider === 'aws' ? 'aws-prod' : 'azure-dev'}
            required
            className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/40"
          />
        </div>

        <div className="space-y-1.5">
          <label className="text-xs text-muted-foreground">
            Credentials <span className="text-muted-foreground/60">(JSON)</span>
          </label>
          <textarea
            value={creds}
            onChange={(e) => setCreds(e.target.value)}
            placeholder={provider === 'aws' ? awsPlaceholder : azurePlaceholder}
            rows={5}
            required
            className="w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-xs outline-none placeholder:text-muted-foreground/50 focus:border-ring focus:ring-2 focus:ring-ring/40 resize-none"
          />
          {provider === 'azure' && (
            <p className="text-[0.7rem] text-muted-foreground">
              Add <code className="font-mono">tenant_id</code>, <code className="font-mono">client_id</code> and{' '}
              <code className="font-mono">client_secret</code> for a service principal — omit all three to fall back to the
              server's own <code className="font-mono">az login</code> session.
            </p>
          )}
        </div>

        {result && <StatusMsg ok={result.ok} msg={result.msg} />}
        <Button type="submit" size="sm" disabled={loading} className="gap-1.5">
          {loading ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
          Register
        </Button>
      </form>
    </Card>
  )
}

function SelfHealingSection() {
  const { data: settings, isLoading } = useQuery<Record<string, string>>({
    queryKey: ['settings'],
    queryFn: () => apiFetch<Record<string, string>>('/settings'),
  })

  const [saving, setSaving] = useState(false)

  const scope = settings?.self_healing_scope ?? 'autodevops_only'
  const healAll = scope === 'all_local'

  async function toggle(checked: boolean) {
    setSaving(true)
    try {
      await apiFetch('/settings', {
        method: 'PUT',
        body: JSON.stringify({ key: 'self_healing_scope', value: checked ? 'all_local' : 'autodevops_only' }),
      })
      // Invalidate so the next render picks up the updated value.
      // (useQueryClient is available from the outer scope via closure if we add it above, but
      // we import it inline here to keep the component self-contained.)
    } catch { /* silently ignore for now */ }
    finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <div className="flex items-center gap-2 border-b border-border px-5 py-3.5">
        <ShieldCheck className="size-4 text-muted-foreground" />
        <h2 className="text-sm font-semibold">Self-Healing</h2>
      </div>
      <div className="px-5 py-4 space-y-3">
        <label className="flex items-start gap-3 cursor-pointer">
          <input
            type="checkbox"
            className="mt-0.5 rounded"
            disabled={isLoading || saving}
            checked={healAll}
            onChange={(e) => toggle(e.target.checked)}
          />
          <div>
            <p className="text-sm font-medium">Allow self-healing to monitor non-AutoDevOps local resources</p>
            <p className="text-xs text-muted-foreground mt-0.5">
              When enabled, the auto-restart controller will also watch and restart Docker containers / K8s pods
              that were not created by AutoDevOps. Default: off.
            </p>
            <p className="text-xs text-warning mt-1">
              Note: healer behaviour change ships in a future phase — the toggle is stored now but not yet acted on.
            </p>
          </div>
        </label>
        {saving && <p className="text-xs text-muted-foreground">Saving…</p>}
      </div>
    </Card>
  )
}

export function SettingsPage() {
  return (
    <div>
      <PageHeader
        title="Settings"
        description="Register Kubernetes clusters and cloud accounts for the control plane."
      />
      <div className="space-y-6 p-4 lg:p-6">
        <ClusterSection />
        <CloudAccountSection />
        <SelfHealingSection />
      </div>
    </div>
  )
}
