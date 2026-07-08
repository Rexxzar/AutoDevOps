// Terraform Store, types + helpers for the workspace catalog and detail pages.

export interface TfWorkspace {
  id: string
  name: string
  provider: string
  resource_type: string
  kind: 'generated' | 'imported'
  status: string
  has_config: boolean
  has_state: boolean
  last_run?: string // "apply:succeeded"
  last_run_at?: string | null
  run_count: number
  created_at: string
  updated_at: string
}

export interface TfRun {
  id: string
  resource_id?: string
  template: string
  action: 'plan' | 'apply' | 'destroy'
  status: 'pending' | 'running' | 'succeeded' | 'failed'
  plan_output?: string
  created_at: string
}

// statusStyle maps a resource/run status to a Tailwind chip class.
export const tfStatusStyle: Record<string, string> = {
  applied: 'text-success border-success/25 bg-success/10',
  succeeded: 'text-success border-success/25 bg-success/10',
  planned: 'text-info border-info/25 bg-info/10',
  planning: 'text-info border-info/25 bg-info/10',
  applying: 'text-info border-info/25 bg-info/10',
  pending: 'text-muted-foreground border-border bg-muted/50',
  running: 'text-info border-info/25 bg-info/10',
  destroying: 'text-warning border-warning/25 bg-warning/10',
  destroyed: 'text-muted-foreground border-border bg-muted/50',
  failed: 'text-critical border-critical/25 bg-critical/10',
}

export function tfRelativeTime(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}
