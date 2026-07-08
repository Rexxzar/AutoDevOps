import { useState, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft, FileCode2, Loader2, Download, Package, Trash2, Save, Play,
  Database, ExternalLink, AlertCircle,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { type TfRun, tfStatusStyle, tfRelativeTime } from '@/lib/terraform'
import { severityStyle, type RepoFinding } from '@/lib/repos'

interface ResourceInfo {
  id: string
  name: string
  provider: string
  resource_type: string
  template: string
  status: string
}

// downloadFile fetches an authenticated endpoint and triggers a browser save.
async function downloadFile(path: string, filename: string) {
  const token = localStorage.getItem('autodevops_token')
  const res = await fetch(`/api${path}`, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (!res.ok) throw new Error(`download failed (${res.status})`)
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

export function TerraformDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { token } = useAuth()

  const [content, setContent] = useState('')
  const [original, setOriginal] = useState('')
  const [planning, setPlanning] = useState(false)
  const [applying, setApplying] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const { data: resource, isLoading: loadingResource } = useQuery({
    queryKey: ['resource', id],
    queryFn: () => apiFetch<ResourceInfo>(`/resources/${id}`),
  })

  const { isLoading: loadingConfig } = useQuery({
    queryKey: ['resource-terraform', id],
    queryFn: async () => {
      const d = await apiFetch<{ config: string }>(`/resources/${id}/terraform`)
      setContent(d.config)
      setOriginal(d.config)
      return d
    },
    retry: false,
  })

  const { data: allRuns = [] } = useQuery({
    queryKey: ['terraform-runs'],
    queryFn: () => apiFetch<TfRun[]>('/terraform/runs'),
    refetchInterval: 10_000,
  })
  const runs = allRuns.filter((r) => r.resource_id === id)

  const { data: findings = [] } = useQuery({
    queryKey: ['resource-findings', id],
    queryFn: () => apiFetch<RepoFinding[]>(`/resources/${id}/findings`),
    refetchInterval: 15_000,
  })

  useSSE(token, useCallback((type: string) => {
    if (type === 'resource' || type === 'terraform.run') {
      qc.invalidateQueries({ queryKey: ['resource', id] })
      qc.invalidateQueries({ queryKey: ['terraform-runs'] })
    }
  }, [qc, id]))

  const dirty = content !== original
  const imported = resource?.template === 'imported'

  async function planChanges() {
    setPlanning(true); setError(''); setNotice('')
    try {
      await apiFetch(`/resources/${id}/terraform/plan`, { method: 'POST', body: JSON.stringify({ config: content }) })
      setOriginal(content)
      setNotice('Plan started, watch the run history below for the result.')
    } catch (e) { setError(String(e)) } finally { setPlanning(false) }
  }

  async function apply() {
    setApplying(true); setError(''); setNotice('')
    try {
      await apiFetch(`/resources/${id}/apply`, { method: 'POST' })
      setNotice('Apply started, watch the run history below.')
    } catch (e) { setError(String(e)) } finally { setApplying(false) }
  }

  async function deleteWorkspace() {
    if (!confirm('Delete this Terraform workspace (config + state)? This is irreversible. Only do this after the resource is destroyed.')) return
    setDeleting(true); setError('')
    try {
      await apiFetch(`/resources/${id}/terraform/workspace`, { method: 'DELETE' })
      navigate('/terraform')
    } catch (e) { setError(String(e)); setDeleting(false) }
  }

  if (loadingResource || !resource) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" /> Loading workspace…
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      {/* Header */}
      <div className="border-b border-border px-4 py-4 lg:px-6">
        <button onClick={() => navigate('/terraform')} className="mb-3 flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground">
          <ArrowLeft className="size-4" /> Terraform
        </button>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <div className="flex size-8 items-center justify-center rounded-md border border-border bg-background">
                <FileCode2 className="size-4 text-muted-foreground" />
              </div>
              <h1 className="truncate text-xl font-semibold tracking-tight">{resource.name}</h1>
              <span className={cn('rounded border px-1.5 py-0.5 text-xs font-medium capitalize', tfStatusStyle[resource.status] ?? 'border-border bg-muted/50')}>
                {resource.status}
              </span>
              {imported && <span className="rounded border border-info/25 bg-info/10 px-1.5 py-0.5 text-xs font-medium text-info">imported</span>}
              <span className="text-xs text-muted-foreground">{resource.provider} · {resource.resource_type}</span>
            </div>
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" className="gap-1.5" onClick={() => downloadFile(`/resources/${id}/terraform/download`, `${resource.name}.tf`)}>
              <Download className="size-3.5" /> Download .tf
            </Button>
            <Button variant="outline" size="sm" className="gap-1.5" onClick={() => downloadFile(`/resources/${id}/terraform/export`, `${resource.name}-terraform.zip`)}>
              <Package className="size-3.5" /> Export
            </Button>
            <Button variant="outline" size="sm" className="gap-1.5" onClick={() => navigate(`/resources/${id}`)}>
              <ExternalLink className="size-3.5" /> Resource
            </Button>
            <Button variant="outline" size="sm" className="gap-1.5 text-critical hover:text-critical" onClick={deleteWorkspace} disabled={deleting || resource.status !== 'destroyed'}
              title={resource.status !== 'destroyed' ? 'Destroy the resource first' : 'Delete workspace files'}>
              {deleting ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />} Delete workspace
            </Button>
          </div>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-4 lg:p-6 space-y-4">
        {imported && (
          <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-xs text-warning">
            <AlertCircle className="size-4 shrink-0" />
            <span>Imported Terraform runs with your cloud credentials and can execute provisioners (e.g. <code>local-exec</code>). Review it before applying, you are running your own code.</span>
          </div>
        )}
        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        {notice && (
          <div className="rounded-lg border border-info/25 bg-info/8 px-3 py-2.5 text-sm text-info">{notice}</div>
        )}

        {/* State status */}
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-card px-4 py-3 text-sm">
          <Database className="size-4 text-muted-foreground" />
          <span className="text-muted-foreground">State:</span>
          <span className="font-medium capitalize">{resource.status === 'applied' ? 'provisioned' : resource.status}</span>
          <span className="ml-auto text-xs text-muted-foreground">State contents are hidden, they can contain secrets.</span>
        </div>

        {/* Config editor */}
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
            <p className="text-sm font-medium">main.tf</p>
            <div className="flex items-center gap-2">
              <Button size="sm" variant="outline" className="h-7 gap-1.5 text-xs" disabled={planning || !dirty} onClick={planChanges}>
                {planning ? <Loader2 className="size-3 animate-spin" /> : <Save className="size-3" />} Plan changes
              </Button>
              <Button size="sm" className="h-7 gap-1.5 text-xs" disabled={applying} onClick={apply}>
                {applying ? <Loader2 className="size-3 animate-spin" /> : <Play className="size-3" />} Apply
              </Button>
            </div>
          </div>
          {loadingConfig ? (
            <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> Loading config…
            </div>
          ) : (
            <textarea
              className="h-[44vh] w-full resize-none bg-background p-3 font-mono text-xs outline-none"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              spellCheck={false}
            />
          )}
        </div>

        {/* IaC security (tfsec) */}
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
            <p className="text-sm font-medium">IaC security (tfsec)</p>
            <span className="text-xs text-muted-foreground">report-only, scanned on plan</span>
          </div>
          {findings.length === 0 ? (
            <p className="px-4 py-6 text-center text-sm text-muted-foreground">No tfsec findings, this configuration looks clean.</p>
          ) : (
            <div className="divide-y divide-border">
              {findings.map((f, i) => (
                <div key={i} className="flex flex-col gap-1 px-4 py-2.5 text-xs sm:flex-row sm:items-start sm:gap-3">
                  <span className={cn('shrink-0 rounded border px-1.5 py-0.5 font-semibold uppercase', severityStyle[f.severity])}>{f.severity}</span>
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

        {/* Run history */}
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          <div className="border-b border-border px-4 py-2.5">
            <p className="text-sm font-medium">Run history</p>
          </div>
          {runs.length === 0 ? (
            <p className="px-4 py-6 text-center text-sm text-muted-foreground">No runs yet.</p>
          ) : (
            <div className="divide-y divide-border">
              {runs.map((run) => (
                <div key={run.id} className="flex items-center gap-3 px-4 py-2.5 text-xs">
                  <span className="w-16 font-medium capitalize">{run.action}</span>
                  <span className={cn('rounded border px-1.5 py-0.5 font-medium capitalize', tfStatusStyle[run.status] ?? 'border-border bg-muted/50')}>
                    {run.status}
                  </span>
                  <span className="ml-auto text-muted-foreground">{tfRelativeTime(run.created_at)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
