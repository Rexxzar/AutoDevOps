import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import type { StatusKind } from '@/components/ui/status'

export interface InventoryContainer {
  id: string          // synthetic "docker:<containerID>"
  name: string
  image: string
  state: string       // "running" | "exited" | "created" | "restarting" | "paused" | "removing" | "dead"
  status: string      // human string e.g. "Up 2 hours"
  created_at: string
  managed: boolean
  link_path?: string
  link_label?: string
}

export interface InventoryPod {
  id: string          // synthetic "k8s:<namespace>:<pod>"
  namespace: string
  name: string
  containers: string[]
  image: string
  phase: string       // "Running" | "Pending" | "Succeeded" | "Failed" | "Unknown"
  created_at: string
  managed: boolean
  restartable: boolean
  link_path?: string
  link_label?: string
}

export interface HostInventory {
  docker_available: boolean
  k8s_available: boolean
  containers: InventoryContainer[]
  pods: InventoryPod[]
}

export function containerStateKind(state: string): StatusKind {
  switch (state) {
    case 'running':    return 'running'
    case 'restarting': return 'building'
    case 'created':    return 'pending'
    case 'paused':     return 'idle'
    case 'exited':     return 'idle'
    case 'removing':   return 'building'
    case 'dead':       return 'failed'
    default:           return 'unknown'
  }
}

export function podPhaseKind(phase: string): StatusKind {
  switch (phase) {
    case 'Running':   return 'running'
    case 'Pending':   return 'pending'
    case 'Succeeded': return 'idle'
    case 'Failed':    return 'failed'
    default:          return 'unknown'
  }
}

export function useHostInventory() {
  return useQuery<HostInventory>({
    queryKey: ['host-inventory'],
    queryFn: () => apiFetch<HostInventory>('/local/inventory'),
    refetchInterval: 15_000,
  })
}
