import { useState, useEffect, useRef } from 'react'
import { Loader2, Save, GitCommit, FileText, ChevronRight, Plus, X } from 'lucide-react'
import { Dialog } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'

interface RepoEditorProps {
  stackId: string
  appId: string
  appName: string
  hasGitCredentials: boolean
  open: boolean
  onClose: () => void
  onRedeployNeeded?: () => void
}

export function RepoEditor({ stackId, appId, appName, hasGitCredentials, open, onClose, onRedeployNeeded }: RepoEditorProps) {
  const [files, setFiles] = useState<string[]>([])
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const [originalContent, setOriginalContent] = useState('')
  const [loadingFiles, setLoadingFiles] = useState(false)
  const [loadingContent, setLoadingContent] = useState(false)
  const [saving, setSaving] = useState(false)
  const [committing, setCommitting] = useState(false)
  const [commitMsg, setCommitMsg] = useState('')
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [committed, setCommitted] = useState(false)
  const [newFileName, setNewFileName] = useState('')
  const [showNewFile, setShowNewFile] = useState(false)
  const newFileInputRef = useRef<HTMLInputElement>(null)

  const isDirty = content !== originalContent

  useEffect(() => {
    if (!open) return
    setLoadingFiles(true)
    setError('')
    setSelectedFile(null)
    setContent('')
    setOriginalContent('')
    apiFetch<{ files: string[] }>(`/deployments/${stackId}/apps/${appId}/repo/files`)
      .then((d) => setFiles(d.files ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoadingFiles(false))
  }, [open, stackId, appId])

  async function selectFile(path: string) {
    if (isDirty && selectedFile) {
      if (!confirm(`Discard unsaved changes to ${selectedFile}?`)) return
    }
    setSelectedFile(path)
    setContent('')
    setOriginalContent('')
    setSaved(false)
    setLoadingContent(true)
    setError('')
    try {
      const d = await apiFetch<{ content: string }>(`/deployments/${stackId}/apps/${appId}/repo/files/${path}`)
      setContent(d.content)
      setOriginalContent(d.content)
    } catch (e) {
      setError(String(e))
    } finally {
      setLoadingContent(false)
    }
  }

  function openNewFile() {
    setShowNewFile(true)
    setNewFileName('')
    setTimeout(() => newFileInputRef.current?.focus(), 0)
  }

  async function confirmNewFile() {
    const path = newFileName.trim()
    if (!path) return
    if (isDirty && selectedFile) {
      if (!confirm(`Discard unsaved changes to ${selectedFile}?`)) return
    }
    // Save empty file to workspace, then open it in editor
    setError('')
    try {
      await apiFetch(`/deployments/${stackId}/apps/${appId}/repo/files/${path}`, {
        method: 'PUT',
        body: JSON.stringify({ content: '' }),
      })
      setFiles((prev) => prev.includes(path) ? prev : [...prev, path].sort())
      setSelectedFile(path)
      setContent('')
      setOriginalContent('')
      setSaved(false)
    } catch (e) {
      setError(String(e))
    }
    setShowNewFile(false)
    setNewFileName('')
  }

  async function saveFile() {
    if (!selectedFile) return
    setSaving(true)
    setError('')
    try {
      await apiFetch(`/deployments/${stackId}/apps/${appId}/repo/files/${selectedFile}`, {
        method: 'PUT',
        body: JSON.stringify({ content }),
      })
      setOriginalContent(content)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e) {
      setError(String(e))
    } finally {
      setSaving(false)
    }
  }

  async function commitAndPush() {
    setCommitting(true)
    setError('')
    try {
      await apiFetch(`/deployments/${stackId}/apps/${appId}/repo/commit`, {
        method: 'POST',
        body: JSON.stringify({ message: commitMsg || 'edit via AutoDevOps dashboard' }),
      })
      setCommitted(true)
      setCommitMsg('')
      setTimeout(() => setCommitted(false), 3000)
      onRedeployNeeded?.()
    } catch (e) {
      setError(String(e))
    } finally {
      setCommitting(false)
    }
  }

  // Group files into a simple tree structure by directory prefix
  const grouped: Record<string, string[]> = {}
  for (const f of files) {
    const slash = f.indexOf('/')
    const dir = slash === -1 ? '' : f.slice(0, slash)
    if (!grouped[dir]) grouped[dir] = []
    grouped[dir].push(f)
  }
  const dirs = Object.keys(grouped).sort()

  return (
    <Dialog open={open} onClose={onClose} className="max-w-5xl">
      <div className="flex h-[70vh] flex-col">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <div>
            <p className="text-sm font-semibold">Edit files, {appName}</p>
            <p className="text-xs text-muted-foreground">Changes are saved to the local workspace. Commit & push to send them to your repo.</p>
          </div>
        </div>

        {error && (
          <div className="border-b bg-critical/10 px-4 py-2 text-xs text-critical">{error}</div>
        )}

        <div className="flex min-h-0 flex-1">
          {/* File tree */}
          <div className="w-56 shrink-0 overflow-y-auto border-r bg-muted/30 py-2 text-xs">
            {/* New file row */}
            <div className="flex items-center justify-between px-3 pb-1.5 mb-1 border-b">
              <span className="font-medium text-muted-foreground">Files</span>
              <button
                onClick={openNewFile}
                title="New file"
                className="rounded p-0.5 text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
              >
                <Plus className="size-3.5" />
              </button>
            </div>
            {showNewFile && (
              <div className="flex items-center gap-1 px-2 pb-1.5">
                <input
                  ref={newFileInputRef}
                  className="h-6 flex-1 rounded border bg-background px-2 font-mono text-xs focus:outline-none focus:ring-1 focus:ring-ring"
                  placeholder="filename e.g. .env"
                  value={newFileName}
                  onChange={(e) => setNewFileName(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') confirmNewFile()
                    if (e.key === 'Escape') { setShowNewFile(false); setNewFileName('') }
                  }}
                />
                <button onClick={confirmNewFile} className="text-primary hover:text-primary/80"><Plus className="size-3.5" /></button>
                <button onClick={() => { setShowNewFile(false); setNewFileName('') }} className="text-muted-foreground hover:text-foreground"><X className="size-3.5" /></button>
              </div>
            )}
            {loadingFiles ? (
              <div className="flex items-center gap-2 px-3 py-2 text-muted-foreground">
                <Loader2 className="size-3 animate-spin" /> Loading…
              </div>
            ) : files.length === 0 && !showNewFile ? (
              <p className="px-3 py-2 text-muted-foreground">No cloned workspace yet, trigger a deploy first.</p>
            ) : (
              dirs.map((dir) => (
                <div key={dir}>
                  {dir && (
                    <div className="flex items-center gap-1 px-3 py-1 font-medium text-muted-foreground">
                      <ChevronRight className="size-3" />
                      {dir}
                    </div>
                  )}
                  {grouped[dir].map((f) => (
                    <button
                      key={f}
                      onClick={() => selectFile(f)}
                      className={cn(
                        'flex w-full items-center gap-1.5 truncate px-3 py-1 text-left hover:bg-muted',
                        dir ? 'pl-6' : '',
                        selectedFile === f && 'bg-muted font-medium text-foreground',
                      )}
                    >
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
              <div className="flex flex-1 items-center justify-center text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
              </div>
            ) : selectedFile ? (
              <>
                <div className="flex items-center justify-between border-b px-3 py-1.5">
                  <span className="font-mono text-xs text-muted-foreground">{selectedFile}</span>
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-6 gap-1 px-2 text-xs"
                    disabled={saving || !isDirty}
                    onClick={saveFile}
                  >
                    {saving ? <Loader2 className="size-3 animate-spin" /> : <Save className="size-3" />}
                    {saved ? 'Saved!' : 'Save file'}
                  </Button>
                </div>
                <textarea
                  className="flex-1 resize-none bg-background p-3 font-mono text-xs outline-none"
                  value={content}
                  onChange={(e) => setContent(e.target.value)}
                  spellCheck={false}
                />
              </>
            ) : (
              <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
                Select a file to edit
              </div>
            )}
          </div>
        </div>

        {/* Commit bar */}
        {hasGitCredentials ? (
          <div className="flex items-center gap-2 border-t px-4 py-3">
            <input
              className="h-8 flex-1 rounded border bg-background px-3 text-xs placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
              placeholder="Commit message (optional)"
              value={commitMsg}
              onChange={(e) => setCommitMsg(e.target.value)}
            />
            <Button
              size="sm"
              className="gap-1.5"
              disabled={committing}
              onClick={commitAndPush}
            >
              {committing ? <Loader2 className="size-3.5 animate-spin" /> : <GitCommit className="size-3.5" />}
              {committed ? 'Pushed!' : 'Commit & push'}
            </Button>
          </div>
        ) : (
          <div className="border-t px-4 py-3 text-xs text-muted-foreground">
            <span className="font-medium text-foreground">Local edits only</span>, no git credentials stored for this app.
            File saves persist in the workspace until the next deploy re-clones the repo.
            To enable commit &amp; push, delete this app and re-add it with a git token.
          </div>
        )}
      </div>
    </Dialog>
  )
}
