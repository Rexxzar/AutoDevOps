import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Bell, Check, CheckCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'
import { relativeTime } from '@/lib/repos'

interface Notification {
  id: string
  type: string
  severity: 'info' | 'success' | 'warning' | 'critical'
  title: string
  body?: string
  link?: string
  read: boolean
  created_at: string
}

const dotColor: Record<Notification['severity'], string> = {
  critical: 'bg-critical',
  warning: 'bg-warning',
  success: 'bg-success',
  info: 'bg-info',
}

export function NotificationBell() {
  const { token } = useAuth()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  const { data: notifications = [] } = useQuery({
    queryKey: ['notifications'],
    queryFn: () => apiFetch<Notification[]>('/notifications?limit=30'),
    refetchInterval: 60_000,
    enabled: !!token,
  })

  const unread = notifications.filter((n) => !n.read).length

  // Live updates: any new notification refreshes the list.
  useSSE(token, useCallback((type: string) => {
    if (type === 'notification') qc.invalidateQueries({ queryKey: ['notifications'] })
  }, [qc]))

  // Close on outside click.
  useEffect(() => {
    if (!open) return
    function onClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onClick)
    return () => document.removeEventListener('mousedown', onClick)
  }, [open])

  async function markRead(ids: string[]) {
    if (ids.length === 0) return
    try {
      await apiFetch('/notifications/read', { method: 'POST', body: JSON.stringify({ ids }) })
      qc.invalidateQueries({ queryKey: ['notifications'] })
    } catch { /* best-effort */ }
  }

  async function markAllRead() {
    try {
      await apiFetch('/notifications/read', { method: 'POST', body: JSON.stringify({ all: true }) })
      qc.invalidateQueries({ queryKey: ['notifications'] })
    } catch { /* best-effort */ }
  }

  function openItem(n: Notification) {
    if (!n.read) markRead([n.id])
    if (n.link) {
      navigate(n.link)
      setOpen(false)
    }
  }

  return (
    <div className="relative" ref={ref}>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="Notifications"
        className="relative"
        onClick={() => setOpen((o) => !o)}
      >
        <Bell className="size-4" />
        {unread > 0 && (
          unread > 9
            ? <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-critical px-1 font-mono text-[0.55rem] font-bold text-white">9+</span>
            : <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-critical px-1 font-mono text-[0.55rem] font-bold text-white">{unread}</span>
        )}
      </Button>

      {open && (
        <div className="absolute right-0 top-full z-50 mt-2 w-80 overflow-hidden rounded-lg border border-border bg-card shadow-lg">
          <div className="flex items-center justify-between border-b border-border px-3 py-2">
            <span className="text-sm font-semibold">Notifications</span>
            {unread > 0 && (
              <button
                onClick={markAllRead}
                className="flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
              >
                <CheckCheck className="size-3.5" /> Mark all read
              </button>
            )}
          </div>

          <div className="max-h-96 overflow-y-auto">
            {notifications.length === 0 && (
              <div className="px-3 py-10 text-center text-sm text-muted-foreground">No notifications yet.</div>
            )}
            {notifications.map((n) => (
              <button
                key={n.id}
                onClick={() => openItem(n)}
                className={cn(
                  'flex w-full gap-2.5 border-b border-border px-3 py-2.5 text-left transition-colors last:border-0 hover:bg-accent/50',
                  !n.read && 'bg-accent/30',
                )}
              >
                <span className={cn('mt-1.5 size-2 shrink-0 rounded-full', dotColor[n.severity] ?? 'bg-muted-foreground')} />
                <div className="min-w-0 flex-1">
                  <p className={cn('truncate text-sm', !n.read && 'font-medium')}>{n.title}</p>
                  {n.body && <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{n.body}</p>}
                  <p className="mt-0.5 text-[0.65rem] text-muted-foreground">{relativeTime(n.created_at)}</p>
                </div>
                {!n.read && <Check className="mt-1 size-3.5 shrink-0 text-muted-foreground opacity-0 group-hover:opacity-100" />}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
