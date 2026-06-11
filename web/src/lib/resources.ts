// Shared types and lookups for the standalone Resources feature — used by
// both the Resources list page and the per-resource detail page.

export interface ProvisionedResource {
  id: string
  name: string
  provider: string
  cloud_account_id?: string
  region: string
  resource_type: string
  template: string
  status: string
  // JSON-encoded terraform outputs (vpc_id, public IP, db endpoint, etc. —
  // see `output` blocks in internal/terraform/templates/*/stack.tf.tmpl),
  // "{}" until the first successful Apply.
  outputs: string
  created_at: string
  updated_at: string
}

export interface TerraformRun {
  id: string
  deployment_id?: string
  resource_id?: string
  cloud_account_id?: string
  template: string
  action: string
  status: string
  plan_output?: string
  created_at: string
}

export const provisionedTypeLabel: Record<string, Record<string, string>> = {
  aws: { ecs: 'ECS Fargate', vm: 'EC2 Instance', rds: 'RDS Database' },
  azure: { aci: 'Container Instance', vm: 'Virtual Machine', db: 'Database' },
}

export const provisionedStatusMeta: Record<string, { kind: import('@/components/ui/status').StatusKind; label: string; pulse?: boolean }> = {
  pending: { kind: 'pending', label: 'Pending' },
  planning: { kind: 'building', label: 'Planning', pulse: true },
  planned: { kind: 'queued', label: 'Planned' },
  applying: { kind: 'building', label: 'Applying', pulse: true },
  applied: { kind: 'running', label: 'Applied' },
  destroying: { kind: 'building', label: 'Destroying', pulse: true },
  destroyed: { kind: 'idle', label: 'Destroyed' },
  failed: { kind: 'failed', label: 'Failed' },
}

export function formatRelativeTime(iso: string): string {
  const minutes = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

// Known transient/cloud-side terraform errors that confuse users if shown
// raw — translated into plain-language guidance with the actual fix.
const KNOWN_TF_ERRORS: { pattern: RegExp; message: string }[] = [
  {
    pattern: /NicReservedForAnotherVm/i,
    message: "Azure is still holding this VM's network interface from a failed creation attempt — it releases automatically after about 3 minutes. Wait a few minutes, then try Destroy again.",
  },
  {
    pattern: /SkuNotAvailable/i,
    message: 'This VM size is not available in this region for your subscription (a capacity restriction, not a config error). Destroy this resource, then try a different VM size or region.',
  },
]

export function friendlyTerraformError(output?: string): string | null {
  if (!output) return null
  for (const { pattern, message } of KNOWN_TF_ERRORS) {
    if (pattern.test(output)) return message
  }
  return null
}

// parseOutputs safely parses the outputs JSON string into a flat
// string-keyed map — terraform output values are JSON-encoded scalars
// (numbers/strings), so String() on the parsed value renders cleanly.
export function parseOutputs(outputs: string): Record<string, string> {
  try {
    const parsed = JSON.parse(outputs) as Record<string, unknown>
    const result: Record<string, string> = {}
    for (const [k, v] of Object.entries(parsed)) {
      result[k] = typeof v === 'string' ? v : JSON.stringify(v)
    }
    return result
  } catch {
    return {}
  }
}
