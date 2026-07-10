import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  type ReactNode,
} from 'react'
import { useAuth } from '@/lib/auth'

const API_BASE = '/api'

type Subscriber = (type: string, data: unknown) => void

interface SSEContextValue {
  subscribe: (cb: Subscriber) => () => void
}

const SSEContext = createContext<SSEContextValue | null>(null)

// SSEProvider owns the single /api/events stream for the whole app and fans
// each event out to every subscriber. Previously every useSSE consumer opened
// its own stream: with the always-mounted notification bell plus per-page
// hooks (the Resources page alone opened three), a single view could hold
// several concurrent connections, and under the browser's ~6-per-host
// HTTP/1.1 cap that starved unrelated requests (apply POSTs, a freshly
// mounted page's own stream) until a full refresh reset the pool.
export function SSEProvider({ children }: { children: ReactNode }) {
  const { token } = useAuth()
  const subsRef = useRef<Set<Subscriber>>(new Set())

  useEffect(() => {
    if (!token) return
    let cancelled = false
    let retry: ReturnType<typeof setTimeout> | undefined
    let ac: AbortController | undefined

    async function connect() {
      ac = new AbortController()
      try {
        const res = await fetch(`${API_BASE}/events`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: ac.signal,
        })
        if (!res.body) throw new Error('no response body')
        const reader = res.body.getReader()
        const dec = new TextDecoder()
        let buf = ''
        let eventType = 'message'

        while (!cancelled) {
          const { done, value } = await reader.read()
          if (done) break
          buf += dec.decode(value, { stream: true })
          const lines = buf.split('\n')
          buf = lines.pop()!
          for (const line of lines) {
            if (line.startsWith('event: ')) {
              eventType = line.slice(7).trim()
            } else if (line.startsWith('data: ')) {
              try {
                const data = JSON.parse(line.slice(6))
                subsRef.current.forEach((cb) => cb(eventType, data))
              } catch {
                // ignore malformed JSON
              }
              eventType = 'message'
            }
          }
        }
      } catch {
        // fall through to reconnect
      }
      // Reconnect on any drop (network error or a clean server close, e.g. a
      // server restart) unless we were torn down.
      if (!cancelled) retry = setTimeout(connect, 3000)
    }

    connect()
    return () => {
      cancelled = true
      if (retry) clearTimeout(retry)
      ac?.abort()
    }
  }, [token])

  // subscribe is stable across renders (it only mutates a ref-backed Set), so
  // consumers' effects never re-subscribe on unrelated re-renders.
  const subscribe = useCallback((cb: Subscriber) => {
    subsRef.current.add(cb)
    return () => {
      subsRef.current.delete(cb)
    }
  }, [])

  const value = useMemo(() => ({ subscribe }), [subscribe])
  return <SSEContext.Provider value={value}>{children}</SSEContext.Provider>
}

export function useSSESubscribe(): SSEContextValue {
  const ctx = useContext(SSEContext)
  if (!ctx) throw new Error('useSSE must be used within an SSEProvider')
  return ctx
}
