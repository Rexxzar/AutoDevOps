import { useState, useEffect } from 'react'
import { RefreshCw } from 'lucide-react'
import { Dialog } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'

interface InventoryLogsDialogProps {
  open: boolean
  onClose: () => void
  inventoryId: string   // synthetic "docker:..." or "k8s:..."
  name: string
  containers: string[]  // container names for multi-container pods; empty for Docker
}

export function InventoryLogsDialog({ open, onClose, inventoryId, name, containers }: InventoryLogsDialogProps) {
  const [logs, setLogs] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [container, setContainer] = useState(containers[0] ?? '')

  async function fetchLogs() {
    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams({ tail: '300' })
      if (container) params.set('container', container)
      const data = await apiFetch<{ logs: string }>(`/local/inventory/${encodeURIComponent(inventoryId)}/logs?${params}`)
      setLogs(data.logs)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (open) fetchLogs()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, container])

  // Reset container selection when the dialog is opened for a new item.
  useEffect(() => {
    setContainer(containers[0] ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inventoryId])

  return (
    <Dialog open={open} onClose={onClose} title={`${name} — logs`} className="max-w-3xl">
      <div className="p-4 space-y-3">
        <div className="flex items-center gap-2">
          {containers.length > 1 && (
            <select
              className="text-xs border border-border rounded px-2 py-1 bg-background"
              value={container}
              onChange={(e) => setContainer(e.target.value)}
            >
              {containers.map((c) => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
          )}
          <Button
            size="sm"
            variant="outline"
            onClick={fetchLogs}
            disabled={loading}
            className="ml-auto flex items-center gap-1"
          >
            <RefreshCw className={`w-3 h-3 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </div>

        {error && (
          <p className="text-xs text-destructive">{error}</p>
        )}

        <pre className="max-h-[28rem] overflow-auto rounded border bg-muted p-3 text-xs font-mono whitespace-pre-wrap break-all">
          {loading && !logs ? 'Loading…' : (logs || '(no output)')}
        </pre>
      </div>
    </Dialog>
  )
}
