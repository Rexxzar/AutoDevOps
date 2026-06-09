import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Server, Cloud, ArrowLeft, Loader2, CheckCircle2, AlertCircle, Container } from 'lucide-react'
import { Dialog } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'

type Step = 'choose' | 'local' | 'cloud' | 'success-local' | 'success-cloud'
type LocalTarget = 'k8s' | 'container'
type CloudProvider = 'aws' | 'azure'
type AWSResource = 'ecs' | 'vm' | 'rds'
type AzureResource = 'aci' | 'vm' | 'db'

interface SuccessLocalData { webhookURL: string; webhookSecret: string; name: string }
interface SuccessCloudData { name: string; provider: string; resourceType: string }

interface Props { open: boolean; onClose: () => void }

const inputCls = 'h-9 w-full rounded-md border border-border bg-background px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/40'

function OptionCard({ icon: Icon, title, description, onClick }: {
  icon: React.ElementType; title: string; description: string; onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className="flex flex-1 flex-col items-center gap-3 rounded-xl border border-border bg-card p-6 text-center transition-all hover:border-ring/40 hover:bg-accent hover:shadow-md"
    >
      <div className="flex size-12 items-center justify-center rounded-xl border border-border bg-background">
        <Icon className="size-5 text-foreground" />
      </div>
      <div>
        <p className="font-semibold">{title}</p>
        <p className="mt-1 text-xs text-muted-foreground">{description}</p>
      </div>
    </button>
  )
}

function TargetPill({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex-1 rounded-lg border py-2.5 text-sm font-medium transition-colors',
        active ? 'border-ring/40 bg-accent text-foreground' : 'border-border text-muted-foreground hover:bg-accent/60',
      )}
    >
      {label}
    </button>
  )
}

export function NewDeploymentModal({ open, onClose }: Props) {
  const qc = useQueryClient()
  const [step, setStep] = useState<Step>('choose')

  // Local form state
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [localTarget, setLocalTarget] = useState<LocalTarget>('k8s')

  // Cloud form state
  const [cloudProvider, setCloudProvider] = useState<CloudProvider>('aws')
  const [cloudAccountID, setCloudAccountID] = useState('')
  const [cloudName, setCloudName] = useState('')
  const [region, setRegion] = useState('')
  const [awsResource, setAWSResource] = useState<AWSResource>('ecs')
  const [azureResource, setAzureResource] = useState<AzureResource>('aci')

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [successLocal, setSuccessLocal] = useState<SuccessLocalData | null>(null)
  const [successCloud, setSuccessCloud] = useState<SuccessCloudData | null>(null)

  const { data: cloudAccounts = [] } = useQueryClient().getQueryData<{id:string;provider:string;name:string}[]>(['cloud-accounts']) ? { data: qc.getQueryData<{id:string;provider:string;name:string}[]>(['cloud-accounts']) ?? [] } : { data: [] }

  function reset() {
    setStep('choose'); setName(''); setRepo(''); setBranch('main'); setToken('')
    setLocalTarget('k8s'); setCloudProvider('aws'); setCloudAccountID('')
    setCloudName(''); setRegion(''); setAWSResource('ecs'); setAzureResource('aci')
    setLoading(false); setError(''); setSuccessLocal(null); setSuccessCloud(null)
  }

  function handleClose() { reset(); onClose() }

  async function submitLocal(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true); setError('')
    try {
      const body: Record<string, unknown> = { name, repo_url: repo, branch, target_type: localTarget }
      if (token) body.git_auth = { type: 'token', token }
      const resp = await apiFetch<{ id: string; webhook_url: string; webhook_secret: string }>('/deployments', {
        method: 'POST', body: JSON.stringify(body),
      })
      setSuccessLocal({ webhookURL: resp.webhook_url, webhookSecret: resp.webhook_secret, name })
      setStep('success-local')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create deployment')
    } finally { setLoading(false) }
  }

  async function submitCloud(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true); setError('')
    try {
      const resourceType = cloudProvider === 'aws' ? awsResource : azureResource
      await apiFetch('/terraform/runs', {
        method: 'POST',
        body: JSON.stringify({
          name: cloudName,
          provider: cloudProvider,
          cloud_account_id: cloudAccountID,
          region,
          resource_type: resourceType,
        }),
      })
      setSuccessCloud({ name: cloudName, provider: cloudProvider, resourceType })
      setStep('success-cloud')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to initiate terraform run')
    } finally { setLoading(false) }
  }

  const awsRegions = ['us-east-1','us-west-2','eu-west-1','eu-west-2','ap-southeast-1']
  const azureLocations = ['uksouth','westeurope','eastus','northeurope','southeastasia']

  return (
    <Dialog open={open} onClose={handleClose} className="max-w-xl">
      {/* Header */}
      <div className="flex items-center gap-3 border-b border-border px-6 py-4">
        {step !== 'choose' && (
          <button onClick={() => { setStep('choose'); setError('') }} className="rounded p-1 text-muted-foreground hover:bg-accent">
            <ArrowLeft className="size-4" />
          </button>
        )}
        <h2 className="text-sm font-semibold">
          {step === 'choose' && 'New Deployment'}
          {step === 'local' && 'Local Deployment'}
          {step === 'cloud' && 'Cloud Deployment'}
          {(step === 'success-local' || step === 'success-cloud') && 'Deployment Created'}
        </h2>
      </div>

      <div className="p-6">
        {/* Step 1 — Choose */}
        {step === 'choose' && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">Where do you want to deploy?</p>
            <div className="flex gap-4">
              <OptionCard
                icon={Server}
                title="Local"
                description="K8s cluster or Docker container on this machine"
                onClick={() => setStep('local')}
              />
              <div className="flex items-center">
                <div className="h-24 w-px bg-border" />
              </div>
              <OptionCard
                icon={Cloud}
                title="Cloud"
                description="AWS or Azure via Terraform provisioning"
                onClick={() => setStep('cloud')}
              />
            </div>
          </div>
        )}

        {/* Step 2a — Local */}
        {step === 'local' && (
          <form onSubmit={submitLocal} className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">App name</label>
              <input value={name} onChange={e => setName(e.target.value)} placeholder="my-app" required className={inputCls} />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Git repository URL</label>
              <input value={repo} onChange={e => setRepo(e.target.value)} placeholder="https://github.com/you/my-app" required className={inputCls} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">Branch</label>
                <input value={branch} onChange={e => setBranch(e.target.value)} placeholder="main" className={inputCls} />
              </div>
              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">Git token <span className="text-muted-foreground/60">(private repos)</span></label>
                <input value={token} onChange={e => setToken(e.target.value)} placeholder="ghp_..." type="password" className={inputCls} />
              </div>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Target</label>
              <div className="flex gap-2">
                <TargetPill label="Kubernetes" active={localTarget === 'k8s'} onClick={() => setLocalTarget('k8s')} />
                <TargetPill label="Container" active={localTarget === 'container'} onClick={() => setLocalTarget('container')} />
              </div>
            </div>
            {error && (
              <div className="flex items-center gap-2 rounded-md border border-critical/25 bg-critical/8 px-3 py-2 text-sm text-critical">
                <AlertCircle className="size-4 shrink-0" />{error}
              </div>
            )}
            <Button type="submit" className="w-full gap-2" disabled={loading}>
              {loading && <Loader2 className="size-4 animate-spin" />}
              {loading ? 'Creating…' : 'Create Deployment'}
            </Button>
          </form>
        )}

        {/* Step 2b — Cloud */}
        {step === 'cloud' && (
          <form onSubmit={submitCloud} className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Provider</label>
              <div className="flex gap-2">
                <TargetPill label="AWS" active={cloudProvider === 'aws'} onClick={() => setCloudProvider('aws')} />
                <TargetPill label="Azure" active={cloudProvider === 'azure'} onClick={() => setCloudProvider('azure')} />
              </div>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Cloud account</label>
              <select
                value={cloudAccountID}
                onChange={e => setCloudAccountID(e.target.value)}
                required
                className={cn(inputCls, 'cursor-pointer')}
              >
                <option value="">Select account…</option>
                {cloudAccounts.filter(a => a.provider === cloudProvider).map(a => (
                  <option key={a.id} value={a.id}>{a.name}</option>
                ))}
              </select>
              {cloudAccounts.filter(a => a.provider === cloudProvider).length === 0 && (
                <p className="text-xs text-muted-foreground">No {cloudProvider.toUpperCase()} accounts registered — add one in Settings.</p>
              )}
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">Stack name</label>
                <input value={cloudName} onChange={e => setCloudName(e.target.value)} placeholder="my-stack" required className={inputCls} />
              </div>
              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground">{cloudProvider === 'aws' ? 'Region' : 'Location'}</label>
                <select value={region} onChange={e => setRegion(e.target.value)} required className={cn(inputCls, 'cursor-pointer')}>
                  <option value="">Select…</option>
                  {(cloudProvider === 'aws' ? awsRegions : azureLocations).map(r => (
                    <option key={r} value={r}>{r}</option>
                  ))}
                </select>
              </div>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs text-muted-foreground">Resource type</label>
              <div className="flex gap-2 flex-wrap">
                {cloudProvider === 'aws' ? (
                  <>
                    <TargetPill label="ECS Fargate" active={awsResource === 'ecs'} onClick={() => setAWSResource('ecs')} />
                    <TargetPill label="EC2 VM" active={awsResource === 'vm'} onClick={() => setAWSResource('vm')} />
                    <TargetPill label="RDS Database" active={awsResource === 'rds'} onClick={() => setAWSResource('rds')} />
                  </>
                ) : (
                  <>
                    <TargetPill label="Container Instance" active={azureResource === 'aci'} onClick={() => setAzureResource('aci')} />
                    <TargetPill label="VM" active={azureResource === 'vm'} onClick={() => setAzureResource('vm')} />
                    <TargetPill label="Database" active={azureResource === 'db'} onClick={() => setAzureResource('db')} />
                  </>
                )}
              </div>
            </div>
            {error && (
              <div className="flex items-center gap-2 rounded-md border border-critical/25 bg-critical/8 px-3 py-2 text-sm text-critical">
                <AlertCircle className="size-4 shrink-0" />{error}
              </div>
            )}
            <Button type="submit" className="w-full gap-2" disabled={loading}>
              {loading && <Loader2 className="size-4 animate-spin" />}
              {loading ? 'Initiating…' : 'Generate Terraform Plan'}
            </Button>
          </form>
        )}

        {/* Success — Local */}
        {step === 'success-local' && successLocal && (
          <div className="space-y-5">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-5 text-success" />
              <p className="text-sm font-medium">Deployment <span className="font-mono">{successLocal.name}</span> registered</p>
            </div>
            <div className="rounded-lg border border-border bg-muted/30 p-4 space-y-3">
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">GitHub Webhook</p>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Payload URL</p>
                <p className="font-mono text-xs break-all rounded bg-background p-2 border border-border">
                  http://YOUR_SERVER:8080{successLocal.webhookURL}
                </p>
              </div>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Secret</p>
                <p className="font-mono text-xs rounded bg-background p-2 border border-border select-all">{successLocal.webhookSecret}</p>
              </div>
              <p className="text-xs text-muted-foreground">Set Content type to <span className="font-mono">application/json</span> and event to <span className="font-mono">push</span>.</p>
            </div>
            <p className="text-xs text-muted-foreground">
              Or trigger immediately from the CLI: <span className="font-mono">autodev deploy --name {successLocal.name} --repo &lt;url&gt; --target k8s --trigger</span>
            </p>
            <Button className="w-full" onClick={handleClose}>Done</Button>
          </div>
        )}

        {/* Success — Cloud */}
        {step === 'success-cloud' && successCloud && (
          <div className="space-y-4">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-5 text-success" />
              <p className="text-sm font-medium">Terraform plan initiated for <span className="font-mono">{successCloud.name}</span></p>
            </div>
            <div className="rounded-lg border border-border bg-muted/30 p-4 space-y-2">
              <p className="text-xs text-muted-foreground">Provider: <span className="text-foreground font-medium uppercase">{successCloud.provider}</span></p>
              <p className="text-xs text-muted-foreground">Resource: <span className="text-foreground font-medium">{successCloud.resourceType}</span></p>
            </div>
            <p className="text-xs text-muted-foreground">Watch the SSE stream for <span className="font-mono">terraform.run</span> events, or run <span className="font-mono">autodev get deployments</span> to see the status.</p>
            <Button className="w-full" onClick={handleClose}>Done</Button>
          </div>
        )}
      </div>
    </Dialog>
  )
}
