import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import {
  RefreshCw, Plus, Loader2, KeyRound, AlertCircle, Trash2, Search,
  ShieldCheck, RotateCw, Lock, ExternalLink, AlertTriangle,
} from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/dialog'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import { relativeTime } from '@/lib/repos'
import { FormField, inputCls } from '@/components/deploy-form-fields'

interface SecretSummary {
  name: string
  category: string
  scope_label?: string
  deployment_id?: string
  repo_id?: string
  managed: boolean
  created_at: string
  updated_at: string
}

interface VaultStatus {
  version: number
  rotation_count: number
  active_since: string
  last_rotated_at?: string
  secret_count: number
}

const categoryMeta: Record<string, { label: string; cls: string }> = {
  webhook:           { label: 'Webhook',        cls: 'text-info border-info/25 bg-info/10' },
  'git-credentials': { label: 'Git credentials', cls: 'text-warning border-warning/25 bg-warning/10' },
  'image-env':       { label: 'Image env',      cls: 'text-info border-info/25 bg-info/10' },
  'secret-files':    { label: 'Secret files',   cls: 'text-info border-info/25 bg-info/10' },
  'cloud-account':   { label: 'Cloud account',  cls: 'text-critical border-critical/25 bg-critical/10' },
  'git-repo':        { label: 'Repo creds',     cls: 'text-warning border-warning/25 bg-warning/10' },
  user:              { label: 'User secret',    cls: 'text-foreground border-border bg-muted/50' },
}

function catMeta(c: string) {
  return categoryMeta[c] ?? { label: c, cls: 'text-muted-foreground border-border bg-muted/40' }
}

export function SecretsPage() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [search, setSearch] = useState('')
  const [catFilter, setCatFilter] = useState<string>('all')
  const [showAdd, setShowAdd] = useState(false)
  const [showRotateMaster, setShowRotateMaster] = useState(false)
  const [toDelete, setToDelete] = useState<SecretSummary | null>(null)

  const { data: secrets = [], isLoading, refetch, isFetching } = useQuery({
    queryKey: ['secrets'],
    queryFn: () => apiFetch<SecretSummary[]>('/secrets'),
    refetchInterval: 30_000,
  })
  const { data: status } = useQuery({
    queryKey: ['vault-status'],
    queryFn: () => apiFetch<VaultStatus>('/secrets/status'),
    refetchInterval: 30_000,
  })

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['secrets'] })
    qc.invalidateQueries({ queryKey: ['vault-status'] })
  }

  const categories = ['all', ...Object.keys(categoryMeta).filter(c => secrets.some(s => s.category === c))]
  const filtered = secrets.filter(s => {
    if (catFilter !== 'all' && s.category !== catFilter) return false
    if (search && !s.name.toLowerCase().includes(search.toLowerCase()) &&
        !(s.scope_label ?? '').toLowerCase().includes(search.toLowerCase())) return false
    return true
  })

  return (
    <div>
      <PageHeader
        title="Secrets"
        description="AES-256-GCM vault with argon2id-derived keys and envelope encryption (KEK/DEK)."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
        <Button size="sm" className="gap-1.5" onClick={() => setShowAdd(true)}>
          <Plus className="size-3.5" />
          New secret
        </Button>
      </PageHeader>

      <div className="space-y-4 p-4 lg:p-6">
        {status && <VaultStatusCard status={status} onRotated={invalidate} onRotateMaster={() => setShowRotateMaster(true)} />}

        {/* Category filter + search */}
        <div className="flex flex-wrap items-center gap-2">
          {categories.map(c => (
            <button
              key={c}
              onClick={() => setCatFilter(c)}
              className={cn(
                'flex shrink-0 items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                catFilter === c ? 'border-ring/40 bg-accent text-foreground' : 'border-border text-muted-foreground hover:bg-accent/60',
              )}
            >
              {c === 'all' ? 'All' : catMeta(c).label}
              <span className="rounded bg-muted px-1 font-mono text-[0.65rem]">
                {c === 'all' ? secrets.length : secrets.filter(s => s.category === c).length}
              </span>
            </button>
          ))}
          <div className="relative ml-auto">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <input
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search secrets…"
              className={cn(inputCls, 'h-8 w-48 pl-8 text-xs')}
            />
          </div>
        </div>

        {/* Secrets table */}
        <Card className="overflow-hidden">
          {isLoading && <div className="py-12 text-center text-sm text-muted-foreground">Loading secrets…</div>}
          {!isLoading && filtered.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-16 text-center">
              <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                <KeyRound className="size-4 text-muted-foreground" />
              </div>
              <p className="max-w-xs text-sm text-muted-foreground text-pretty">
                {secrets.length === 0
                  ? 'The vault is empty. Secrets appear here as you connect repos, deploy apps and add cloud accounts.'
                  : 'No secrets match the current filter.'}
              </p>
            </div>
          )}
          {filtered.length > 0 && (
            <div className="divide-y divide-border">
              {filtered.map(s => (
                <div key={s.name} className="flex flex-col gap-1.5 px-5 py-3.5 transition-colors hover:bg-accent/30 sm:flex-row sm:items-center sm:gap-4">
                  <div className="flex min-w-0 flex-1 items-center gap-3">
                    <Lock className="size-3.5 shrink-0 text-muted-foreground" />
                    <div className="min-w-0">
                      <p className="truncate font-mono text-sm">{s.name}</p>
                      {s.scope_label && (
                        <button
                          className={cn(
                            'mt-0.5 inline-flex items-center gap-1 text-xs text-muted-foreground',
                            (s.deployment_id || s.repo_id) && 'hover:text-foreground hover:underline',
                          )}
                          disabled={!s.deployment_id && !s.repo_id}
                          onClick={() => {
                            if (s.deployment_id) navigate(`/deployments/${s.deployment_id}`)
                            else if (s.repo_id) navigate(`/repos/${s.repo_id}`)
                          }}
                        >
                          {s.scope_label}
                          {(s.deployment_id || s.repo_id) && <ExternalLink className="size-3" />}
                        </button>
                      )}
                    </div>
                  </div>

                  <span className={cn('shrink-0 rounded border px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase', catMeta(s.category).cls)}>
                    {catMeta(s.category).label}
                  </span>

                  <span className="shrink-0 font-mono text-[0.65rem] text-muted-foreground sm:w-28 sm:text-right">
                    {relativeTime(s.updated_at)}
                  </span>

                  <button
                    onClick={() => setToDelete(s)}
                    title="Delete secret"
                    className="shrink-0 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-critical/10 hover:text-critical"
                  >
                    <Trash2 className="size-3.5" />
                  </button>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>

      {showAdd && <AddSecretDialog onClose={() => setShowAdd(false)} onAdded={() => { setShowAdd(false); invalidate() }} />}
      {showRotateMaster && <RotateMasterDialog onClose={() => setShowRotateMaster(false)} />}
      {toDelete && <DeleteSecretDialog secret={toDelete} onClose={() => setToDelete(null)} onDeleted={() => { setToDelete(null); invalidate() }} />}
    </div>
  )
}

function VaultStatusCard({ status, onRotated, onRotateMaster }: {
  status: VaultStatus
  onRotated: () => void
  onRotateMaster: () => void
}) {
  const [rotating, setRotating] = useState(false)
  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')

  async function rotate() {
    if (!confirm('Rotate the data key? Every secret is re-encrypted under a fresh key. Values are unchanged and deployments are unaffected.')) return
    setRotating(true); setMsg(''); setErr('')
    try {
      const r = await apiFetch<{ version: number; reencrypted: number }>('/secrets/rotate', { method: 'POST' })
      setMsg(`Rotated to key version ${r.version}, ${r.reencrypted} secret(s) re-encrypted.`)
      onRotated()
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Rotation failed')
    } finally {
      setRotating(false)
    }
  }

  return (
    <Card className="p-5">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex items-center gap-4">
          <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-muted/40">
            <ShieldCheck className="size-5 text-success" />
          </div>
          <div className="flex flex-wrap gap-x-8 gap-y-2">
            <Stat label="Secrets" value={String(status.secret_count)} />
            <Stat label="Data key version" value={`v${status.version}`} />
            <Stat label="Rotations" value={String(Math.max(0, status.rotation_count - 1))} />
            <Stat label="Active since" value={relativeTime(status.active_since)} />
          </div>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button variant="outline" size="sm" className="gap-1.5" onClick={rotate} disabled={rotating}>
            {rotating ? <Loader2 className="size-3.5 animate-spin" /> : <RotateCw className="size-3.5" />}
            Rotate data key
          </Button>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={onRotateMaster}>
            <KeyRound className="size-3.5" />
            Rotate master key
          </Button>
        </div>
      </div>
      {msg && (
        <div className="mt-3 flex items-center gap-2 rounded-lg border border-success/25 bg-success/8 px-3 py-2 text-sm text-success">
          <ShieldCheck className="size-4 shrink-0" />{msg}
        </div>
      )}
      {err && (
        <div className="mt-3 flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2 text-sm text-critical">
          <AlertCircle className="size-4 shrink-0" />{err}
        </div>
      )}
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-[0.65rem] uppercase tracking-wide text-muted-foreground">{label}</p>
      <p className="font-mono text-sm font-semibold">{value}</p>
    </div>
  )
}

function AddSecretDialog({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      await apiFetch('/secrets', { method: 'POST', body: JSON.stringify({ name: name.trim(), value }) })
      onAdded()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to store secret')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onClose={onClose} className="max-w-md">
      <form onSubmit={submit} className="space-y-4 p-5">
        <div>
          <p className="text-sm font-semibold">New secret</p>
          <p className="text-xs text-muted-foreground">Stored AES-256-GCM encrypted. The value is never shown again after saving.</p>
        </div>
        <FormField label="Name">
          <input value={name} onChange={e => setName(e.target.value)} placeholder="my-api-key" required className={inputCls} />
        </FormField>
        <FormField label="Value">
          <textarea value={value} onChange={e => setValue(e.target.value)} required rows={4} className={cn(inputCls, 'font-mono resize-y')} />
        </FormField>
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" className="gap-1.5" disabled={saving || !name.trim() || !value}>
            {saving && <Loader2 className="size-4 animate-spin" />}
            {saving ? 'Saving…' : 'Save secret'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function DeleteSecretDialog({ secret, onClose, onDeleted }: {
  secret: SecretSummary
  onClose: () => void
  onDeleted: () => void
}) {
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState('')

  async function del() {
    setDeleting(true); setError('')
    try {
      await apiFetch(`/secrets/${encodeURIComponent(secret.name)}`, { method: 'DELETE' })
      onDeleted()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete secret')
      setDeleting(false)
    }
  }

  return (
    <Dialog open onClose={onClose} className="max-w-md">
      <div className="space-y-4 p-5">
        <div className="flex items-center gap-2">
          <AlertTriangle className="size-4 text-critical" />
          <p className="text-sm font-semibold">Delete secret</p>
        </div>
        <p className="break-all font-mono text-xs text-muted-foreground">{secret.name}</p>
        {secret.managed && (
          <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-sm text-warning">
            <AlertTriangle className="size-4 shrink-0" />
            <span>
              This is a platform-managed secret ({catMeta(secret.category).label.toLowerCase()})
              {secret.scope_label ? ` for ${secret.scope_label}` : ''}. Deleting it will break the
              deployment, cloud account or repo that relies on it. Only delete it if you know what you're doing.
            </span>
          </div>
        )}
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="button" variant="destructive" className="gap-1.5" onClick={del} disabled={deleting}>
            {deleting ? <Loader2 className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
            {deleting ? 'Deleting…' : 'Delete'}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

function RotateMasterDialog({ onClose }: { onClose: () => void }) {
  const [key, setKey] = useState('')
  const [confirmKey, setConfirmKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (key !== confirmKey) { setError('The two values do not match.'); return }
    setSaving(true); setError('')
    try {
      await apiFetch('/secrets/rotate-master', { method: 'POST', body: JSON.stringify({ new_master_key: key }) })
      setDone(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Rotation failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onClose={onClose} className="max-w-md">
      {done ? (
        <div className="space-y-4 p-5">
          <div className="flex items-center gap-2">
            <ShieldCheck className="size-4 text-success" />
            <p className="text-sm font-semibold">Master key rotated</p>
          </div>
          <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-sm text-warning">
            <AlertTriangle className="size-4 shrink-0" />
            <span>
              The data key is now wrapped under the new master key. You <strong>must</strong> update the
              <code className="mx-1 rounded bg-muted px-1 font-mono">MASTER_KEY</code> environment variable to the new
              value before restarting the server, or the vault will fail to start.
            </span>
          </div>
          <div className="flex justify-end pt-1">
            <Button type="button" onClick={onClose}>Got it</Button>
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-4 p-5">
          <div>
            <p className="text-sm font-semibold">Rotate master key</p>
            <p className="text-xs text-muted-foreground">
              Re-wraps the data key under a new master key. Secrets are not re-encrypted, so this is fast, but you must
              update the <code className="rounded bg-muted px-1 font-mono">MASTER_KEY</code> env var afterwards.
            </p>
          </div>
          <FormField label="New master key">
            <input value={key} onChange={e => setKey(e.target.value)} type="password" required className={inputCls} />
          </FormField>
          <FormField label="Confirm new master key">
            <input value={confirmKey} onChange={e => setConfirmKey(e.target.value)} type="password" required className={inputCls} />
          </FormField>
          {error && (
            <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertCircle className="size-4 shrink-0" />{error}
            </div>
          )}
          <div className="flex justify-end gap-2 pt-1">
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" className="gap-1.5" disabled={saving || !key}>
              {saving && <Loader2 className="size-4 animate-spin" />}
              {saving ? 'Rotating…' : 'Rotate master key'}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}
