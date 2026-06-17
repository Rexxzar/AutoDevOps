import { useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Loader2, CheckCircle2, AlertCircle, Globe, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import { TargetPill, FormField, DomainField, inputCls, selectCls } from '@/components/deploy-form-fields'
import { ComposeImportForm } from '@/components/compose-import-form'
import { type ProvisionedResource, provisionedTypeLabel } from '@/lib/resources'

type Step = 'form' | 'success'
type Mode = 'scratch' | 'import'
type LocalTarget = 'k8s' | 'container'
type TargetLocation = 'local' | 'cloud'
type CloudTarget = 'vm' | 'ecs' | 'aci'

// ─── Main page ────────────────────────────────────────────────────
export function NewDeploymentPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStep] = useState<Step>('form')
  const [mode, setMode] = useState<Mode>('scratch')

  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [targetLocation, setTargetLocation] = useState<TargetLocation>('local')
  const [localTarget, setLocalTarget] = useState<LocalTarget>('k8s')
  const [cloudTarget, setCloudTarget] = useState<CloudTarget>('vm')
  const [vmResourceId, setVmResourceId] = useState('')
  const [ecsResourceId, setEcsResourceId] = useState('')
  const [aciResourceId, setAciResourceId] = useState('')
  const [localDomain, setLocalDomain] = useState('')

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState<{ webhookURL: string; webhookSecret: string; name: string; targetType: string } | null>(null)

  // Applied AWS/Azure resources — offered as the cloud target picker, same
  // resource_type filters AddAppDialog uses (deployment-detail.tsx).
  const { data: allResources = [] } = useQuery({
    queryKey: ['resources'],
    queryFn: () => apiFetch<ProvisionedResource[]>('/resources'),
  })
  const appliedResources = allResources.filter((r) => r.status === 'applied')
  const vmResources = appliedResources.filter((r) => r.resource_type === 'vm')
  const ecsResources = appliedResources.filter((r) => r.resource_type === 'ecs')
  const aciResources = appliedResources.filter((r) => r.resource_type === 'aci')
  const hasCloudResources = vmResources.length > 0 || ecsResources.length > 0 || aciResources.length > 0

  const effectiveVmResourceId = vmResourceId || vmResources[0]?.id || ''
  const effectiveEcsResourceId = ecsResourceId || ecsResources[0]?.id || ''
  const effectiveAciResourceId = aciResourceId || aciResources[0]?.id || ''

  function selectTargetLocation(loc: TargetLocation) {
    setTargetLocation(loc)
    if (loc === 'cloud') {
      if (vmResources.length > 0) setCloudTarget('vm')
      else if (ecsResources.length > 0) setCloudTarget('ecs')
      else if (aciResources.length > 0) setCloudTarget('aci')
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault(); setLoading(true); setError('')
    try {
      const targetType: string = targetLocation === 'local' ? localTarget : cloudTarget
      const body: Record<string, unknown> = { name, repo_url: repo, branch, target_type: targetType }
      if (targetLocation === 'cloud') {
        body.target_resource_id =
          cloudTarget === 'vm' ? effectiveVmResourceId
          : cloudTarget === 'ecs' ? effectiveEcsResourceId
          : effectiveAciResourceId
      }
      if (token) body.git_auth = { type: 'token', token }
      if (localDomain) body.domain = localDomain
      const resp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>('/deployments', {
        method: 'POST', body: JSON.stringify(body),
      })
      setSuccess({ webhookURL: resp.webhook_url, webhookSecret: resp.webhook_secret, name, targetType })
      setStep('success')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create deployment')
    } finally { setLoading(false) }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      {/* Top bar */}
      <div className="flex h-14 items-center gap-3 border-b border-border px-6">
        <button
          onClick={() => step === 'form' ? navigate(-1) : navigate('/deployments')}
          className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
          {step === 'form' ? 'Back' : 'View deployments'}
        </button>
        <span className="text-sm font-semibold">New Deployment</span>
      </div>

      <div className="flex flex-1 items-start justify-center p-8 pt-12">

        {/* ── Form ── */}
        {step === 'form' && (
          <div className="flex w-full max-w-2xl flex-col items-center gap-6">
            <div className="flex w-full max-w-md gap-2">
              <TargetPill label="From scratch" sub="single repo" active={mode === 'scratch'} onClick={() => setMode('scratch')} />
              <TargetPill label="Import docker-compose" sub="multiple services" active={mode === 'import'} onClick={() => setMode('import')} />
            </div>

            {mode === 'scratch' ? (
              <div className="w-full max-w-md space-y-6">
                <div className="space-y-1">
                  <h1 className="text-xl font-bold">New Deployment</h1>
                  <p className="text-sm text-muted-foreground">Connect a Git repo and choose a target</p>
                </div>
                <form onSubmit={submit} className="space-y-4">
                  <FormField label="App name">
                    <input value={name} onChange={e => setName(e.target.value)} placeholder="my-app" required className={inputCls} />
                  </FormField>
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
                  <FormField label="Target">
                    <div className="flex gap-2">
                      <TargetPill label="Local" sub="this machine" active={targetLocation === 'local'} onClick={() => selectTargetLocation('local')} />
                      <TargetPill label="Cloud" sub="AWS / Azure" active={targetLocation === 'cloud'} onClick={() => selectTargetLocation('cloud')} />
                    </div>
                  </FormField>
                  {targetLocation === 'local' && (
                    <FormField label="Local target">
                      <div className="flex gap-2">
                        <TargetPill label="Kubernetes" sub="kind cluster" active={localTarget === 'k8s'} onClick={() => setLocalTarget('k8s')} />
                        <TargetPill label="Container" sub="Docker SDK" active={localTarget === 'container'} onClick={() => setLocalTarget('container')} />
                      </div>
                    </FormField>
                  )}
                  {targetLocation === 'cloud' && !hasCloudResources && (
                    <div className="rounded-lg border border-border bg-card px-3 py-2.5 text-sm text-muted-foreground">
                      You don&apos;t have any applied cloud resources yet.{' '}
                      <button type="button" onClick={() => navigate('/new-resource')} className="underline text-foreground">
                        Go provision one
                      </button>{' '}first (EC2/Azure VM, ECS, or ACI), then come back here.
                    </div>
                  )}
                  {targetLocation === 'cloud' && hasCloudResources && (
                    <>
                      <FormField label="Cloud target">
                        <div className="flex gap-2">
                          {vmResources.length > 0 && (
                            <TargetPill label="Cloud VM" sub="SSH deploy" active={cloudTarget === 'vm'} onClick={() => setCloudTarget('vm')} />
                          )}
                          {ecsResources.length > 0 && (
                            <TargetPill label="AWS ECS" sub="Fargate service" active={cloudTarget === 'ecs'} onClick={() => setCloudTarget('ecs')} />
                          )}
                          {aciResources.length > 0 && (
                            <TargetPill label="Azure ACI" sub="container group" active={cloudTarget === 'aci'} onClick={() => setCloudTarget('aci')} />
                          )}
                        </div>
                      </FormField>
                      {cloudTarget === 'vm' && vmResources.length > 0 && (
                        <FormField label="VM resource" hint="from the Resources page">
                          <select value={effectiveVmResourceId} onChange={(e) => setVmResourceId(e.target.value)} className={selectCls} required>
                            {vmResources.map((res) => (
                              <option key={res.id} value={res.id}>
                                {res.name} ({provisionedTypeLabel[res.provider]?.[res.resource_type] ?? res.resource_type})
                              </option>
                            ))}
                          </select>
                        </FormField>
                      )}
                      {cloudTarget === 'ecs' && ecsResources.length > 0 && (
                        <FormField label="ECS service" hint="from the Resources page">
                          <select value={effectiveEcsResourceId} onChange={(e) => setEcsResourceId(e.target.value)} className={selectCls} required>
                            {ecsResources.map((res) => (
                              <option key={res.id} value={res.id}>
                                {res.name} ({provisionedTypeLabel[res.provider]?.[res.resource_type] ?? res.resource_type})
                              </option>
                            ))}
                          </select>
                        </FormField>
                      )}
                      {cloudTarget === 'aci' && aciResources.length > 0 && (
                        <FormField label="ACI container group" hint="from the Resources page">
                          <select value={effectiveAciResourceId} onChange={(e) => setAciResourceId(e.target.value)} className={selectCls} required>
                            {aciResources.map((res) => (
                              <option key={res.id} value={res.id}>
                                {res.name} ({provisionedTypeLabel[res.provider]?.[res.resource_type] ?? res.resource_type})
                              </option>
                            ))}
                          </select>
                        </FormField>
                      )}
                    </>
                  )}
                  {targetLocation === 'local' && <DomainField value={localDomain} onChange={setLocalDomain} />}
                  {error && (
                    <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
                      <AlertCircle className="size-4 shrink-0" />{error}
                    </div>
                  )}
                  <Button type="submit" className="w-full h-10 gap-2 mt-2" disabled={loading}>
                    {loading && <Loader2 className="size-4 animate-spin" />}
                    {loading ? 'Creating deployment…' : 'Create deployment'}
                  </Button>
                </form>
              </div>
            ) : (
              <ComposeImportForm />
            )}
          </div>
        )}

        {/* ── Success ── */}
        {step === 'success' && success && (
          <div className="w-full max-w-md space-y-6">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-6 text-success shrink-0" />
              <div>
                <p className="font-semibold">Deployment registered</p>
                <p className="text-sm text-muted-foreground font-mono">{success.name}</p>
              </div>
            </div>
            <div className="rounded-xl border border-border bg-card p-5 space-y-4">
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">GitHub Webhook Setup</p>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Payload URL</p>
                <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs break-all select-all">
                  http://YOUR_SERVER:8080{success.webhookURL}
                </p>
              </div>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Secret</p>
                <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs select-all">{success.webhookSecret}</p>
              </div>
              <p className="text-xs text-muted-foreground">
                Content type: <span className="font-mono">application/json</span> · Event: <span className="font-mono">push</span>
              </p>
            </div>
            <p className="text-xs text-muted-foreground">
              Or trigger immediately from CLI:<br />
              <span className="font-mono">autodev deploy --name {success.name} --repo &lt;url&gt; --target {success.targetType} --trigger</span>
            </p>
            <div className="flex gap-3">
              <Button variant="outline" className="flex-1" onClick={() => { setStep('form'); setName(''); setRepo(''); setBranch('main'); setToken('') }}>
                New deployment
              </Button>
              <Button className="flex-1" onClick={() => navigate('/deployments')}>View deployments</Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
