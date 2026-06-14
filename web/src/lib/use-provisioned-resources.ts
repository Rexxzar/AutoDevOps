import { useState, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { useSSE } from '@/lib/use-sse'
import { useAuth } from '@/lib/auth'
import { type ProvisionedResource, type TerraformRun } from '@/lib/resources'

// useProvisionedResources centralizes the /resources + /terraform/runs
// fetch/SSE/action logic shared by the Cloud Resources and Local Resources
// pages — both render a ProvisionedTable filtered to a different `provider`.
export function useProvisionedResources() {
  const { token } = useAuth()
  const [provisioned, setProvisioned] = useState<ProvisionedResource[]>([])
  const [runs, setRuns] = useState<TerraformRun[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)
  const [actingOn, setActingOn] = useState<Record<string, 'apply' | 'destroy'>>({})

  const { isLoading: provisionedLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: async () => {
      const data = await apiFetch<ProvisionedResource[]>('/resources')
      setProvisioned(data)
      return data
    },
    refetchInterval: 30_000,
  })

  useQuery({
    queryKey: ['terraform-runs'],
    queryFn: async () => {
      const data = await apiFetch<TerraformRun[]>('/terraform/runs')
      setRuns(data)
      return data
    },
    refetchInterval: 30_000,
  })

  useSSE(token, useCallback((type: string, data: unknown) => {
    if (type === 'resource') {
      const r = data as ProvisionedResource
      setProvisioned((prev) => {
        const idx = prev.findIndex((x) => x.id === r.id)
        if (idx >= 0) {
          const next = [...prev]
          next[idx] = r
          return next
        }
        return [r, ...prev]
      })
    } else if (type === 'terraform.run') {
      const run = data as TerraformRun
      setRuns((prev) => {
        const idx = prev.findIndex((x) => x.id === run.id)
        if (idx >= 0) {
          const next = [...prev]
          next[idx] = run
          return next
        }
        return [run, ...prev]
      })
    }
  }, []))

  async function handleApply(id: string) {
    setActingOn((prev) => ({ ...prev, [id]: 'apply' }))
    try {
      await apiFetch(`/resources/${id}/apply`, { method: 'POST' })
    } catch { /* status surfaces via "resource" SSE event */ }
    finally {
      setActingOn((prev) => { const next = { ...prev }; delete next[id]; return next })
    }
  }

  async function handleDestroy(id: string) {
    setActingOn((prev) => ({ ...prev, [id]: 'destroy' }))
    try {
      await apiFetch(`/resources/${id}/destroy`, { method: 'POST' })
    } catch { /* status surfaces via "resource" SSE event */ }
    finally {
      setActingOn((prev) => { const next = { ...prev }; delete next[id]; return next })
    }
  }

  async function handleDismiss(id: string) {
    try {
      await apiFetch(`/resources/${id}/dismiss`, { method: 'POST' })
      setProvisioned((prev) => prev.filter((p) => p.id !== id))
    } catch { /* leave it listed if the dismiss failed */ }
  }

  return {
    provisioned, runs, expanded, setExpanded, actingOn, provisionedLoading,
    handleApply, handleDestroy, handleDismiss,
  }
}
