import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Boxes, Package, RefreshCw, Download, Trash2, Loader2, AlertCircle } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { FormField, inputCls } from '@/components/deploy-form-fields'
import { apiFetch } from '@/lib/api'
import { formatRelativeTime } from '@/lib/resources'
import { cn } from '@/lib/utils'

interface ImageInfo {
  repo: string
  tag: string
  size_bytes: number
  created_at: string
  built_via: 'paketo' | 'docker'
}

function formatSize(bytes: number): string {
  if (bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

const builtViaLabel: Record<ImageInfo['built_via'], string> = {
  paketo: 'Paketo',
  docker: 'Dockerfile',
}

export function ImageRegistryPage() {
  const qc = useQueryClient()
  const { data, isLoading, refetch, isFetching } = useQuery({
    queryKey: ['registry-images'],
    queryFn: () => apiFetch<ImageInfo[]>('/registry/images'),
    refetchInterval: 60_000,
  })

  const images = useMemo(
    () =>
      [...(data ?? [])].sort((a, b) =>
        a.repo === b.repo
          ? b.created_at.localeCompare(a.created_at)
          : a.repo.localeCompare(b.repo),
      ),
    [data],
  )

  // ── Import ──────────────────────────────────────────────────────
  const [importOpen, setImportOpen] = useState(false)
  const [importSource, setImportSource] = useState('')
  const [importRepo, setImportRepo] = useState('')
  const [importTag, setImportTag] = useState('')
  const [importLoading, setImportLoading] = useState(false)
  const [importError, setImportError] = useState('')

  function openImport() {
    setImportSource('')
    setImportRepo('')
    setImportTag('')
    setImportError('')
    setImportOpen(true)
  }

  async function handleImport(e: React.FormEvent) {
    e.preventDefault()
    setImportLoading(true)
    setImportError('')
    try {
      await apiFetch('/registry/images/import', {
        method: 'POST',
        body: JSON.stringify({
          source: importSource.trim(),
          repo: importRepo.trim() || undefined,
          tag: importTag.trim() || undefined,
        }),
      })
      setImportOpen(false)
      qc.invalidateQueries({ queryKey: ['registry-images'] })
    } catch (err) {
      setImportError(err instanceof Error ? err.message : 'Failed to import image')
    } finally {
      setImportLoading(false)
    }
  }

  // ── Delete ──────────────────────────────────────────────────────
  const [deleteTarget, setDeleteTarget] = useState<ImageInfo | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)
  const [deleteError, setDeleteError] = useState('')

  async function handleDelete() {
    if (!deleteTarget) return
    setDeleteLoading(true)
    setDeleteError('')
    try {
      const params = new URLSearchParams({ repo: deleteTarget.repo, tag: deleteTarget.tag })
      await apiFetch(`/registry/images?${params.toString()}`, { method: 'DELETE' })
      setDeleteTarget(null)
      qc.invalidateQueries({ queryKey: ['registry-images'] })
    } catch (err) {
      setDeleteError(err instanceof Error ? err.message : 'Failed to delete image')
    } finally {
      setDeleteLoading(false)
    }
  }

  return (
    <div>
      <PageHeader
        title="Image Registry"
        description="Images in the local container registry, built via Dockerfile or Paketo Buildpacks."
      >
        <Button variant="outline" size="sm" className="gap-1.5" onClick={openImport}>
          <Download className="size-3.5" />
          Import image
        </Button>
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn('size-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-6">
        <Card className="overflow-hidden">
          {isLoading && (
            <div className="py-12 text-center text-sm text-muted-foreground">Loading images…</div>
          )}
          {!isLoading && images.length === 0 && (
            <div className="flex flex-col items-center gap-3 py-16 text-center">
              <div className="flex size-10 items-center justify-center rounded-lg border border-border bg-card">
                <Boxes className="size-4 text-muted-foreground" />
              </div>
              <p className="text-sm text-muted-foreground max-w-xs text-pretty">
                No images in the local registry yet. Trigger a deployment to build and push one, or import one.
              </p>
            </div>
          )}
          {images.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full table-fixed text-sm">
                <thead>
                  <tr className="border-b border-border">
                    {[
                      ['Repository', ''],
                      ['Tag', 'w-40'],
                      ['Built via', 'w-28'],
                      ['Size', 'w-24'],
                      ['Created', 'w-24'],
                      ['', 'w-12'],
                    ].map(([h, width]) => (
                      <th key={h} className={cn('px-4 py-3 text-left text-[0.7rem] font-medium uppercase tracking-wide text-muted-foreground', width)}>
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {images.map((img) => (
                    <tr key={`${img.repo}:${img.tag}`} className="hover:bg-accent/30 transition-colors">
                      <td className="px-4 py-3 overflow-hidden">
                        <div className="flex items-center gap-1.5">
                          <Package className="size-3.5 shrink-0 text-muted-foreground" />
                          <span className="truncate font-medium">{img.repo}</span>
                        </div>
                      </td>
                      <td className="px-4 py-3 truncate font-mono text-xs text-muted-foreground">{img.tag}</td>
                      <td className="px-4 py-3">
                        <Badge variant="outline">{builtViaLabel[img.built_via] ?? img.built_via}</Badge>
                      </td>
                      <td className="px-4 py-3 text-xs text-muted-foreground">{formatSize(img.size_bytes)}</td>
                      <td className="px-4 py-3 text-xs text-muted-foreground" title={new Date(img.created_at).toLocaleString()}>
                        {formatRelativeTime(img.created_at)}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <button
                          onClick={() => { setDeleteError(''); setDeleteTarget(img) }}
                          className="rounded p-1.5 text-muted-foreground hover:bg-critical/10 hover:text-critical transition-colors"
                          title="Delete image"
                        >
                          <Trash2 className="size-3.5" />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>

      {/* ── Import dialog ── */}
      <Dialog open={importOpen} onClose={() => { if (!importLoading) setImportOpen(false) }} title="Import image">
        <form onSubmit={handleImport} className="p-4 space-y-4">
          <FormField label="Source image" hint="from Docker Hub or another registry">
            <input
              value={importSource}
              onChange={(e) => setImportSource(e.target.value)}
              placeholder="postgres:16"
              required
              className={cn(inputCls, 'font-mono')}
            />
          </FormField>
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Repository" hint="optional">
              <input
                value={importRepo}
                onChange={(e) => setImportRepo(e.target.value)}
                placeholder="(from source)"
                className={cn(inputCls, 'font-mono')}
              />
            </FormField>
            <FormField label="Tag" hint="optional">
              <input
                value={importTag}
                onChange={(e) => setImportTag(e.target.value)}
                placeholder="(from source)"
                className={cn(inputCls, 'font-mono')}
              />
            </FormField>
          </div>
          {importError && (
            <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertCircle className="size-4 shrink-0" />{importError}
            </div>
          )}
          <p className="text-[0.7rem] text-muted-foreground">
            Pulls the image and pushes it into the local registry — this can take a minute for large images.
          </p>
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="outline" onClick={() => setImportOpen(false)} disabled={importLoading}>
              Cancel
            </Button>
            <Button type="submit" size="sm" className="gap-1.5" disabled={importLoading || !importSource.trim()}>
              {importLoading && <Loader2 className="size-3.5 animate-spin" />}
              {importLoading ? 'Importing…' : 'Import'}
            </Button>
          </div>
        </form>
      </Dialog>

      {/* ── Delete confirm dialog ── */}
      <Dialog open={!!deleteTarget} onClose={() => { if (!deleteLoading) setDeleteTarget(null) }} title="Delete image">
        <div className="p-4 space-y-4">
          <p className="text-sm text-muted-foreground">
            Delete <span className="font-mono font-medium text-foreground">{deleteTarget?.repo}:{deleteTarget?.tag}</span> from the local registry?
            Any other tags pointing at the same image content will also be removed. This cannot be undone.
          </p>
          {deleteError && (
            <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertCircle className="size-4 shrink-0" />{deleteError}
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="outline" onClick={() => setDeleteTarget(null)} disabled={deleteLoading}>
              Cancel
            </Button>
            <Button size="sm" variant="destructive" className="gap-1.5" onClick={handleDelete} disabled={deleteLoading}>
              {deleteLoading && <Loader2 className="size-3.5 animate-spin" />}
              Delete
            </Button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}
