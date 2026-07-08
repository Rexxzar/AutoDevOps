import type { StatusKind } from '@/components/ui/status'

export type Provider = 'aws' | 'azure' | 'kind' | 'local'
export type Target = 'kubernetes' | 'docker' | 'vm'
export type Framework = 'node' | 'go' | 'python' | 'java' | 'static' | string

export type PipelineStage = {
  name: string
  status: 'done' | 'active' | 'pending' | 'failed' | 'skipped'
  durationMs?: number
}

export type ComplianceFinding = {
  scanner: 'hadolint' | 'trivy' | 'tfsec' | 'gitleaks'
  severity: 'critical' | 'high' | 'medium' | 'low'
  rule: string
  message: string
  location: string
}

export type Deployment = {
  id: string
  name: string
  repo: string
  branch: string
  commit: string
  commitMessage: string
  author: string
  framework: Framework
  target: Target
  provider: Provider
  status: StatusKind
  url?: string
  createdAt: string
  durationMs: number
  stages: PipelineStage[]
  complianceGate: 'passed' | 'failed' | 'running'
  findings: ComplianceFinding[]
  replicas: { ready: number; desired: number }
}

export type Pod = {
  name: string
  status: StatusKind
  restarts: number
  cpu: number
  memory: number
  node: string
  age: string
  ready: boolean
  namespace?: string
  reason?: string
  appName?: string
}

export function extractAppName(podName: string): string {
  const parts = podName.split('-')
  if (parts.length >= 3) {
    return parts.slice(0, -2).join('-')
  }
  return podName
}

export type Cluster = {
  id: string
  name: string
  provider: Provider
  status: StatusKind
  version: string
  nodes: number
  namespace: string
  pods: Pod[]
}

export type HealEvent = {
  id: string
  ts: string
  cluster: string
  pod: string
  action: string
  detail: string
  tone: 'success' | 'warning' | 'critical' | 'info'
  backoff?: string
}

export const initialHealEvents: HealEvent[] = [
  { id: 'h1', ts: '-', cluster: 'waiting', pod: '-', action: 'Waiting for heal events', detail: 'Self-healing controller will stream events here in real time', tone: 'info' },
]
