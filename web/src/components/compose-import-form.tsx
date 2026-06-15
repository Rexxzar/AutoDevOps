import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, AlertCircle, AlertTriangle, CheckCircle2, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import { TargetPill, FormField, inputCls } from '@/components/deploy-form-fields'

type LocalTarget = 'k8s' | 'container'

interface ComposeServicePlan {
  name: string
  kind: 'image' | 'git' | ''
  image?: string
  build_context?: string
  port?: number
  env?: Record<string, string>
  volume_path?: string
  warnings?: string[]
}

interface ComposeLinkPlan {
  app_name: string
  depends_on_name: string
  alias: string
}

interface ComposePlanResponse {
  services: ComposeServicePlan[]
  links: ComposeLinkPlan[]
  warnings?: string[]
}

interface ServiceRow extends ComposeServicePlan {
  included: boolean
  portInput: string
}

interface LinkRow extends ComposeLinkPlan {
  included: boolean
}

interface CreatedApp {
  name: string
  kind: 'image' | 'git'
  webhookURL?: string
  webhookSecret?: string
}

interface ImportResult {
  deploymentId: string
  stackName: string
  created: CreatedApp[]
  errors: string[]
  linksCreated: number
  linksTotal: number
}

type ImportStep = 'connect' | 'preview' | 'success'

// ComposeImportForm is the "Import from docker-compose" entry point (Phase 9):
// preview a repo's compose file, let the user confirm which services/links to
// import, then orchestrate the existing create-deployment/add-app/create-link
// endpoints with the confirmed plan. No dedicated "import commit" endpoint —
// this is pure frontend orchestration over POST /deployments/import/preview's
// ComposePlan.
export function ComposeImportForm() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStep] = useState<ImportStep>('connect')

  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewError, setPreviewError] = useState('')

  const [services, setServices] = useState<ServiceRow[]>([])
  const [links, setLinks] = useState<LinkRow[]>([])
  const [planWarnings, setPlanWarnings] = useState<string[]>([])
  const [stackName, setStackName] = useState('')
  const [target, setTarget] = useState<LocalTarget>('k8s')

  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [result, setResult] = useState<ImportResult | null>(null)

  async function preview(e: React.FormEvent) {
    e.preventDefault()
    setPreviewLoading(true); setPreviewError('')
    try {
      const body: Record<string, unknown> = { repo_url: repo, branch }
      if (token) body.git_auth = { type: 'token', token }
      const plan = await apiFetch<ComposePlanResponse>('/deployments/import/preview', {
        method: 'POST', body: JSON.stringify(body),
      })
      const svcRows: ServiceRow[] = (plan.services ?? []).map(s => ({
        ...s,
        included: s.kind !== '',
        portInput: s.port ? String(s.port) : '',
      }))
      const includedNames = new Set(svcRows.filter(s => s.included).map(s => s.name))
      const linkRows: LinkRow[] = (plan.links ?? []).map(l => ({
        ...l,
        included: includedNames.has(l.app_name) && includedNames.has(l.depends_on_name),
      }))
      setServices(svcRows)
      setLinks(linkRows)
      setPlanWarnings(plan.warnings ?? [])
      setStackName(svcRows.find(s => s.kind === 'git')?.name ?? '')
      setCreateError('')
      setStep('preview')
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : 'Failed to preview docker-compose file')
    } finally {
      setPreviewLoading(false)
    }
  }

  function toggleService(name: string) {
    setServices(rows => {
      const next = rows.map(r => (r.name === name ? { ...r, included: !r.included } : r))
      const includedNames = new Set(next.filter(r => r.included).map(r => r.name))
      setLinks(ls => ls.map(l => ({
        ...l,
        included: l.included && includedNames.has(l.app_name) && includedNames.has(l.depends_on_name),
      })))
      return next
    })
  }

  function setPort(name: string, value: string) {
    setServices(rows => rows.map(r => (r.name === name ? { ...r, portInput: value } : r)))
  }

  function toggleLink(index: number) {
    setLinks(rows => rows.map((l, i) => (i === index ? { ...l, included: !l.included } : l)))
  }

  const gitServiceCount = services.filter(s => s.kind === 'git').length

  async function createStack() {
    setCreating(true); setCreateError('')
    try {
      if (!stackName.trim()) throw new Error('Stack name is required')
      const included = services.filter(s => s.included && s.kind !== '')
      const gitIncluded = included.filter(s => s.kind === 'git')
      if (gitIncluded.length === 0) {
        throw new Error('Select at least one git-based (build:) service to anchor the stack')
      }
      for (const s of included) {
        if (s.kind === 'image') {
          const p = Number(s.portInput)
          if (!Number.isInteger(p) || p < 1 || p > 65535) {
            throw new Error(`"${s.name}": container port must be between 1 and 65535`)
          }
        }
      }

      const first = gitIncluded[0]
      const rest = included.filter(s => s !== first)
      const gitAuth = token ? { type: 'token', token } : undefined

      const createBody: Record<string, unknown> = { name: stackName.trim(), repo_url: repo, branch, target_type: target }
      if (gitAuth) createBody.git_auth = gitAuth
      if (first.build_context) createBody.build_context = first.build_context

      const stackResp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>('/deployments', {
        method: 'POST', body: JSON.stringify(createBody),
      })
      const deploymentId = stackResp.id

      const created: CreatedApp[] = [{ name: first.name, kind: 'git', webhookURL: stackResp.webhook_url, webhookSecret: stackResp.webhook_secret }]
      const errors: string[] = []

      for (const svc of rest) {
        try {
          if (svc.kind === 'git') {
            const body: Record<string, unknown> = { name: svc.name, kind: 'git', repo_url: repo, branch, target_type: target }
            if (gitAuth) body.git_auth = gitAuth
            if (svc.build_context) body.build_context = svc.build_context
            const resp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>(`/deployments/${deploymentId}/apps`, {
              method: 'POST', body: JSON.stringify(body),
            })
            created.push({ name: svc.name, kind: 'git', webhookURL: resp.webhook_url, webhookSecret: resp.webhook_secret })
          } else {
            const body: Record<string, unknown> = { name: svc.name, kind: 'image', image_ref: svc.image, port: Number(svc.portInput), target_type: target }
            if (svc.env && Object.keys(svc.env).length > 0) body.env = svc.env
            if (svc.volume_path) body.volume_path = svc.volume_path
            await apiFetch<{ id: string }>(`/deployments/${deploymentId}/apps`, {
              method: 'POST', body: JSON.stringify(body),
            })
            created.push({ name: svc.name, kind: 'image' })
          }
        } catch (err) {
          errors.push(`${svc.name}: ${err instanceof Error ? err.message : 'failed to add app'}`)
        }
      }

      const nameToAppId: Record<string, string> = {}
      try {
        const stack = await apiFetch<{ apps: { id: string; name: string }[] }>(`/deployments/${deploymentId}`)
        for (const app of stack.apps) nameToAppId[app.name] = app.id
      } catch {
        errors.push('failed to resolve app ids — links were not created')
      }

      const includedLinks = links.filter(l => l.included)
      let linksCreated = 0
      for (const link of includedLinks) {
        const appId = nameToAppId[link.app_name]
        const dependsOnId = nameToAppId[link.depends_on_name]
        if (!appId || !dependsOnId) continue
        try {
          await apiFetch(`/deployments/${deploymentId}/apps/${appId}/links`, {
            method: 'POST', body: JSON.stringify({ alias: link.alias, depends_on_app_id: dependsOnId }),
          })
          linksCreated++
        } catch (err) {
          errors.push(`link ${link.app_name} → ${link.depends_on_name}: ${err instanceof Error ? err.message : 'failed'}`)
        }
      }

      setResult({ deploymentId, stackName: stackName.trim(), created, errors, linksCreated, linksTotal: includedLinks.length })
      setStep('success')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : 'Failed to create stack')
    } finally {
      setCreating(false)
    }
  }

  function startOver() {
    setStep('connect')
    setServices([]); setLinks([]); setPlanWarnings([]); setStackName('')
    setResult(null); setCreateError(''); setPreviewError('')
  }

  if (step === 'connect') {
    return (
      <div className="w-full max-w-md space-y-6">
        <div className="space-y-1">
          <h1 className="text-xl font-bold">Import docker-compose</h1>
          <p className="text-sm text-muted-foreground">Connect a repo with a docker-compose file at its root</p>
        </div>
        <form onSubmit={preview} className="space-y-4">
          <FormField label="Git repository URL">
            <input value={repo} onChange={e => setRepo(e.target.value)} placeholder="https://github.com/you/my-app" required className={inputCls} />
          </FormField>
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Branch">
              <input value={branch} onChange={e => setBranch(e.target.value)} placeholder="main" className={inputCls} />
            </FormField>
            <FormField label="Git token" hint="private repos">
              <input value={token} onChange={e => setToken(e.target.value)} placeholder="ghp_…" type="password" className={inputCls} />
            </FormField>
          </div>
          {previewError && (
            <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
              <AlertCircle className="size-4 shrink-0" />{previewError}
            </div>
          )}
          <Button type="submit" className="w-full h-10 gap-2 mt-2" disabled={previewLoading}>
            {previewLoading && <Loader2 className="size-4 animate-spin" />}
            {previewLoading ? 'Parsing docker-compose…' : 'Preview services'}
          </Button>
        </form>
      </div>
    )
  }

  if (step === 'preview') {
    return (
      <div className="w-full max-w-2xl space-y-6">
        <div className="space-y-1">
          <h1 className="text-xl font-bold">Review services</h1>
          <p className="text-sm text-muted-foreground font-mono">{repo}@{branch}</p>
        </div>

        {planWarnings.length > 0 && (
          <div className="space-y-1.5 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5">
            {planWarnings.map((w, i) => (
              <p key={i} className="flex items-start gap-2 text-xs text-warning">
                <AlertTriangle className="size-3.5 shrink-0 mt-0.5" />{w}
              </p>
            ))}
          </div>
        )}

        {gitServiceCount === 0 && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />
            No build: services found — at least one is required to create a stack
          </div>
        )}

        <div className="space-y-2">
          {services.map(svc => (
            <div key={svc.name} className={cn('rounded-xl border border-border bg-card p-4 space-y-2', !svc.included && 'opacity-60')}>
              <div className="flex flex-wrap items-center gap-3">
                <input
                  type="checkbox"
                  checked={svc.included}
                  disabled={svc.kind === ''}
                  onChange={() => toggleService(svc.name)}
                  className="size-4 cursor-pointer accent-primary disabled:cursor-not-allowed"
                />
                <span className="font-mono text-sm font-medium">{svc.name}</span>
                <span className={cn(
                  'rounded-full border px-2 py-0.5 text-[0.65rem] font-medium uppercase tracking-wide',
                  svc.kind === 'git' ? 'border-info/20 bg-info/12 text-info'
                    : svc.kind === 'image' ? 'border-success/20 bg-success/12 text-success'
                      : 'border-border bg-muted text-muted-foreground',
                )}>
                  {svc.kind === 'git' ? 'Build from source' : svc.kind === 'image' ? 'Container image' : 'Skipped'}
                </span>
                {svc.env && Object.keys(svc.env).length > 0 && (
                  <span className="text-xs text-muted-foreground">{Object.keys(svc.env).length} env var{Object.keys(svc.env).length === 1 ? '' : 's'}</span>
                )}
              </div>

              <div className="ml-7 space-y-1.5">
                {svc.kind === 'git' && (
                  <p className="text-xs text-muted-foreground">
                    Build context: <span className="font-mono text-foreground">{svc.build_context || '(repo root)'}</span>
                  </p>
                )}
                {svc.kind === 'image' && (
                  <>
                    <p className="text-xs text-muted-foreground">
                      Image: <span className="font-mono text-foreground">{svc.image}</span>
                    </p>
                    <div className="flex items-center gap-2">
                      <label className="text-xs text-muted-foreground">Port</label>
                      <input
                        type="number" min={1} max={65535}
                        value={svc.portInput}
                        onChange={e => setPort(svc.name, e.target.value)}
                        disabled={!svc.included}
                        className={cn(inputCls, 'h-8 w-24 text-xs')}
                      />
                    </div>
                  </>
                )}
                {svc.warnings?.map((w, i) => (
                  <p key={i} className="flex items-start gap-1.5 text-xs text-warning">
                    <AlertTriangle className="size-3 shrink-0 mt-0.5" />{w}
                  </p>
                ))}
              </div>
            </div>
          ))}
        </div>

        <FormField label="Stack name">
          <input value={stackName} onChange={e => setStackName(e.target.value)} placeholder="my-stack" required className={inputCls} />
        </FormField>

        <FormField label="Target" hint="applies to all included services">
          <div className="flex gap-2">
            <TargetPill label="Kubernetes" sub="kind cluster" active={target === 'k8s'} onClick={() => setTarget('k8s')} />
            <TargetPill label="Container" sub="Docker SDK" active={target === 'container'} onClick={() => setTarget('container')} />
          </div>
        </FormField>

        {links.length > 0 && (
          <FormField label="Suggested links" hint="from depends_on">
            <div className="space-y-2">
              {links.map((link, i) => {
                const bothIncluded = !!services.find(s => s.name === link.app_name)?.included
                  && !!services.find(s => s.name === link.depends_on_name)?.included
                return (
                  <label key={i} className={cn('flex items-center gap-2 text-xs', !bothIncluded && 'opacity-50')}>
                    <input
                      type="checkbox"
                      checked={link.included}
                      disabled={!bothIncluded}
                      onChange={() => toggleLink(i)}
                      className="size-4 cursor-pointer accent-primary disabled:cursor-not-allowed"
                    />
                    <span className="font-mono">{link.app_name}</span>
                    <ArrowRight className="size-3 text-muted-foreground" />
                    <span className="font-mono">{link.depends_on_name}</span>
                    <span className="text-muted-foreground">as AUTODEVOPS_{link.alias}_*</span>
                  </label>
                )
              })}
            </div>
          </FormField>
        )}

        {createError && (
          <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
            <AlertCircle className="size-4 shrink-0" />{createError}
          </div>
        )}

        <div className="flex gap-3">
          <Button type="button" variant="outline" className="flex-1 gap-1.5" onClick={startOver} disabled={creating}>
            <ArrowLeft className="size-4" /> Back
          </Button>
          <Button type="button" className="flex-1 gap-2" onClick={createStack} disabled={creating || gitServiceCount === 0}>
            {creating && <Loader2 className="size-4 animate-spin" />}
            {creating ? 'Creating stack…' : 'Create stack'}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="w-full max-w-md space-y-6">
      <div className="flex items-center gap-3">
        <CheckCircle2 className="size-6 text-success shrink-0" />
        <div>
          <p className="font-semibold">Stack imported</p>
          <p className="text-sm text-muted-foreground font-mono">{result?.stackName}</p>
        </div>
      </div>

      <div className="space-y-3">
        {result?.created.map((app, i) => (
          <div key={i} className="rounded-xl border border-border bg-card p-4 space-y-2">
            <div className="flex items-center justify-between">
              <span className="font-mono text-sm font-medium">{app.name}</span>
              <span className="text-xs text-muted-foreground">{app.kind === 'git' ? 'build from source' : 'container image'}</span>
            </div>
            {app.webhookURL && app.webhookSecret && (
              <div className="space-y-2">
                <div className="space-y-1">
                  <p className="text-[0.7rem] text-muted-foreground">Payload URL</p>
                  <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs break-all select-all">
                    http://YOUR_SERVER:8080{app.webhookURL}
                  </p>
                </div>
                <div className="space-y-1">
                  <p className="text-[0.7rem] text-muted-foreground">Secret</p>
                  <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs select-all">{app.webhookSecret}</p>
                </div>
              </div>
            )}
          </div>
        ))}
      </div>

      {result && result.linksTotal > 0 && (
        <p className="text-xs text-muted-foreground">{result.linksCreated} of {result.linksTotal} link(s) created</p>
      )}

      {result && result.errors.length > 0 && (
        <div className="space-y-1.5 rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5">
          {result.errors.map((e, i) => (
            <p key={i} className="flex items-start gap-2 text-xs text-warning">
              <AlertTriangle className="size-3.5 shrink-0 mt-0.5" />{e}
            </p>
          ))}
        </div>
      )}

      <div className="flex gap-3">
        <Button variant="outline" className="flex-1" onClick={startOver}>Import another</Button>
        <Button className="flex-1" onClick={() => navigate(`/deployments/${result?.deploymentId}`)}>View deployment</Button>
      </div>
    </div>
  )
}
