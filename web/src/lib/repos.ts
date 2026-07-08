// Git Repo Store, shared types + helpers for the Repositories pages.

export type RepoSeverity = 'critical' | 'high' | 'medium' | 'low' | 'info'

export interface GitRepo {
  id: string
  name: string
  repo_url: string
  branch: string
  auth_type: string // "" | "token" | "ssh_key"
  has_credentials: boolean
  last_scanned_at: string | null
  used_by: number
  findings: Partial<Record<RepoSeverity, number>>
  created_at: string
}

export interface RepoFinding {
  scanner: 'hadolint' | 'trivy' | 'tfsec' | 'gitleaks'
  severity: RepoSeverity
  rule_id: string
  message: string
  file_path: string
  line_number: number
}

export interface RepoAppUsage {
  app_id: string
  app_name: string
  status: string
  deployment_id: string
  deployment_name: string
}

export interface RepoDetail {
  repo: GitRepo
  used_by: RepoAppUsage[]
  findings: RepoFinding[]
}

export interface RepoScanResult {
  findings: RepoFinding[]
  blocked: boolean
  gate_message?: string
}

export interface RefreshResult {
  updated: boolean
  old_sha?: string
  new_sha?: string
  commit?: { sha: string; message: string; author: string; date: string }
}

export const severityOrder: RepoSeverity[] = ['critical', 'high', 'medium', 'low', 'info']

export const severityStyle: Record<RepoSeverity, string> = {
  critical: 'text-critical border-critical/25 bg-critical/10',
  high: 'text-warning border-warning/25 bg-warning/10',
  medium: 'text-info border-info/25 bg-info/10',
  low: 'text-muted-foreground border-border bg-muted/50',
  info: 'text-muted-foreground border-border bg-muted/30',
}

// relativeTime renders an ISO timestamp as "3m ago" / "2h ago" / "just now".
export function relativeTime(iso: string | null): string {
  if (!iso) return 'never'
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

// findingTotal sums a repo's per-severity finding counts.
export function findingTotal(f: GitRepo['findings']): number {
  return severityOrder.reduce((n, s) => n + (f[s] ?? 0), 0)
}
