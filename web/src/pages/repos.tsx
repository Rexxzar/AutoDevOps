import { useState, useCallback } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import {
  RefreshCw, GitBranch, Plus, Loader2, ShieldCheck, ShieldAlert,
  Lock, Globe, AlertCircle, Rocket,
} from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { FormField, inputCls } from '@/components/deploy-form-fields'
import { type GitRepo, relativeTime, findingTotal } from '@/lib/repos'

export function ReposPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { token } = useAuth()
  const [showNew, setShowNew] = useState(false)

  const { data: repos = [], isLoading, refetch, isFetching } = useQuery({
    queryKey: ['repos'],
    queryFn: () => apiFetch<GitRepo[]>('/repos'),
    refetchInterval: 15_000,
  })

  useSSE(token, useCallback((type: string) => {
    if (type === 'repo') qc.invalidateQueries({ queryKey: ['repos'] })
  }, [qc]))

  return (
    <div>
      <PageHeader
        title="Repositories"
        description="Reusable Git repositories — browse & edit files, commit, and run compliance checks on demand."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
        <Button size="sm" className="gap-1.5" onClick={() => setShowNew(true)}>
          <Plus className="size-3.5" />
          New repository
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6">
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          {isLoading && (
            <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> Loading repositories…
            </div>
          )}
          {!isLoading && repos.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-16 text-center">
              <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                <GitBranch className="size-4 text-muted-foreground" />
              </div>
              <p className="max-w-xs text-sm text-muted-foreground text-pretty">
                No repositories yet. Add one here, or create a deployment — its repo is registered automatically.
              </p>
              <Button size="sm" className="gap-1.5" onClick={() => setShowNew(true)}>
                <Plus className="size-3.5" /> New repository
              </Button>
            </div>
          )}
          {repos.map((repo, i) => (
            <RepoRow key={repo.id} repo={repo} last={i === repos.length - 1} onOpen={() => navigate(`/repos/${repo.id}`)} />
          ))}
        </div>
      </div>

      {showNew && <NewRepoDialog onClose={() => setShowNew(false)} onCreated={(id) => { setShowNew(false); navigate(`/repos/${id}`) }} />}
    </div>
  )
}


function RepoRow({ repo, last, onOpen }: { repo: GitRepo; last: boolean; onOpen: () => void }) {
  const total = findingTotal(repo.findings)
  const actionable = (repo.findings.critical ?? 0) + (repo.findings.high ?? 0)
  return (
    <button
      onClick={onOpen}
      className={cn(
        'flex w-full flex-col gap-3 px-4 py-4 text-left transition-colors hover:bg-accent/40 lg:flex-row lg:items-center lg:gap-4 lg:px-5',
        !last && 'border-b border-border',
      )}
    >
      <div className="flex min-w-0 items-center gap-3 lg:w-72">
        <div className="flex size-8 shrink-0 items-center justify-center rounded-md border border-border bg-background">
          <GitBranch className="size-4 text-muted-foreground" />
        </div>
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold">{repo.name}</p>
          <p className="truncate text-xs text-muted-foreground">{repo.repo_url}</p>
        </div>
      </div>

      <div className="flex min-w-0 flex-1 items-center gap-3 text-xs text-muted-foreground">
        <span className="flex items-center gap-1 rounded border border-border bg-muted/50 px-1.5 py-0.5 font-mono">
          <GitBranch className="size-3" /> {repo.branch}
        </span>
        {repo.has_credentials ? (
          <span className="flex items-center gap-1"><Lock className="size-3" /> {repo.auth_type || 'private'}</span>
        ) : (
          <span className="flex items-center gap-1"><Globe className="size-3" /> public</span>
        )}
        {repo.used_by > 0 && (
          <span className="flex items-center gap-1"><Rocket className="size-3" /> {repo.used_by} deploy{repo.used_by > 1 ? 's' : ''}</span>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-3">
        {repo.last_scanned_at ? (
          total === 0 ? (
            <span className="flex items-center gap-1 text-xs text-success"><ShieldCheck className="size-3.5" /> clean</span>
          ) : actionable > 0 ? (
            <span className="flex items-center gap-1 text-xs text-critical"><ShieldAlert className="size-3.5" /> {actionable} actionable</span>
          ) : (
            <span className="flex items-center gap-1 text-xs text-muted-foreground"><ShieldCheck className="size-3.5" /> {total} minor</span>
          )
        ) : (
          <span className="text-xs text-muted-foreground">not scanned</span>
        )}
        <span className="hidden w-20 text-right text-xs text-muted-foreground sm:inline">
          {repo.last_scanned_at ? relativeTime(repo.last_scanned_at) : relativeTime(repo.created_at)}
        </span>
      </div>
    </button>
  )
}

function NewRepoDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const [name, setName] = useState('')
  const [repoURL, setRepoURL] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const body: Record<string, unknown> = { repo_url: repoURL.trim(), branch: branch.trim() || 'main' }
      if (name.trim()) body.name = name.trim()
      if (token.trim()) body.git_auth = { type: 'token', token: token.trim() }
      const resp = await apiFetch<{ id: string }>('/repos', { method: 'POST', body: JSON.stringify(body) })
      onCreated(resp.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add repository')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onClose={onClose} className="max-w-md">
      <form onSubmit={submit} className="space-y-4 p-5">
        <div>
          <p className="text-sm font-semibold">Add repository</p>
          <p className="text-xs text-muted-foreground">Register a Git repo to browse, edit and scan it.</p>
        </div>
        <FormField label="Git repository URL">
          <input value={repoURL} onChange={e => setRepoURL(e.target.value)} placeholder="https://github.com/you/my-app" required className={inputCls} />
        </FormField>
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Branch">
            <input value={branch} onChange={e => setBranch(e.target.value)} placeholder="main" className={inputCls} />
          </FormField>
          <FormField label="Display name" hint="optional">
            <input value={name} onChange={e => setName(e.target.value)} placeholder="from URL" className={inputCls} />
          </FormField>
        </div>
        <FormField label="Git token" hint="private repos only">
          <input value={token} onChange={e => setToken(e.target.value)} placeholder="ghp_…" type="password" className={inputCls} />
        </FormField>
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" className="gap-1.5" disabled={saving}>
            {saving && <Loader2 className="size-4 animate-spin" />}
            {saving ? 'Adding…' : 'Add repository'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
