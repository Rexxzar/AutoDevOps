import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Loader2, CheckCircle2, AlertCircle, Server, Cloud, Container, Box, GitBranch, Layers } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'
import { TargetPill, FormField, DomainField, K8sOptions, type K8sConfigValue, inputCls, selectCls } from '@/components/deploy-form-fields'
import { ComposeImportForm } from '@/components/compose-import-form'
import { type ProvisionedResource, provisionedTypeLabel } from '@/lib/resources'
import { DECLARED_FRAMEWORK_OPTIONS } from '@/lib/frameworks'
import { type GitRepo } from '@/lib/repos'

type WizardStep = 1 | 2 | 3 | 4
type FormStep = 'form' | 'success'
type Location = 'local' | 'cloud'
type LocalTarget = 'k8s' | 'docker'
type CloudProvider = 'aws' | 'azure'
type DockerMode = 'scratch' | 'compose'
type CloudTarget = 'ec2' | 'ecs' | 'vm' | 'aci'

// ─── Two-card choice layout ──────────────────────────────────────

interface ChoiceOption {
  title: string
  description: string
  icon: React.ElementType
  tags: string[]
  onClick: () => void
}

function CardChoice({ left, right }: { left: ChoiceOption; right: ChoiceOption }) {
  return (
    <div className="flex items-stretch gap-0">
      <ChoiceCard opt={left} />
      <div className="flex flex-col items-center justify-center px-6 gap-2">
        <div className="flex-1 w-px bg-border" />
        <span className="text-[0.65rem] font-medium text-muted-foreground/60 uppercase tracking-widest">or</span>
        <div className="flex-1 w-px bg-border" />
      </div>
      <ChoiceCard opt={right} />
    </div>
  )
}

function ChoiceCard({ opt }: { opt: ChoiceOption }) {
  const Icon = opt.icon
  return (
    <button
      onClick={opt.onClick}
      className="group flex flex-1 flex-col items-center gap-5 rounded-2xl border border-border bg-card p-10 text-center transition-all duration-200 hover:scale-[1.03] hover:border-ring/40 hover:shadow-xl"
    >
      <div className="flex size-16 items-center justify-center rounded-2xl border border-border bg-background transition-colors group-hover:border-ring/30 group-hover:bg-accent">
        <Icon className="size-7 text-foreground" />
      </div>
      <div className="space-y-1.5">
        <p className="text-lg font-semibold">{opt.title}</p>
        <p className="text-xs text-muted-foreground leading-relaxed max-w-[180px]">
          {opt.description}
        </p>
      </div>
      <div className="flex gap-2 flex-wrap justify-center">
        {opt.tags.map((t) => (
          <span key={t} className="rounded-full border border-border bg-muted px-2.5 py-0.5 text-[0.65rem] font-medium text-muted-foreground">{t}</span>
        ))}
      </div>
    </button>
  )
}

// ─── Breadcrumb labels ───────────────────────────────────────────

function breadcrumb(
  location: Location | null,
  localTarget: LocalTarget | null,
  cloudProvider: CloudProvider | null,
  dockerMode: DockerMode | null,
  cloudTarget: CloudTarget | null,
  wizardStep: WizardStep,
): string {
  const parts: string[] = []
  if (location === 'local') {
    parts.push('Local')
    if (localTarget === 'k8s') {
      parts.push('Kubernetes')
      if (dockerMode === 'scratch') parts.push('Git Repo')
      else if (dockerMode === 'compose') parts.push('Compose Import')
    } else if (localTarget === 'docker') {
      parts.push('Docker')
      if (dockerMode === 'scratch') parts.push('Git Repo')
      else if (dockerMode === 'compose') parts.push('Compose Import')
    }
  } else if (location === 'cloud') {
    parts.push('Cloud')
    if (cloudProvider === 'aws') {
      parts.push('AWS')
      if (cloudTarget === 'ec2') parts.push('EC2')
      else if (cloudTarget === 'ecs') parts.push('ECS')
    } else if (cloudProvider === 'azure') {
      parts.push('Azure')
      if (cloudTarget === 'vm') parts.push('VM')
      else if (cloudTarget === 'aci') parts.push('ACI')
    }
  }
  if (parts.length === 0) return 'Choose a target'
  if (wizardStep === 4) return parts.join(' > ')
  return parts.join(' > ')
}

// ─── Main page ────────────────────────────────────────────────────

export function NewDeploymentPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()

  // Wizard navigation
  const [wizardStep, setWizardStep] = useState<WizardStep>(1)
  const [formStep, setFormStep] = useState<FormStep>('form')
  const [location, setLocation] = useState<Location | null>(null)
  const [localTarget, setLocalTarget] = useState<LocalTarget | null>(null)
  const [cloudProvider, setCloudProvider] = useState<CloudProvider | null>(null)
  const [dockerMode, setDockerMode] = useState<DockerMode | null>(null)
  const [cloudTarget, setCloudTarget] = useState<CloudTarget | null>(null)

  // Form fields
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [repoId, setRepoId] = useState('') // when set, deploy from a stored repo
  const [declaredFramework, setDeclaredFramework] = useState('')
  const [vmResourceId, setVmResourceId] = useState('')
  const [ecsResourceId, setEcsResourceId] = useState('')
  const [aciResourceId, setAciResourceId] = useState('')
  const [localDomain, setLocalDomain] = useState('')
  const [namespaceOverride, setNamespaceOverride] = useState('')
  const [k8sConfig, setK8sConfig] = useState<K8sConfigValue | undefined>(undefined)
  const [complianceEnforce, setComplianceEnforce] = useState(false)

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState<{ webhookURL: string; webhookSecret: string; name: string; targetType: string } | null>(null)

  // Stored repos (Git Repo Store) — pick one or add a new one inline.
  const { data: storedRepos = [] } = useQuery({
    queryKey: ['repos'],
    queryFn: () => apiFetch<GitRepo[]>('/repos'),
  })

  function selectStoredRepo(id: string) {
    setRepoId(id)
    if (id) {
      const r = storedRepos.find((x) => x.id === id)
      if (r) { setRepo(r.repo_url); setBranch(r.branch); setToken('') }
    } else {
      setRepo(''); setBranch('main')
    }
  }

  // Applied cloud resources
  const { data: allResources = [] } = useQuery({
    queryKey: ['resources'],
    queryFn: () => apiFetch<ProvisionedResource[]>('/resources'),
  })
  const appliedResources = allResources.filter((r) => r.status === 'applied')
  const awsVmResources = appliedResources.filter((r) => r.resource_type === 'vm' && r.provider === 'aws')
  const azureVmResources = appliedResources.filter((r) => r.resource_type === 'vm' && r.provider === 'azure')
  const ecsResources = appliedResources.filter((r) => r.resource_type === 'ecs')
  const aciResources = appliedResources.filter((r) => r.resource_type === 'aci')

  const effectiveVmResourceId = vmResourceId || (cloudProvider === 'aws' ? awsVmResources : azureVmResources)[0]?.id || ''
  const effectiveEcsResourceId = ecsResourceId || ecsResources[0]?.id || ''
  const effectiveAciResourceId = aciResourceId || aciResources[0]?.id || ''

  function resolveTargetType(): string {
    if (location === 'local') return localTarget === 'k8s' ? 'k8s' : 'container'
    if (cloudTarget === 'ec2' || cloudTarget === 'vm') return 'vm'
    if (cloudTarget === 'ecs') return 'ecs'
    if (cloudTarget === 'aci') return 'aci'
    return 'k8s'
  }

  function resolveResourceId(): string | undefined {
    if (location !== 'cloud') return undefined
    if (cloudTarget === 'ec2') return effectiveVmResourceId
    if (cloudTarget === 'ecs') return effectiveEcsResourceId
    if (cloudTarget === 'vm') return effectiveVmResourceId
    if (cloudTarget === 'aci') return effectiveAciResourceId
    return undefined
  }

  // The resource list for the current cloud path
  function cloudResources(): ProvisionedResource[] {
    if (cloudTarget === 'ec2') return awsVmResources
    if (cloudTarget === 'ecs') return ecsResources
    if (cloudTarget === 'vm') return azureVmResources
    if (cloudTarget === 'aci') return aciResources
    return []
  }

  function cloudResourceLabel(): string {
    if (cloudTarget === 'ec2') return 'EC2 instance'
    if (cloudTarget === 'ecs') return 'ECS service'
    if (cloudTarget === 'vm') return 'Azure VM'
    if (cloudTarget === 'aci') return 'ACI container group'
    return 'Resource'
  }

  function cloudResourceEffectiveId(): string {
    if (cloudTarget === 'ec2' || cloudTarget === 'vm') return effectiveVmResourceId
    if (cloudTarget === 'ecs') return effectiveEcsResourceId
    if (cloudTarget === 'aci') return effectiveAciResourceId
    return ''
  }

  function handleBack() {
    if (formStep === 'success') {
      navigate('/deployments')
      return
    }
    if (wizardStep === 1) {
      navigate(-1)
      return
    }
    if (wizardStep === 4) {
      setWizardStep(3)
      return
    }
    setWizardStep((wizardStep - 1) as WizardStep)
  }

  const isComposeForm = wizardStep === 4 && location === 'local' && (localTarget === 'docker' || localTarget === 'k8s') && dockerMode === 'compose'

  async function submit(e: React.FormEvent) {
    e.preventDefault(); setLoading(true); setError('')
    try {
      const targetType = resolveTargetType()
      const body: Record<string, unknown> = { name, repo_url: repo, branch, target_type: targetType }
      if (repoId) body.repo_id = repoId
      const resourceId = resolveResourceId()
      if (resourceId) body.target_resource_id = resourceId
      if (token && !repoId) body.git_auth = { type: 'token', token }
      if (declaredFramework) body.declared_framework = declaredFramework
      if (localDomain) body.domain = localDomain
      if (targetType === 'k8s') {
        if (namespaceOverride) body.namespace_override = namespaceOverride
        if (k8sConfig) body.k8s_config = k8sConfig
      }
      if (complianceEnforce) body.compliance_enforce = true
      const resp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>('/deployments', {
        method: 'POST', body: JSON.stringify(body),
      })
      setSuccess({ webhookURL: resp.webhook_url, webhookSecret: resp.webhook_secret, name, targetType })
      setFormStep('success')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create deployment')
    } finally { setLoading(false) }
  }

  const bc = breadcrumb(location, localTarget, cloudProvider, dockerMode, cloudTarget, wizardStep)

  return (
    <div className="flex min-h-screen flex-col bg-background">
      {/* Top bar */}
      <div className="flex h-14 items-center gap-3 border-b border-border px-6">
        {!isComposeForm && (
          <button
            onClick={handleBack}
            className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            <ArrowLeft className="size-4" />
            {formStep === 'success' ? 'View deployments' : 'Back'}
          </button>
        )}
        <span className="text-sm font-semibold">New Deployment</span>
        {wizardStep > 1 && formStep !== 'success' && (
          <span className="text-xs text-muted-foreground">{bc}</span>
        )}
      </div>

      <div className="flex flex-1 items-start justify-center p-8 pt-12">

        {formStep === 'form' && (
          <>
            {/* ── Step 1: Local vs Cloud ── */}
            {wizardStep === 1 && (
              <div className="w-full max-w-2xl space-y-6">
                <div className="space-y-1 text-center">
                  <h1 className="text-xl font-bold">New Deployment</h1>
                  <p className="text-sm text-muted-foreground">Where should this deployment run?</p>
                </div>
                <CardChoice
                  left={{
                    title: 'Local',
                    description: 'Run on this machine using Kubernetes or Docker',
                    icon: Server,
                    tags: ['K8s Pod', 'Docker Container'],
                    onClick: () => { setLocation('local'); setWizardStep(2) },
                  }}
                  right={{
                    title: 'Cloud',
                    description: 'Deploy to a pre-provisioned AWS or Azure resource',
                    icon: Cloud,
                    tags: ['EC2', 'ECS', 'Azure VM', 'ACI'],
                    onClick: () => { setLocation('cloud'); setWizardStep(2) },
                  }}
                />
              </div>
            )}

            {/* ── Step 2 (Local): K8s vs Docker ── */}
            {wizardStep === 2 && location === 'local' && (
              <div className="w-full max-w-2xl space-y-6">
                <div className="space-y-1 text-center">
                  <h1 className="text-xl font-bold">Local Target</h1>
                  <p className="text-sm text-muted-foreground">Choose a runtime for your deployment</p>
                </div>
                <CardChoice
                  left={{
                    title: 'Kubernetes',
                    description: 'Deploy as a pod in the local kind cluster with ingress',
                    icon: Box,
                    tags: ['Pod', 'Service', 'Ingress'],
                    onClick: () => { setLocalTarget('k8s'); setWizardStep(3) },
                  }}
                  right={{
                    title: 'Docker Container',
                    description: 'Run as a standalone container via the Docker SDK',
                    icon: Container,
                    tags: ['Container', 'Compose'],
                    onClick: () => { setLocalTarget('docker'); setWizardStep(3) },
                  }}
                />
              </div>
            )}

            {/* ── Step 2 (Cloud): AWS vs Azure ── */}
            {wizardStep === 2 && location === 'cloud' && (
              <div className="w-full max-w-2xl space-y-6">
                <div className="space-y-1 text-center">
                  <h1 className="text-xl font-bold">Cloud Provider</h1>
                  <p className="text-sm text-muted-foreground">Which cloud provider are you deploying to?</p>
                </div>
                <CardChoice
                  left={{
                    title: 'AWS',
                    description: 'Deploy to an Amazon Web Services resource',
                    icon: Cloud,
                    tags: ['EC2', 'ECS Fargate'],
                    onClick: () => { setCloudProvider('aws'); setWizardStep(3) },
                  }}
                  right={{
                    title: 'Azure',
                    description: 'Deploy to a Microsoft Azure resource',
                    icon: Cloud,
                    tags: ['Virtual Machine', 'ACI'],
                    onClick: () => { setCloudProvider('azure'); setWizardStep(3) },
                  }}
                />
              </div>
            )}

            {/* ── Step 3 (K8s / Docker): Git Repo vs Compose ── */}
            {wizardStep === 3 && location === 'local' && (localTarget === 'docker' || localTarget === 'k8s') && (
              <div className="w-full max-w-2xl space-y-6">
                <div className="space-y-1 text-center">
                  <h1 className="text-xl font-bold">{localTarget === 'k8s' ? 'Kubernetes' : 'Docker'} Deployment</h1>
                  <p className="text-sm text-muted-foreground">How would you like to set up your deployment?</p>
                </div>
                <CardChoice
                  left={{
                    title: 'Git Repo',
                    description: 'Build and deploy a single application from a Git repository',
                    icon: GitBranch,
                    tags: ['Single App', 'Paketo', 'Dockerfile'],
                    onClick: () => { setDockerMode('scratch'); setWizardStep(4) },
                  }}
                  right={{
                    title: 'Docker Compose',
                    description: 'Import services from a docker-compose.yml in a Git repository',
                    icon: Layers,
                    tags: ['Multi-Service', 'Auto-Import'],
                    onClick: () => { setDockerMode('compose'); setWizardStep(4) },
                  }}
                />
              </div>
            )}

            {/* ── Step 3 (AWS): EC2 vs ECS ── */}
            {wizardStep === 3 && location === 'cloud' && cloudProvider === 'aws' && (
              <div className="w-full max-w-2xl space-y-6">
                <div className="space-y-1 text-center">
                  <h1 className="text-xl font-bold">AWS Target</h1>
                  <p className="text-sm text-muted-foreground">Choose which type of AWS resource to deploy to</p>
                </div>
                <CardChoice
                  left={{
                    title: 'EC2',
                    description: 'Deploy to an EC2 instance via SSH',
                    icon: Server,
                    tags: ['Virtual Machine', 'SSH Deploy'],
                    onClick: () => { setCloudTarget('ec2'); setWizardStep(4) },
                  }}
                  right={{
                    title: 'ECS Fargate',
                    description: 'Deploy to an ECS Fargate service with container registry',
                    icon: Container,
                    tags: ['Serverless', 'ECR', 'ALB'],
                    onClick: () => { setCloudTarget('ecs'); setWizardStep(4) },
                  }}
                />
              </div>
            )}

            {/* ── Step 3 (Azure): VM vs ACI ── */}
            {wizardStep === 3 && location === 'cloud' && cloudProvider === 'azure' && (
              <div className="w-full max-w-2xl space-y-6">
                <div className="space-y-1 text-center">
                  <h1 className="text-xl font-bold">Azure Target</h1>
                  <p className="text-sm text-muted-foreground">Choose which type of Azure resource to deploy to</p>
                </div>
                <CardChoice
                  left={{
                    title: 'Azure VM',
                    description: 'Deploy to a Virtual Machine via SSH',
                    icon: Server,
                    tags: ['Virtual Machine', 'SSH Deploy'],
                    onClick: () => { setCloudTarget('vm'); setWizardStep(4) },
                  }}
                  right={{
                    title: 'ACI',
                    description: 'Deploy to an Azure Container Instances group',
                    icon: Container,
                    tags: ['Container Group', 'ACR'],
                    onClick: () => { setCloudTarget('aci'); setWizardStep(4) },
                  }}
                />
              </div>
            )}

            {/* ── Step 4: Form ── */}
            {wizardStep === 4 && !isComposeForm && (
              <div className="w-full max-w-md space-y-6">
                <div className="space-y-1">
                  <h1 className="text-xl font-bold">New Deployment</h1>
                  <p className="text-sm text-muted-foreground">Connect a Git repo to deploy</p>
                </div>

                {/* Cloud: resource picker */}
                {location === 'cloud' && cloudResources().length === 0 && (
                  <div className="rounded-lg border border-border bg-card px-3 py-2.5 text-sm text-muted-foreground">
                    You don&apos;t have any applied {cloudResourceLabel()} resources yet.{' '}
                    <button type="button" onClick={() => navigate('/new-resource')} className="underline text-foreground">
                      Go provision one
                    </button>{' '}first, then come back here.
                  </div>
                )}

                <form onSubmit={submit} className="space-y-4">
                  <FormField label="Stack name">
                    <input value={name} onChange={e => setName(e.target.value)} placeholder="my-app" required className={inputCls} />
                  </FormField>

                  {/* Repository: pick a stored one or add a new one (auto-registered in the store). */}
                  {storedRepos.length > 0 && (
                    <FormField label="Repository" hint="reuse a stored repo or add a new one">
                      <select value={repoId} onChange={e => selectStoredRepo(e.target.value)} className={selectCls}>
                        <option value="">+ Add a new repository</option>
                        {storedRepos.map((r) => (
                          <option key={r.id} value={r.id}>{r.name} — {r.repo_url} ({r.branch})</option>
                        ))}
                      </select>
                    </FormField>
                  )}

                  {repoId ? (
                    <div className="rounded-lg border border-border bg-card px-3 py-2.5 text-xs text-muted-foreground">
                      Deploying from stored repo <span className="font-mono text-foreground">{repo}</span>
                      <span className="ml-1">on <span className="font-mono text-foreground">{branch}</span></span>
                      {' '}· credentials reused from the store.
                    </div>
                  ) : (
                    <>
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
                    </>
                  )}
                  <FormField label="Framework" hint="auto-generates Procfile + DB env vars">
                    <div className="flex flex-wrap gap-1.5">
                      {DECLARED_FRAMEWORK_OPTIONS.map((opt) => (
                        <button
                          key={opt.value} type="button"
                          onClick={() => setDeclaredFramework(opt.value)}
                          className={cn(
                            'rounded-full border px-2.5 py-1 text-xs transition-colors',
                            declaredFramework === opt.value
                              ? 'border-primary bg-primary/10 text-primary font-medium'
                              : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground',
                          )}
                        >
                          {opt.label}
                          {opt.sub && <span className="ml-1 opacity-60">{opt.sub}</span>}
                        </button>
                      ))}
                    </div>
                  </FormField>

                  {/* Cloud resource picker */}
                  {location === 'cloud' && cloudResources().length > 0 && (
                    <FormField label={cloudResourceLabel()} hint="from the Resources page">
                      <select
                        value={cloudResourceEffectiveId()}
                        onChange={(e) => {
                          if (cloudTarget === 'ec2' || cloudTarget === 'vm') setVmResourceId(e.target.value)
                          else if (cloudTarget === 'ecs') setEcsResourceId(e.target.value)
                          else if (cloudTarget === 'aci') setAciResourceId(e.target.value)
                        }}
                        className={selectCls}
                        required
                      >
                        {cloudResources().map((res) => (
                          <option key={res.id} value={res.id}>
                            {res.name} ({provisionedTypeLabel[res.provider]?.[res.resource_type] ?? res.resource_type})
                          </option>
                        ))}
                      </select>
                    </FormField>
                  )}

                  {location === 'cloud' && (cloudTarget === 'ecs' || cloudTarget === 'aci') && (
                    <div className="rounded-lg border border-blue-500/20 bg-blue-500/5 px-3.5 py-2.5 text-xs text-muted-foreground space-y-1.5">
                      <p className="font-medium text-foreground">How {cloudTarget === 'ecs' ? 'ECS' : 'ACI'} deployment works</p>
                      <ol className="list-decimal list-inside space-y-0.5">
                        <li>Provision a <strong>{cloudTarget === 'ecs' ? 'ECR registry' : 'ACR registry'}</strong> from the <button type="button" onClick={() => navigate('/new-resource')} className="underline">Resources page</button></li>
                        <li>Provision an <strong>{cloudTarget === 'ecs' ? 'ECS service' : 'ACI container group'}</strong> (any placeholder image is fine)</li>
                        <li>Link the registry to the {cloudTarget === 'ecs' ? 'ECS service' : 'ACI container group'} on its detail page</li>
                        <li>Deploy here — AutoDevOps builds your app, pushes to the registry, and redeploys</li>
                      </ol>
                    </div>
                  )}

                  {/* Domain field */}
                  {location === 'local' && <DomainField value={localDomain} onChange={setLocalDomain} />}
                  {location === 'cloud' && (() => {
                    let vmIP: string | undefined
                    if (cloudTarget === 'ec2' || cloudTarget === 'vm') {
                      const res = appliedResources.find((r) => r.id === effectiveVmResourceId)
                      const o: Record<string, string> = res?.outputs ? (() => { try { return JSON.parse(res.outputs) } catch { return {} } })() : {}
                      vmIP = o.vm_public_ip || o.ec2_public_ip
                    }
                    return (
                      <>
                        <DomainField value={localDomain} onChange={setLocalDomain} targetIP={vmIP} />
                        {vmIP && (
                          <p className="text-xs text-muted-foreground -mt-1">
                            Point your domain to the VM&apos;s IP: <code className="rounded bg-muted px-1 font-mono">{vmIP}</code>
                          </p>
                        )}
                        {!localDomain && (cloudTarget === 'aci' || cloudTarget === 'ecs') && (
                          <p className="text-xs text-muted-foreground -mt-1">
                            Optional — {cloudTarget === 'aci' ? 'ACI' : 'ECS'} gets a public URL automatically. Add a domain above for HTTPS.
                          </p>
                        )}
                      </>
                    )
                  })()}

                  {/* Kubernetes-only: namespace override + per-app tuning */}
                  {location === 'local' && localTarget === 'k8s' && (
                    <>
                      <FormField label="Namespace" hint="optional — defaults to autodevops-<stack>">
                        <input value={namespaceOverride} onChange={e => setNamespaceOverride(e.target.value)} placeholder="autodevops-my-app" className={inputCls} />
                      </FormField>
                      <K8sOptions onChange={setK8sConfig} />
                    </>
                  )}

                  <label className="flex items-start gap-2 rounded-lg border border-border bg-card px-3 py-2.5 text-xs text-muted-foreground cursor-pointer">
                    <input type="checkbox" className="mt-0.5" checked={complianceEnforce} onChange={(e) => setComplianceEnforce(e.target.checked)} />
                    <span>
                      <span className="font-medium text-foreground">Enforce the compliance gate</span><br />
                      Off by default. When on, a deploy is blocked by high+ <span className="font-medium text-foreground">secret, Dockerfile or IaC</span> findings (gitleaks, hadolint, tfsec) and <span className="font-medium text-foreground">critical CVEs in your own dependencies</span>. Base-image OS CVEs stay informational.
                    </span>
                  </label>

                  {error && (
                    <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
                      <AlertCircle className="size-4 shrink-0" />{error}
                    </div>
                  )}
                  <Button type="submit" className="w-full h-10 gap-2 mt-2" disabled={loading || (location === 'cloud' && cloudResources().length === 0)}>
                    {loading && <Loader2 className="size-4 animate-spin" />}
                    {loading ? 'Creating deployment…' : 'Create deployment'}
                  </Button>
                </form>
              </div>
            )}

            {/* ── Step 4: Compose import ── */}
            {isComposeForm && <ComposeImportForm target={localTarget === 'k8s' ? 'k8s' : 'container'} />}
          </>
        )}

        {/* ── Success ── */}
        {formStep === 'success' && success && (
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
              <Button variant="outline" className="flex-1" onClick={() => {
                setFormStep('form'); setWizardStep(1); setLocation(null); setLocalTarget(null)
                setCloudProvider(null); setDockerMode(null); setCloudTarget(null)
                setName(''); setRepo(''); setBranch('main'); setToken(''); setRepoId(''); setDeclaredFramework('')
                setLocalDomain(''); setVmResourceId(''); setEcsResourceId(''); setAciResourceId('')
                setNamespaceOverride(''); setK8sConfig(undefined); setComplianceEnforce(false)
                setSuccess(null)
              }}>
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
