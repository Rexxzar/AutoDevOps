import { useEffect, useRef } from 'react'
import { useSSESubscribe } from '@/lib/sse-provider'

// useSSE registers a handler on the single app-wide SSE connection owned by
// SSEProvider. The `token` parameter is retained for call-site compatibility;
// authentication and the connection itself are handled once by the provider,
// so this hook only adds/removes a subscriber and never opens a socket.
export function useSSE(
  _token: string | null,
  onEvent: (type: string, data: unknown) => void,
) {
  const { subscribe } = useSSESubscribe()
  const cbRef = useRef(onEvent)
  useEffect(() => {
    cbRef.current = onEvent
  })

  useEffect(() => {
    return subscribe((type, data) => cbRef.current(type, data))
  }, [subscribe])
}
