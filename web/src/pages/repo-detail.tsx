import { useState, useEffect, useRef, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  ArrowLeft, GitBranch, Loader2, Save, GitCommit, FileText, ChevronRight,
  Plus, X, ShieldCheck, ShieldAlert, Trash2, Lock, Globe,
  Code2, Rocket, Copy, Check, AlertCircle, Play,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import {
  type RepoDetail, type RepoFinding, type RepoScanResult,
  severityOrder, severityStyle, relativeTime,
} from '@/lib/repos'

type Tab = 'code' | 'compliance' | 'deployments'

export function RepoDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [tab, setTab] = useState<Tab>('code')
  const [deleting, setDeleting] = useState(false)

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['repo', id],
    queryFn: () => apiFetch<RepoDetail>(`/repos/${id}`),
  })

  async function deleteRepo() {
    if (!confirm('Remove this repository from the store? Deployments that used it keep running; their link is just cleared.')) return
    setDeleting(true)
    try {
      await apiFetch(`/repos/${id}`, { method: 'DELETE' })
      navigate('/repos')
    } catch (e) {
      alert(String(e))
      setDeleting(false)
    }
  }

  if (isLoading || !data) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" /> Loading repository…
      </div>
    )
  }

  const repo = data.repo

  return (
    <div className="flex h-full flex-col">
      {/* Header */}
      <div className="border-b border-border px-4 py-4 lg:px-6">
        <button
          onClick={() => navigate('/repos')}
          className="mb-3 flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          <ArrowLeft className="size-4" /> Repositories
        </button>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <div className="flex items-center gap-2.5">
              <div className="flex size-8 items-center justify-center rounded-md border border-border bg-background">
                <GitBranch className="size-4 text-muted-foreground" />
              </div>
              <h1 className="truncate text-xl font-semibold tracking-tight">{repo.name}</h1>
              <span className="flex items-center gap-1 rounded border border-border bg-muted/50 px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                <GitBranch className="size-3" /> {repo.branch}
              </span>
              {repo.has_credentials ? (
                <span className="flex items-center gap-1 text-xs text-muted-foreground"><Lock className="size-3" /> {repo.auth_type || 'private'}</span>
              ) : (
                <span className="flex items-center gap-1 text-xs text-muted-foreground"><Globe className="size-3" /> public</span>
              )}
            </div>
            <CloneURL url={repo.repo_url} />
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button variant="outline" size="sm" className="gap-1.5 text-critical hover:text-critical" onClick={deleteRepo} disabled={deleting}>
              {deleting ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
              Remove
            </Button>
          </div>
        </div>

        {/* Tabs */}
        <div className="mt-4 flex items-center gap-1">
          <TabButton active={tab === 'code'} onClick={() => setTab('code')} icon={Code2} label="Code" />
          <TabButton active={tab === 'compliance'} onClick={() => setTab('compliance')} icon={ShieldCheck} label="Compliance" count={data.findings.length} />
          <TabButton active={tab === 'deployments'} onClick={() => setTab('deployments')} icon={Rocket} label="Deployments" count={data.used_by.length} />
        </div>
      </div>

      {/* Body */}
      <div className="min-h-0 flex-1 overflow-hidden">
        {tab === 'code' && <CodeTab repoId={id} hasCredentials={repo.has_credentials} />}
        {tab === 'compliance' && <ComplianceTab repoId={id} detail={data} onScanned={() => refetch()} />}
        {tab === 'deployments' && <DeploymentsTab detail={data} />}
      </div>
    </div>
  )
}

function TabButton({ active, onClick, icon: Icon, label, count }: { active: boolean; onClick: () => void; icon: React.ElementType; label: string; count?: number }) {
  return (
    <button
      onClick={onClick}
      className={cn(
        'flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
        active ? 'bg-accent text-foreground' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
      )}
    >
      <Icon className="size-4" /> {label}
      {count !== undefined && count > 0 && (
        <span className="rounded bg-muted px-1 font-mono text-[0.65rem]">{count}</span>
      )}
    </button>
  )
}

function CloneURL({ url }: { url: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => { navigator.clipboard.writeText(url); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
      className="mt-2 flex max-w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
      title="Copy clone URL"
    >
      <span className="truncate">{url}</span>
      {copied ? <Check className="size-3.5 shrink-0 text-success" /> : <Copy className="size-3.5 shrink-0" />}
    </button>
  )
}

// ── Code tab: GitHub-style file browser + editor ────────────────────────────

function CodeTab({ repoId, hasCredentials }: { repoId: string; hasCredentials: boolean }) {
  const [files, setFiles] = useState<string[]>([])
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const [originalContent, setOriginalContent] = useState('')
  const [loadingFiles, setLoadingFiles] = useState(true)
  const [loadingContent, setLoadingContent] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [committing, setCommitting] = useState(false)
  const [committed, setCommitted] = useState(false)
  const [commitMsg, setCommitMsg] = useState('')
  const [error, setError] = useState('')
  const [newFileName, setNewFileName] = useState('')
  const [showNewFile, setShowNewFile] = useState(false)
  const newFileInputRef = useRef<HTMLInputElement>(null)

  const isDirty = content !== originalContent

  const loadFiles = useCallback(() => {
    setLoadingFiles(true); setError('')
    apiFetch<{ files: string[] }>(`/repos/${repoId}/files`)
      .then((d) => setFiles(d.files ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoadingFiles(false))
  }, [repoId])

  useEffect(() => { loadFiles() }, [loadFiles])

  async function selectFile(path: string) {
    if (isDirty && selectedFile && !confirm(`Discard unsaved changes to ${selectedFile}?`)) return
    setSelectedFile(path); setContent(''); setOriginalContent(''); setSaved(false); setLoadingContent(true); setError('')
    try {
      const d = await apiFetch<{ content: string }>(`/repos/${repoId}/files/${path}`)
      setContent(d.content); setOriginalContent(d.content)
    } catch (e) { setError(String(e)) } finally { setLoadingContent(false) }
  }

  async function confirmNewFile() {
    const path = newFileName.trim()
    if (!path) return
    if (isDirty && selectedFile && !confirm(`Discard unsaved changes to ${selectedFile}?`)) return
    setError('')
    try {
      await apiFetch(`/repos/${repoId}/files/${path}`, { method: 'PUT', body: JSON.stringify({ content: '' }) })
      setFiles((prev) => prev.includes(path) ? prev : [...prev, path].sort())
      setSelectedFile(path); setContent(''); setOriginalContent(''); setSaved(false)
    } catch (e) { setError(String(e)) }
    setShowNewFile(false); setNewFileName('')
  }

  async function saveFile() {
    if (!selectedFile) return
    setSaving(true); setError('')
    try {
      await apiFetch(`/repos/${repoId}/files/${selectedFile}`, { method: 'PUT', body: JSON.stringify({ content }) })
      setOriginalContent(content); setSaved(true); setTimeout(() => setSaved(false), 2000)
    } catch (e) { setError(String(e)) } finally { setSaving(false) }
  }

  async function commitAndPush() {
    setCommitting(true); setError('')
    try {
      await apiFetch(`/repos/${repoId}/commit`, { method: 'POST', body: JSON.stringify({ message: commitMsg || 'edit via AutoDevOps dashboard' }) })
      setCommitted(true); setCommitMsg(''); setTimeout(() => setCommitted(false), 3000)
    } catch (e) { setError(String(e)) } finally { setCommitting(false) }
  }

  // Group files by top-level directory for a lightweight tree.
  const grouped: Record<string, string[]> = {}
  for (const f of files) {
    const slash = f.indexOf('/')
    const dir = slash === -1 ? '' : f.slice(0, slash)
    ;(grouped[dir] ??= []).push(f)
  }
  const dirs = Object.keys(grouped).sort()

  return (
    <div className="flex h-full flex-col">
      {error && <div className="border-b border-border bg-critical/10 px-4 py-2 text-xs text-critical">{error}</div>}
      <div className="flex min-h-0 flex-1">
        {/* File tree */}
        <div className="w-64 shrink-0 overflow-y-auto border-r border-border bg-muted/20 py-2 text-xs">
          <div className="mb-1 flex items-center justify-between border-b border-border px-3 pb-1.5">
            <span className="font-medium text-muted-foreground">Files</span>
            <button onClick={() => { setShowNewFile(true); setNewFileName(''); setTimeout(() => newFileInputRef.current?.focus(), 0) }} title="New file"
              className="rounded p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground">
              <Plus className="size-3.5" />
            </button>
          </div>
          {showNewFile && (
            <div className="flex items-center gap-1 px-2 pb-1.5">
              <input ref={newFileInputRef} className="h-6 flex-1 rounded border bg-background px-2 font-mono text-xs focus:outline-none focus:ring-1 focus:ring-ring"
                placeholder="filename e.g. .env" value={newFileName} onChange={(e) => setNewFileName(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') confirmNewFile(); if (e.key === 'Escape') { setShowNewFile(false); setNewFileName('') } }} />
              <button onClick={confirmNewFile} className="text-primary hover:text-primary/80"><Plus className="size-3.5" /></button>
              <button onClick={() => { setShowNewFile(false); setNewFileName('') }} className="text-muted-foreground hover:text-foreground"><X className="size-3.5" /></button>
            </div>
          )}
          {loadingFiles ? (
            <div className="flex items-center gap-2 px-3 py-2 text-muted-foreground"><Loader2 className="size-3 animate-spin" /> Loading…</div>
          ) : files.length === 0 && !showNewFile ? (
            <p className="px-3 py-2 text-muted-foreground">Empty repository.</p>
          ) : (
            dirs.map((dir) => (
              <div key={dir}>
                {dir && (
                  <div className="flex items-center gap-1 px-3 py-1 font-medium text-muted-foreground">
                    <ChevronRight className="size-3" /> {dir}
                  </div>
                )}
                {grouped[dir].map((f) => (
                  <button key={f} onClick={() => selectFile(f)}
                    className={cn('flex w-full items-center gap-1.5 truncate px-3 py-1 text-left hover:bg-muted', dir ? 'pl-6' : '',
                      selectedFile === f && 'bg-muted font-medium text-foreground')}>
                    <FileText className="size-3 shrink-0" />
                    <span className="truncate">{dir ? f.slice(dir.length + 1) : f}</span>
                  </button>
                ))}
              </div>
            ))
          )}
        </div>

        {/* Editor */}
        <div className="flex min-w-0 flex-1 flex-col">
          {loadingContent ? (
            <div className="flex flex-1 items-center justify-center text-muted-foreground"><Loader2 className="size-4 animate-spin" /></div>
          ) : selectedFile ? (
            <>
              <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
                <span className="font-mono text-xs text-muted-foreground">{selectedFile}</span>
                <Button size="sm" variant="outline" className="h-6 gap-1 px-2 text-xs" disabled={saving || !isDirty} onClick={saveFile}>
                  {saving ? <Loader2 className="size-3 animate-spin" /> : <Save className="size-3" />}
                  {saved ? 'Saved!' : 'Save file'}
                </Button>
              </div>
              <textarea className="flex-1 resize-none bg-background p-3 font-mono text-xs outline-none" value={content}
                onChange={(e) => setContent(e.target.value)} spellCheck={false} />
            </>
          ) : (
            <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">Select a file to view & edit</div>
          )}
        </div>
      </div>

      {/* Commit bar */}
      {hasCredentials ? (
        <div className="flex items-center gap-2 border-t border-border px-4 py-3">
          <input className="h-8 flex-1 rounded border bg-background px-3 text-xs placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            placeholder="Commit message (optional)" value={commitMsg} onChange={(e) => setCommitMsg(e.target.value)} />
          <Button size="sm" className="gap-1.5" disabled={committing} onClick={commitAndPush}>
            {committing ? <Loader2 className="size-3.5 animate-spin" /> : <GitCommit className="size-3.5" />}
            {committed ? 'Pushed!' : 'Commit & push'}
          </Button>
        </div>
      ) : (
        <div className="border-t border-border px-4 py-3 text-xs text-muted-foreground">
          <span className="font-medium text-foreground">Read-only edits</span> — no git credentials stored. Add a token (remove & re-add the repo) to enable commit &amp; push.
        </div>
      )}
    </div>
  )
}

// ── Compliance tab ──────────────────────────────────────────────────────────

// ScanningPanel is the animated "scan in progress" state — a spinning ring
// around a shield, with the source scanners listed as pulsing chips.
function ScanningPanel() {
  const scanners = ['GitLeaks', 'Hadolint', 'tfsec']
  return (
    <div className="flex flex-col items-center gap-5 py-16">
      <div className="relative flex size-16 items-center justify-center">
        <span className="absolute inset-0 rounded-full border-2 border-border" />
        <span className="absolute inset-0 animate-spin rounded-full border-2 border-transparent border-t-primary" />
        <span className="absolute inset-1.5 animate-ping rounded-full bg-primary/10" />
        <ShieldCheck className="size-6 text-primary" />
      </div>
      <div className="space-y-1 text-center">
        <p className="text-sm font-medium">Scanning repository…</p>
        <p className="text-xs text-muted-foreground">Running source compliance scanners</p>
      </div>
      <div className="flex flex-wrap items-center justify-center gap-2">
        {scanners.map((s, i) => (
          <span key={s} className="flex items-center gap-1.5 rounded-full border border-border bg-muted/40 px-2.5 py-1 text-xs text-muted-foreground">
            <span className="size-1.5 animate-pulse rounded-full bg-primary" style={{ animationDelay: `${i * 250}ms` }} />
            {s}
          </span>
        ))}
      </div>
    </div>
  )
}

function ComplianceTab({ repoId, detail, onScanned }: { repoId: string; detail: RepoDetail; onScanned: () => void }) {
  const [findings, setFindings] = useState<RepoFinding[]>(detail.findings)
  const [scanning, setScanning] = useState(false)
  const [blocked, setBlocked] = useState(false)
  const [error, setError] = useState('')

  async function scan() {
    setScanning(true); setError('')
    const start = Date.now()
    try {
      const res = await apiFetch<RepoScanResult>(`/repos/${repoId}/scan`, { method: 'POST' })
      setFindings(res.findings ?? [])
      setBlocked(res.blocked)
      onScanned()
    } catch (e) {
      setError(String(e))
    } finally {
      // These scanners are near-instant on a small repo (gitleaks scans in ~10ms),
      // so hold the scanning animation for a minimum beat — otherwise the spinner
      // flashes by faster than the eye can register and it looks like nothing ran.
      const elapsed = Date.now() - start
      const minMs = 800
      if (elapsed < minMs) await new Promise((r) => setTimeout(r, minMs - elapsed))
      setScanning(false)
    }
  }

  const counts = Object.fromEntries(severityOrder.map((s) => [s, findings.filter((f) => f.severity === s).length]))
  const actionable = (counts.critical ?? 0) + (counts.high ?? 0)

  return (
    <div className="h-full overflow-y-auto p-4 lg:p-6">
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Button size="sm" className="gap-1.5" onClick={scan} disabled={scanning}>
          {scanning ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
          {scanning ? 'Scanning…' : 'Run compliance check'}
        </Button>
        <p className="text-xs text-muted-foreground">
          GitLeaks + Hadolint + tfsec on the source tree. {detail.repo.last_scanned_at ? `Last scanned ${relativeTime(detail.repo.last_scanned_at)}.` : 'Never scanned.'}
        </p>
        {findings.length > 0 && (
          actionable > 0 ? (
            <span className="ml-auto flex items-center gap-1.5 rounded-lg border border-critical/25 bg-critical/8 px-3 py-1.5 text-sm font-medium text-critical">
              <ShieldAlert className="size-4" /> {actionable} actionable{blocked ? ' · gate would block' : ''}
            </span>
          ) : (
            <span className="ml-auto flex items-center gap-1.5 rounded-lg border border-success/25 bg-success/8 px-3 py-1.5 text-sm font-medium text-success">
              <ShieldCheck className="size-4" /> no critical / high
            </span>
          )
        )}
      </div>

      {error && (
        <div className="mb-4 flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
          <AlertCircle className="size-4 shrink-0" />{error}
        </div>
      )}

      {!scanning && findings.length > 0 && (
        <div className="mb-4 flex flex-wrap gap-2">
          {severityOrder.map((s) => (counts[s] ?? 0) > 0 && (
            <div key={s} className={cn('flex items-center gap-2 rounded-lg border px-3 py-1.5', severityStyle[s])}>
              <span className="text-[0.7rem] font-semibold uppercase tracking-wide">{s}</span>
              <span className="font-mono text-sm font-bold">{counts[s]}</span>
            </div>
          ))}
        </div>
      )}

      <div className="overflow-hidden rounded-xl border border-border bg-card">
        {scanning ? (
          <ScanningPanel />
        ) : findings.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-16 text-center">
            <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
              <ShieldCheck className="size-4 text-muted-foreground" />
            </div>
            <p className="max-w-xs text-sm text-muted-foreground text-pretty">
              {detail.repo.last_scanned_at ? 'No findings — this repo is clean.' : 'Run a compliance check to scan this repository.'}
            </p>
          </div>
        ) : (
          <div className="divide-y divide-border">
            {findings.map((f, i) => (
              <div key={i} className="flex flex-col gap-1.5 px-5 py-3.5 transition-colors hover:bg-accent/30 sm:flex-row sm:items-start sm:gap-4">
                <div className="flex shrink-0 items-center gap-2">
                  <span className={cn('rounded border px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase', severityStyle[f.severity])}>{f.severity}</span>
                  <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground">{f.scanner}</span>
                </div>
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{f.message}</p>
                  {f.file_path && (
                    <span className="font-mono text-xs text-muted-foreground">{f.file_path}{f.line_number ? `:${f.line_number}` : ''}</span>
                  )}
                </div>
                <span className="shrink-0 font-mono text-[0.65rem] text-muted-foreground">{f.rule_id}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ── Deployments tab ─────────────────────────────────────────────────────────

function DeploymentsTab({ detail }: { detail: RepoDetail }) {
  const navigate = useNavigate()
  return (
    <div className="h-full overflow-y-auto p-4 lg:p-6">
      <div className="overflow-hidden rounded-xl border border-border bg-card">
        {detail.used_by.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-16 text-center">
            <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
              <Rocket className="size-4 text-muted-foreground" />
            </div>
            <p className="max-w-xs text-sm text-muted-foreground text-pretty">
              No deployments use this repository yet. Pick it when creating a new deployment.
            </p>
          </div>
        ) : (
          <div className="divide-y divide-border">
            {detail.used_by.map((a) => (
              <button key={a.app_id} onClick={() => navigate(`/deployments/${a.deployment_id}`)}
                className="flex w-full items-center gap-3 px-5 py-3.5 text-left transition-colors hover:bg-accent/30">
                <Rocket className="size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{a.app_name}</p>
                  <p className="truncate text-xs text-muted-foreground">in {a.deployment_name}</p>
                </div>
                <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[0.65rem] text-muted-foreground">{a.status}</span>
                <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
