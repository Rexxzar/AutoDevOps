import { useCallback, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { RefreshCw, FileCode2, Loader2, Database, Cloud, ChevronRight, Upload, AlertCircle } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { FormField, inputCls, selectCls } from '@/components/deploy-form-fields'
import { type TfWorkspace, tfStatusStyle, tfRelativeTime } from '@/lib/terraform'

interface CloudAccount { id: string; name: string; provider: string }

export function TerraformPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { token } = useAuth()
  const [importOpen, setImportOpen] = useState(false)

  const { data: workspaces = [], isLoading, refetch, isFetching } = useQuery({
    queryKey: ['terraform-workspaces'],
    queryFn: () => apiFetch<TfWorkspace[]>('/terraform/workspaces'),
    refetchInterval: 15_000,
  })

  useSSE(token, useCallback((type: string) => {
    if (type === 'resource' || type === 'terraform.run') {
      qc.invalidateQueries({ queryKey: ['terraform-workspaces'] })
    }
  }, [qc]))

  return (
    <div>
      <PageHeader
        title="Terraform"
        description="Every provisioned resource's Terraform workspace — view, edit, download, export, and manage state."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
        <Button size="sm" className="gap-1.5" onClick={() => setImportOpen(true)}>
          <Upload className="size-3.5" />
          Import Terraform
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6">
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          {isLoading && (
            <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> Loading workspaces…
            </div>
          )}
          {!isLoading && workspaces.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-16 text-center">
              <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                <FileCode2 className="size-4 text-muted-foreground" />
              </div>
              <p className="max-w-xs text-sm text-muted-foreground text-pretty">
                No Terraform workspaces yet. Provision a cloud resource — its generated Terraform shows up here.
              </p>
            </div>
          )}
          {workspaces.map((ws, i) => (
            <button
              key={ws.id}
              onClick={() => navigate(`/terraform/${ws.id}`)}
              className={cn(
                'flex w-full flex-col gap-3 px-4 py-4 text-left transition-colors hover:bg-accent/40 lg:flex-row lg:items-center lg:gap-4 lg:px-5',
                i !== workspaces.length - 1 && 'border-b border-border',
              )}
            >
              <div className="flex min-w-0 items-center gap-3 lg:w-72">
                <div className="flex size-8 shrink-0 items-center justify-center rounded-md border border-border bg-background">
                  <FileCode2 className="size-4 text-muted-foreground" />
                </div>
                <div className="min-w-0">
                  <p className="truncate text-sm font-semibold">{ws.name}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {ws.provider} · {ws.resource_type}
                  </p>
                </div>
              </div>

              <div className="flex min-w-0 flex-1 items-center gap-3 text-xs text-muted-foreground">
                <span className={cn('rounded border px-1.5 py-0.5 font-medium capitalize', tfStatusStyle[ws.status] ?? 'border-border bg-muted/50')}>
                  {ws.status}
                </span>
                {ws.kind === 'imported' && (
                  <span className="rounded border border-info/25 bg-info/10 px-1.5 py-0.5 font-medium text-info">imported</span>
                )}
                <span className="flex items-center gap-1">
                  <Database className="size-3" /> {ws.has_state ? 'state present' : 'no state'}
                </span>
                {ws.run_count > 0 && (
                  <span className="flex items-center gap-1"><Cloud className="size-3" /> {ws.run_count} run{ws.run_count > 1 ? 's' : ''}</span>
                )}
              </div>

              <div className="flex shrink-0 items-center gap-3">
                {ws.last_run && (
                  <span className="hidden text-xs text-muted-foreground sm:inline">{ws.last_run}</span>
                )}
                <span className="hidden w-16 text-right text-xs text-muted-foreground md:inline">
                  {tfRelativeTime(ws.last_run_at ?? ws.updated_at)}
                </span>
                <ChevronRight className="size-4 text-muted-foreground" />
              </div>
            </button>
          ))}
        </div>
      </div>

      {importOpen && (
        <ImportDialog
          onClose={() => setImportOpen(false)}
          onImported={(id) => { setImportOpen(false); navigate(`/terraform/${id}`) }}
        />
      )}
    </div>
  )
}

function ImportDialog({ onClose, onImported }: { onClose: () => void; onImported: (id: string) => void }) {
  const [name, setName] = useState('')
  const [hcl, setHcl] = useState('')
  const [accountId, setAccountId] = useState('')
  const [region, setRegion] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const { data: accounts = [] } = useQuery({
    queryKey: ['cloud-accounts'],
    queryFn: () => apiFetch<CloudAccount[]>('/cloud/accounts'),
  })

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const resp = await apiFetch<{ id: string }>('/terraform/import', {
        method: 'POST',
        body: JSON.stringify({ name: name.trim(), hcl, cloud_account_id: accountId, region: region.trim() }),
      })
      onImported(resp.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Import failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onClose={onClose} className="max-w-2xl">
      <form onSubmit={submit} className="space-y-4 p-5">
        <div>
          <p className="text-sm font-semibold">Import Terraform</p>
          <p className="text-xs text-muted-foreground">Bring your own <code>.tf</code>. AutoDevOps scans it (tfsec) and plans it; it runs with the selected account&apos;s credentials.</p>
        </div>

        <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 text-xs text-warning">
          <AlertCircle className="size-4 shrink-0" />
          <span>This Terraform executes with your cloud credentials and can run provisioners (e.g. <code>local-exec</code> shell commands). Only import configs you trust.</span>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <FormField label="Name">
            <input value={name} onChange={e => setName(e.target.value)} placeholder="my-stack" required className={inputCls} />
          </FormField>
          <FormField label="Region" hint="optional">
            <input value={region} onChange={e => setRegion(e.target.value)} placeholder="us-east-1" className={inputCls} />
          </FormField>
        </div>
        <FormField label="Cloud account" hint="provides provider credentials">
          <select value={accountId} onChange={e => setAccountId(e.target.value)} required className={selectCls}>
            <option value="">Select an account…</option>
            {accounts.map((a) => (
              <option key={a.id} value={a.id}>{a.name} ({a.provider})</option>
            ))}
          </select>
        </FormField>
        <FormField label="Terraform (HCL)">
          <textarea value={hcl} onChange={e => setHcl(e.target.value)} required spellCheck={false}
            placeholder={'resource "aws_s3_bucket" "example" {\n  bucket = "my-bucket"\n}'}
            className="h-56 w-full resize-none rounded-md border border-border bg-background p-3 font-mono text-xs outline-none focus:ring-1 focus:ring-ring" />
        </FormField>

        {error && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{error}
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" className="gap-1.5" disabled={saving || !accountId}>
            {saving && <Loader2 className="size-4 animate-spin" />}
            {saving ? 'Importing…' : 'Import & plan'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
