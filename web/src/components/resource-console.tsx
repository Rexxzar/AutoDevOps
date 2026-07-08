import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { SquareTerminal, Play, Loader2, AlertTriangle } from 'lucide-react'
import { Dialog } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'

const API_BASE = '/api'

interface ConsoleProps {
  resourceId: string
  resourceName: string
  open: boolean
  onClose: () => void
}

type Tab = 'terminal' | 'exec'

// ResourceConsole is the T25 "Connect" dialog: a Terminal tab (interactive
// shell over the T24 SSE+POST PTY bridge) and a Run command tab (one-shot
// POST /console/exec). See design notes Dev Notes: "Resource Console design".
export function ResourceConsole({ resourceId, resourceName, open, onClose }: ConsoleProps) {
  const [tab, setTab] = useState<Tab>('terminal')

  return (
    <Dialog open={open} onClose={onClose} title={`Console, ${resourceName}`} className="max-w-3xl">
      <div className="flex items-center gap-1 border-b border-border px-4 pt-2">
        <TabButton active={tab === 'terminal'} onClick={() => setTab('terminal')} icon={<SquareTerminal className="size-3.5" />}>
          Terminal
        </TabButton>
        <TabButton active={tab === 'exec'} onClick={() => setTab('exec')} icon={<Play className="size-3.5" />}>
          Run command
        </TabButton>
      </div>
      <div className="p-4">
        {tab === 'terminal' ? <TerminalPanel resourceId={resourceId} /> : <ExecPanel resourceId={resourceId} />}
      </div>
    </Dialog>
  )
}

function TabButton({ active, onClick, icon, children }: { active: boolean; onClick: () => void; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={cn(
        'flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors',
        active ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground',
      )}
    >
      {icon}
      {children}
    </button>
  )
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return bytes
}

// streamOutput relays the SSE PTY-output stream into term until the session
// closes or signal aborts. Output chunks are base64-framed (arbitrary PTY
// bytes aren't safe in raw SSE data lines) and written to xterm as raw bytes
// so it can handle ANSI escapes / partial UTF-8 itself.
async function streamOutput(url: string, token: string | null, term: Terminal, signal: AbortSignal) {
  const res = await fetch(url, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    signal,
  })
  if (!res.body) return
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  let eventType = 'message'
  while (true) {
    const { done, value } = await reader.read()
    if (done) return
    buf += dec.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop()!
    for (const line of lines) {
      if (line.startsWith('event: ')) {
        eventType = line.slice(7).trim()
      } else if (line.startsWith('data: ')) {
        if (eventType === 'output') {
          term.write(base64ToBytes(line.slice(6)))
        } else if (eventType === 'closed') {
          return
        }
        eventType = 'message'
      }
    }
  }
}

function TerminalPanel({ resourceId }: { resourceId: string }) {
  const { token } = useAuth()
  const containerRef = useRef<HTMLDivElement>(null)
  const [error, setError] = useState<string | null>(null)
  const [connecting, setConnecting] = useState(true)

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    let cancelled = false
    let sessionId: string | null = null
    const ac = new AbortController()

    const term = new Terminal({
      convertEol: true,
      fontSize: 13,
      theme: { background: '#0d1117', foreground: '#c9d1d9' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(container)
    fit.fit()

    async function start() {
      try {
        const res = await apiFetch<{ session_id: string }>(`/resources/${resourceId}/console/sessions`, {
          method: 'POST',
          body: JSON.stringify({ rows: term.rows, cols: term.cols }),
        })
        if (cancelled) return
        sessionId = res.session_id

        term.onData((data) => {
          apiFetch(`/resources/${resourceId}/console/sessions/${sessionId}/input`, {
            method: 'POST',
            body: JSON.stringify({ data }),
          }).catch(() => {})
        })

        setConnecting(false)
        await streamOutput(`${API_BASE}/resources/${resourceId}/console/sessions/${sessionId}/stream`, token, term, ac.signal)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to open console session')
      } finally {
        if (!cancelled) setConnecting(false)
      }
    }
    // Defer the session open by a tick so StrictMode's dev-mode double-invoke
    // (mount -> cleanup -> mount) clears this timer before it fires for the
    // throwaway first invocation, otherwise both invocations would push a
    // separate ephemeral key via Azure Run Command / EC2 Instance Connect and
    // race each other, leaving one signer's key never written.
    const startTimer = setTimeout(start, 0)

    const ro = new ResizeObserver(() => {
      fit.fit()
      if (sessionId) {
        apiFetch(`/resources/${resourceId}/console/sessions/${sessionId}/resize`, {
          method: 'POST',
          body: JSON.stringify({ rows: term.rows, cols: term.cols }),
        }).catch(() => {})
      }
    })
    ro.observe(container)

    return () => {
      cancelled = true
      clearTimeout(startTimer)
      ac.abort()
      ro.disconnect()
      if (sessionId) {
        apiFetch(`/resources/${resourceId}/console/sessions/${sessionId}`, { method: 'DELETE' }).catch(() => {})
      }
      term.dispose()
    }
  }, [resourceId, token])

  return (
    <div className="space-y-2">
      {error && (
        <div className="flex items-start gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
          <AlertTriangle className="size-4 shrink-0 mt-0.5" />
          <span>{error}</span>
        </div>
      )}
      {connecting && !error && <p className="text-xs text-muted-foreground">Connecting…</p>}
      <div ref={containerRef} className="h-96 rounded-lg border border-border bg-[#0d1117] p-2" />
    </div>
  )
}

interface ExecResult {
  stdout: string
  stderr: string
  exit_code: number
}

function ExecPanel({ resourceId }: { resourceId: string }) {
  const [command, setCommand] = useState('')
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<ExecResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function run() {
    if (!command.trim() || running) return
    setRunning(true)
    setError(null)
    setResult(null)
    try {
      const res = await apiFetch<ExecResult>(`/resources/${resourceId}/console/exec`, {
        method: 'POST',
        body: JSON.stringify({ command }),
      })
      setResult(res)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Command failed')
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex gap-2">
        <input
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') run() }}
          placeholder="e.g. uname -a"
          spellCheck={false}
          className="flex-1 rounded-lg border border-border bg-background px-3 py-2 font-mono text-sm focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
        <Button size="sm" className="gap-1.5" disabled={running || !command.trim()} onClick={run}>
          {running ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
          Run
        </Button>
      </div>

      {error && (
        <div className="flex items-start gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
          <AlertTriangle className="size-4 shrink-0 mt-0.5" />
          <span>{error}</span>
        </div>
      )}

      {result && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">
            Exit code:{' '}
            <span className={cn('font-mono', result.exit_code === 0 ? 'text-success' : 'text-critical')}>{result.exit_code}</span>
          </p>
          {result.stdout && (
            <pre className="max-h-64 overflow-auto rounded-lg border border-border bg-card p-3 text-xs font-mono whitespace-pre-wrap">{result.stdout}</pre>
          )}
          {result.stderr && (
            <pre className="max-h-64 overflow-auto rounded-lg border border-critical/25 bg-critical/8 p-3 text-xs font-mono whitespace-pre-wrap text-critical">{result.stderr}</pre>
          )}
          {!result.stdout && !result.stderr && <p className="text-xs text-muted-foreground">(no output)</p>}
        </div>
      )}
    </div>
  )
}
