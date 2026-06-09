import { useEffect, useRef } from 'react'

const API_BASE = '/api'

export function useSSE(
  token: string | null,
  onEvent: (type: string, data: unknown) => void,
) {
  const cbRef = useRef(onEvent)
  useEffect(() => {
    cbRef.current = onEvent
  })

  useEffect(() => {
    if (!token) return
    let cancelled = false
    const ac = new AbortController()

    async function connect() {
      try {
        const res = await fetch(`${API_BASE}/events`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: ac.signal,
        })
        if (!res.body) return
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
                cbRef.current(eventType, data)
              } catch {
                // ignore malformed JSON
              }
              eventType = 'message'
            }
          }
        }
      } catch {
        if (cancelled) return
        // reconnect after 3s on error
        setTimeout(connect, 3000)
      }
    }

    connect()
    return () => {
      cancelled = true
      ac.abort()
    }
  }, [token])
}
