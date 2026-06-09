import { useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Server, Cloud, ArrowLeft, Loader2, CheckCircle2, AlertCircle, Globe, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { cn } from '@/lib/utils'

type Step = 'choose' | 'local' | 'cloud' | 'success-local' | 'success-cloud'
type LocalTarget = 'k8s' | 'container'
type CloudProvider = 'aws' | 'azure'
type AWSResource = 'ecs' | 'vm' | 'rds'
type AzureResource = 'aci' | 'vm' | 'db'

const inputCls =
  'h-10 w-full rounded-lg border border-border bg-card px-3 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/30 transition-colors'

const selectCls = cn(inputCls, 'cursor-pointer')

function TargetPill({ label, sub, active, onClick }: {
  label: string; sub?: string; active: boolean; onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex flex-1 flex-col items-center justify-center gap-0.5 rounded-lg border py-3 text-sm font-medium transition-colors',
        active ? 'border-ring/50 bg-accent text-foreground shadow-sm' : 'border-border text-muted-foreground hover:bg-accent/60',
      )}
    >
      {label}
      {sub && <span className="text-[0.65rem] font-normal text-muted-foreground">{sub}</span>}
    </button>
  )
}

function FormField({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between">
        <label className="text-xs font-medium text-muted-foreground">{label}</label>
        {hint && <span className="text-[0.65rem] text-muted-foreground/60">{hint}</span>}
      </div>
      {children}
    </div>
  )
}

function SectionDivider({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3 py-1">
      <div className="h-px flex-1 bg-border" />
      <span className="text-[0.65rem] font-semibold uppercase tracking-wider text-muted-foreground">{label}</span>
      <div className="h-px flex-1 bg-border" />
    </div>
  )
}

// ─── Domain field with inline DNS checker ────────────────────────
type DNSStatus = 'idle' | 'checking' | 'ok' | 'fail' | 'error'

function DomainField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [status, setStatus] = useState<DNSStatus>('idle')
  const [info, setInfo] = useState<{ resolved_ips?: string[]; server_ip?: string; error?: string } | null>(null)
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null)

  async function check(domain: string) {
    if (!domain) { setStatus('idle'); setInfo(null); return }
    setStatus('checking'); setInfo(null)
    try {
      const res = await apiFetch<{ ok: boolean; resolved_ips?: string[]; server_ip?: string; error?: string }>(
        `/dns/check?domain=${encodeURIComponent(domain)}`
      )
      setStatus(res.ok ? 'ok' : 'fail')
      setInfo({ resolved_ips: res.resolved_ips, server_ip: res.server_ip, error: res.error })
    } catch {
      setStatus('error')
      setInfo(null)
    }
  }

  function handleChange(v: string) {
    onChange(v)
    setStatus('idle'); setInfo(null)
    if (debounce.current) clearTimeout(debounce.current)
    if (v.includes('.')) {
      debounce.current = setTimeout(() => check(v), 800)
    }
  }

  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between">
        <label className="text-xs font-medium text-muted-foreground">Custom domain <span className="text-muted-foreground/60">(optional)</span></label>
        {status !== 'idle' && (
          <button type="button" onClick={() => check(value)} className="flex items-center gap-1 text-[0.65rem] text-muted-foreground hover:text-foreground">
            <RefreshCw className="size-2.5" /> re-check
          </button>
        )}
      </div>

      <div className="relative">
        <Globe className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <input
          value={value}
          onChange={e => handleChange(e.target.value)}
          placeholder="app.example.com"
          className={cn(inputCls, 'pl-9 pr-9')}
        />
        <div className="absolute right-3 top-1/2 -translate-y-1/2">
          {status === 'checking' && <Loader2 className="size-4 animate-spin text-muted-foreground" />}
          {status === 'ok'       && <CheckCircle2 className="size-4 text-success" />}
          {status === 'fail'     && <AlertCircle className="size-4 text-warning" />}
          {status === 'error'    && <AlertCircle className="size-4 text-critical" />}
        </div>
      </div>

      {status === 'ok' && (
        <p className="flex items-center gap-1.5 text-xs text-success">
          <CheckCircle2 className="size-3 shrink-0" />
          Domain resolves to this server — TLS cert will be issued automatically
        </p>
      )}
      {status === 'fail' && info && (
        <div className="rounded-lg border border-warning/25 bg-warning/8 px-3 py-2.5 space-y-1">
          <p className="text-xs font-medium text-warning">Domain not pointing to this server yet</p>
          <p className="text-xs text-muted-foreground">
            Add an <span className="font-mono font-medium text-foreground">A record</span> pointing to{' '}
            <span className="font-mono font-medium text-foreground">{info.server_ip}</span>
          </p>
          {info.resolved_ips && info.resolved_ips.length > 0 && (
            <p className="text-xs text-muted-foreground">
              Currently resolves to: <span className="font-mono">{info.resolved_ips.join(', ')}</span>
            </p>
          )}
          <p className="text-[0.65rem] text-muted-foreground/70">DNS changes can take up to 24h. You can still proceed and set up DNS later.</p>
        </div>
      )}
      {status === 'error' && info?.error && (
        <p className="text-xs text-critical">{info.error}</p>
      )}
    </div>
  )
}

// ─── AWS ECS sub-fields ───────────────────────────────────────────
function ECSFields({ v, set }: { v: Record<string,string>; set: (k: string, val: string) => void }) {
  return (
    <>
      <SectionDivider label="ECS Fargate options" />
      <FormField label="Container image">
        <input value={v.image ?? ''} onChange={e => set('image', e.target.value)} placeholder="nginx:latest" className={inputCls} />
      </FormField>
      <div className="grid grid-cols-3 gap-3">
        <FormField label="CPU (vCPU units)">
          <select value={v.cpu ?? '256'} onChange={e => set('cpu', e.target.value)} className={selectCls}>
            {['256','512','1024','2048','4096'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="Memory (MB)">
          <select value={v.memory ?? '512'} onChange={e => set('memory', e.target.value)} className={selectCls}>
            {['512','1024','2048','4096','8192'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="Port">
          <input value={v.port ?? '80'} onChange={e => set('port', e.target.value)} placeholder="80" className={inputCls} />
        </FormField>
      </div>
      <FormField label="Desired count" hint="number of running tasks">
        <select value={v.desired_count ?? '1'} onChange={e => set('desired_count', e.target.value)} className={selectCls}>
          {['1','2','3','4','5'].map(v => <option key={v} value={v}>{v}</option>)}
        </select>
      </FormField>
    </>
  )
}

// ─── AWS EC2 sub-fields ───────────────────────────────────────────
function EC2Fields({ v, set }: { v: Record<string,string>; set: (k: string, val: string) => void }) {
  return (
    <>
      <SectionDivider label="EC2 options" />
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Instance type">
          <select value={v.instance_type ?? 't3.micro'} onChange={e => set('instance_type', e.target.value)} className={selectCls}>
            {['t3.micro','t3.small','t3.medium','t3.large','t2.micro','m5.large'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="AMI ID" hint="leave blank for Amazon Linux 2023">
          <input value={v.ami ?? ''} onChange={e => set('ami', e.target.value)} placeholder="auto" className={inputCls} />
        </FormField>
      </div>
      <FormField label="Key pair name" hint="optional, for SSH access">
        <input value={v.key_name ?? ''} onChange={e => set('key_name', e.target.value)} placeholder="my-key-pair" className={inputCls} />
      </FormField>
    </>
  )
}

// ─── AWS RDS sub-fields ───────────────────────────────────────────
function RDSFields({ v, set }: { v: Record<string,string>; set: (k: string, val: string) => void }) {
  return (
    <>
      <SectionDivider label="RDS options" />
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Engine">
          <select value={v.engine ?? 'postgres'} onChange={e => set('engine', e.target.value)} className={selectCls}>
            {['postgres','mysql','mariadb'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="Instance class">
          <select value={v.instance_class ?? 'db.t3.micro'} onChange={e => set('instance_class', e.target.value)} className={selectCls}>
            {['db.t3.micro','db.t3.small','db.t3.medium','db.m5.large'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Database name">
          <input value={v.db_name ?? ''} onChange={e => set('db_name', e.target.value)} placeholder="mydb" className={inputCls} />
        </FormField>
        <FormField label="Master username">
          <input value={v.username ?? ''} onChange={e => set('username', e.target.value)} placeholder="admin" className={inputCls} />
        </FormField>
      </div>
    </>
  )
}

// ─── Azure ACI sub-fields ─────────────────────────────────────────
function ACIFields({ v, set }: { v: Record<string,string>; set: (k: string, val: string) => void }) {
  return (
    <>
      <SectionDivider label="Container instance options" />
      <FormField label="Container image">
        <input value={v.image ?? ''} onChange={e => set('image', e.target.value)} placeholder="nginx:latest" className={inputCls} />
      </FormField>
      <div className="grid grid-cols-3 gap-3">
        <FormField label="CPU cores">
          <select value={v.cpu ?? '1'} onChange={e => set('cpu', e.target.value)} className={selectCls}>
            {['0.5','1','2','4'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="Memory (GB)">
          <select value={v.memory_gb ?? '1.5'} onChange={e => set('memory_gb', e.target.value)} className={selectCls}>
            {['0.5','1','1.5','2','4','8'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="Port">
          <input value={v.port ?? '80'} onChange={e => set('port', e.target.value)} placeholder="80" className={inputCls} />
        </FormField>
      </div>
      <FormField label="OS type">
        <select value={v.os_type ?? 'Linux'} onChange={e => set('os_type', e.target.value)} className={selectCls}>
          <option value="Linux">Linux</option>
          <option value="Windows">Windows</option>
        </select>
      </FormField>
    </>
  )
}

// ─── Azure VM sub-fields ──────────────────────────────────────────
function AzureVMFields({ v, set }: { v: Record<string,string>; set: (k: string, val: string) => void }) {
  return (
    <>
      <SectionDivider label="Virtual machine options" />
      <div className="grid grid-cols-2 gap-3">
        <FormField label="VM size">
          <select value={v.size ?? 'Standard_B1s'} onChange={e => set('size', e.target.value)} className={selectCls}>
            {['Standard_B1s','Standard_B2s','Standard_B2ms','Standard_D2s_v3','Standard_D4s_v3','Standard_F2s_v2'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
        <FormField label="Admin username">
          <input value={v.admin_username ?? ''} onChange={e => set('admin_username', e.target.value)} placeholder="azureuser" className={inputCls} />
        </FormField>
      </div>
      <FormField label="OS image">
        <select value={v.image_sku ?? 'Ubuntu2204'} onChange={e => set('image_sku', e.target.value)} className={selectCls}>
          <option value="Ubuntu2204">Ubuntu 22.04 LTS</option>
          <option value="Ubuntu2004">Ubuntu 20.04 LTS</option>
          <option value="Windows2022Datacenter">Windows Server 2022</option>
          <option value="Debian11">Debian 11</option>
        </select>
      </FormField>
    </>
  )
}

// ─── Azure DB sub-fields ──────────────────────────────────────────
function AzureDBFields({ v, set }: { v: Record<string,string>; set: (k: string, val: string) => void }) {
  return (
    <>
      <SectionDivider label="Database options" />
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Engine">
          <select value={v.engine ?? 'postgres'} onChange={e => set('engine', e.target.value)} className={selectCls}>
            <option value="postgres">PostgreSQL</option>
            <option value="mysql">MySQL</option>
          </select>
        </FormField>
        <FormField label="SKU">
          <select value={v.sku ?? 'B_Gen5_1'} onChange={e => set('sku', e.target.value)} className={selectCls}>
            {['B_Gen5_1','B_Gen5_2','GP_Gen5_2','GP_Gen5_4','MO_Gen5_2'].map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        </FormField>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Database name">
          <input value={v.db_name ?? ''} onChange={e => set('db_name', e.target.value)} placeholder="mydb" className={inputCls} />
        </FormField>
        <FormField label="Admin login">
          <input value={v.admin_login ?? ''} onChange={e => set('admin_login', e.target.value)} placeholder="adminuser" className={inputCls} />
        </FormField>
      </div>
    </>
  )
}

// ─── Main page ────────────────────────────────────────────────────
export function NewDeploymentPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStep] = useState<Step>('choose')

  // Local
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [localTarget, setLocalTarget] = useState<LocalTarget>('k8s')
  const [localDomain, setLocalDomain] = useState('')

  // Cloud
  const [cloudProvider, setCloudProvider] = useState<CloudProvider>('aws')
  const [cloudAccountID, setCloudAccountID] = useState('')
  const [cloudName, setCloudName] = useState('')
  const [region, setRegion] = useState('')
  const [awsResource, setAWSResource] = useState<AWSResource>('ecs')
  const [azureResource, setAzureResource] = useState<AzureResource>('aci')
  const [resourceOpts, setResourceOpts] = useState<Record<string, string>>({})
  const [cloudDomain, setCloudDomain] = useState('')

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [successLocal, setSuccessLocal] = useState<{ webhookURL: string; webhookSecret: string; name: string } | null>(null)
  const [successCloud, setSuccessCloud] = useState<{ name: string; provider: string; resourceType: string } | null>(null)

  const cloudAccounts = qc.getQueryData<{ id: string; provider: string; name: string }[]>(['cloud-accounts']) ?? []
  const filteredAccounts = cloudAccounts.filter((a) => a.provider === cloudProvider)
  const awsRegions = ['us-east-1', 'us-west-2', 'eu-west-1', 'eu-west-2', 'ap-southeast-1']
  const azureLocations = ['uksouth', 'westeurope', 'eastus', 'northeurope', 'southeastasia']

  function setOpt(k: string, val: string) {
    setResourceOpts(prev => ({ ...prev, [k]: val }))
  }

  function clearOpts() { setResourceOpts({}) }

  async function submitLocal(e: React.FormEvent) {
    e.preventDefault(); setLoading(true); setError('')
    try {
      const body: Record<string, unknown> = { name, repo_url: repo, branch, target_type: localTarget }
      if (token) body.git_auth = { type: 'token', token }
      if (localDomain) body.domain = localDomain
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
    e.preventDefault(); setLoading(true); setError('')
    try {
      const resourceType = cloudProvider === 'aws' ? awsResource : azureResource
      await apiFetch('/terraform/runs', {
        method: 'POST',
        body: JSON.stringify({
          name: cloudName, provider: cloudProvider, cloud_account_id: cloudAccountID,
          region, resource_type: resourceType, resource_opts: resourceOpts,
          domain: cloudDomain || undefined,
        }),
      })
      setSuccessCloud({ name: cloudName, provider: cloudProvider, resourceType })
      setStep('success-cloud')
      qc.invalidateQueries({ queryKey: ['deployments'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to initiate terraform run')
    } finally { setLoading(false) }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      {/* Top bar */}
      <div className="flex h-14 items-center gap-3 border-b border-border px-6">
        <button
          onClick={() => step === 'choose' ? navigate(-1) : (step === 'local' || step === 'cloud') ? setStep('choose') : navigate('/deployments')}
          className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
          {step === 'choose' ? 'Back' : (step === 'local' || step === 'cloud') ? 'Start over' : 'View deployments'}
        </button>
        <span className="text-sm font-semibold">New Deployment</span>
      </div>

      <div className="flex flex-1 items-start justify-center p-8 pt-12">

        {/* ── STEP 1: Choose ── */}
        {step === 'choose' && (
          <div className="w-full max-w-2xl space-y-8">
            <div className="text-center space-y-1">
              <h1 className="text-2xl font-bold">Where do you want to deploy?</h1>
              <p className="text-sm text-muted-foreground">Choose a deployment target to get started</p>
            </div>
            <div className="flex items-stretch gap-0">
              <button
                onClick={() => setStep('local')}
                className="group flex flex-1 flex-col items-center gap-5 rounded-2xl border border-border bg-card p-10 text-center transition-all duration-200 hover:scale-[1.03] hover:border-ring/40 hover:shadow-xl"
              >
                <div className="flex size-16 items-center justify-center rounded-2xl border border-border bg-background transition-colors group-hover:border-ring/30 group-hover:bg-accent">
                  <Server className="size-7 text-foreground" />
                </div>
                <div className="space-y-1.5">
                  <p className="text-lg font-semibold">Local</p>
                  <p className="text-xs text-muted-foreground leading-relaxed max-w-[180px]">
                    Deploy to your local Kubernetes cluster or as a Docker container on this machine
                  </p>
                </div>
                <div className="flex gap-2 flex-wrap justify-center">
                  {['Kubernetes', 'Container'].map((t) => (
                    <span key={t} className="rounded-full border border-border bg-muted px-2.5 py-0.5 text-[0.65rem] font-medium text-muted-foreground">{t}</span>
                  ))}
                </div>
              </button>

              <div className="flex flex-col items-center justify-center px-6 gap-2">
                <div className="flex-1 w-px bg-border" />
                <span className="text-[0.65rem] font-medium text-muted-foreground/60 uppercase tracking-widest">or</span>
                <div className="flex-1 w-px bg-border" />
              </div>

              <button
                onClick={() => setStep('cloud')}
                className="group flex flex-1 flex-col items-center gap-5 rounded-2xl border border-border bg-card p-10 text-center transition-all duration-200 hover:scale-[1.03] hover:border-ring/40 hover:shadow-xl"
              >
                <div className="flex size-16 items-center justify-center rounded-2xl border border-border bg-background transition-colors group-hover:border-ring/30 group-hover:bg-accent">
                  <Cloud className="size-7 text-foreground" />
                </div>
                <div className="space-y-1.5">
                  <p className="text-lg font-semibold">Cloud</p>
                  <p className="text-xs text-muted-foreground leading-relaxed max-w-[180px]">
                    Provision infrastructure on AWS or Azure via the Terraform engine
                  </p>
                </div>
                <div className="flex gap-2 flex-wrap justify-center">
                  {['ECS Fargate', 'EC2', 'RDS', 'ACI', 'VM', 'Database'].map((t) => (
                    <span key={t} className="rounded-full border border-border bg-muted px-2.5 py-0.5 text-[0.65rem] font-medium text-muted-foreground">{t}</span>
                  ))}
                </div>
              </button>
            </div>
          </div>
        )}

        {/* ── STEP 2a: Local ── */}
        {step === 'local' && (
          <div className="w-full max-w-md space-y-6">
            <div className="space-y-1">
              <h1 className="text-xl font-bold">Local Deployment</h1>
              <p className="text-sm text-muted-foreground">Connect a Git repo and choose a target</p>
            </div>
            <form onSubmit={submitLocal} className="space-y-4">
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
                  <TargetPill label="Kubernetes" sub="kind cluster" active={localTarget === 'k8s'} onClick={() => setLocalTarget('k8s')} />
                  <TargetPill label="Container" sub="Docker SDK" active={localTarget === 'container'} onClick={() => setLocalTarget('container')} />
                </div>
              </FormField>
              <DomainField value={localDomain} onChange={setLocalDomain} />
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
        )}

        {/* ── STEP 2b: Cloud ── */}
        {step === 'cloud' && (
          <div className="w-full max-w-md space-y-6">
            <div className="space-y-1">
              <h1 className="text-xl font-bold">Cloud Deployment</h1>
              <p className="text-sm text-muted-foreground">Provision infrastructure via Terraform</p>
            </div>
            <form onSubmit={submitCloud} className="space-y-4">
              <FormField label="Provider">
                <div className="flex gap-2">
                  <TargetPill label="AWS" active={cloudProvider === 'aws'} onClick={() => { setCloudProvider('aws'); setCloudAccountID(''); clearOpts() }} />
                  <TargetPill label="Azure" active={cloudProvider === 'azure'} onClick={() => { setCloudProvider('azure'); setCloudAccountID(''); clearOpts() }} />
                </div>
              </FormField>

              <FormField label="Cloud account">
                <select value={cloudAccountID} onChange={e => setCloudAccountID(e.target.value)} required className={selectCls}>
                  <option value="">Select account…</option>
                  {filteredAccounts.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
                </select>
                {filteredAccounts.length === 0 && (
                  <p className="text-xs text-muted-foreground mt-1">
                    No {cloudProvider.toUpperCase()} accounts — register one in{' '}
                    <button type="button" onClick={() => navigate('/settings')} className="underline">Settings</button>.
                  </p>
                )}
              </FormField>

              <div className="grid grid-cols-2 gap-3">
                <FormField label="Stack name">
                  <input value={cloudName} onChange={e => setCloudName(e.target.value)} placeholder="my-stack" required className={inputCls} />
                </FormField>
                <FormField label={cloudProvider === 'aws' ? 'Region' : 'Location'}>
                  <select value={region} onChange={e => setRegion(e.target.value)} required className={selectCls}>
                    <option value="">Select…</option>
                    {(cloudProvider === 'aws' ? awsRegions : azureLocations).map(r => <option key={r} value={r}>{r}</option>)}
                  </select>
                </FormField>
              </div>

              <FormField label="Resource type">
                <div className="flex gap-2">
                  {cloudProvider === 'aws' ? (
                    <>
                      <TargetPill label="ECS Fargate" active={awsResource === 'ecs'} onClick={() => { setAWSResource('ecs'); clearOpts() }} />
                      <TargetPill label="EC2 VM" active={awsResource === 'vm'} onClick={() => { setAWSResource('vm'); clearOpts() }} />
                      <TargetPill label="RDS DB" active={awsResource === 'rds'} onClick={() => { setAWSResource('rds'); clearOpts() }} />
                    </>
                  ) : (
                    <>
                      <TargetPill label="Container" sub="ACI" active={azureResource === 'aci'} onClick={() => { setAzureResource('aci'); clearOpts() }} />
                      <TargetPill label="VM" active={azureResource === 'vm'} onClick={() => { setAzureResource('vm'); clearOpts() }} />
                      <TargetPill label="Database" active={azureResource === 'db'} onClick={() => { setAzureResource('db'); clearOpts() }} />
                    </>
                  )}
                </div>
              </FormField>

              {/* Dynamic sub-fields based on resource type */}
              {cloudProvider === 'aws' && awsResource === 'ecs' && <ECSFields v={resourceOpts} set={setOpt} />}
              {cloudProvider === 'aws' && awsResource === 'vm'  && <EC2Fields v={resourceOpts} set={setOpt} />}
              {cloudProvider === 'aws' && awsResource === 'rds' && <RDSFields v={resourceOpts} set={setOpt} />}
              {cloudProvider === 'azure' && azureResource === 'aci' && <ACIFields v={resourceOpts} set={setOpt} />}
              {cloudProvider === 'azure' && azureResource === 'vm'  && <AzureVMFields v={resourceOpts} set={setOpt} />}
              {cloudProvider === 'azure' && azureResource === 'db'  && <AzureDBFields v={resourceOpts} set={setOpt} />}

              <DomainField value={cloudDomain} onChange={setCloudDomain} />

              {error && (
                <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
                  <AlertCircle className="size-4 shrink-0" />{error}
                </div>
              )}
              <Button type="submit" className="w-full h-10 gap-2 mt-2" disabled={loading}>
                {loading && <Loader2 className="size-4 animate-spin" />}
                {loading ? 'Initiating plan…' : 'Generate Terraform plan'}
              </Button>
            </form>
          </div>
        )}

        {/* ── SUCCESS: Local ── */}
        {step === 'success-local' && successLocal && (
          <div className="w-full max-w-md space-y-6">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-6 text-success shrink-0" />
              <div>
                <p className="font-semibold">Deployment registered</p>
                <p className="text-sm text-muted-foreground font-mono">{successLocal.name}</p>
              </div>
            </div>
            <div className="rounded-xl border border-border bg-card p-5 space-y-4">
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">GitHub Webhook Setup</p>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Payload URL</p>
                <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs break-all select-all">
                  http://YOUR_SERVER:8080{successLocal.webhookURL}
                </p>
              </div>
              <div className="space-y-1">
                <p className="text-[0.7rem] text-muted-foreground">Secret</p>
                <p className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs select-all">{successLocal.webhookSecret}</p>
              </div>
              <p className="text-xs text-muted-foreground">
                Content type: <span className="font-mono">application/json</span> · Event: <span className="font-mono">push</span>
              </p>
            </div>
            <p className="text-xs text-muted-foreground">
              Or trigger immediately from CLI:<br />
              <span className="font-mono">autodev deploy --name {successLocal.name} --repo &lt;url&gt; --target {localTarget} --trigger</span>
            </p>
            <div className="flex gap-3">
              <Button variant="outline" className="flex-1" onClick={() => { setStep('choose'); setName(''); setRepo(''); setBranch('main'); setToken('') }}>
                New deployment
              </Button>
              <Button className="flex-1" onClick={() => navigate('/deployments')}>View deployments</Button>
            </div>
          </div>
        )}

        {/* ── SUCCESS: Cloud ── */}
        {step === 'success-cloud' && successCloud && (
          <div className="w-full max-w-md space-y-6">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-6 text-success shrink-0" />
              <div>
                <p className="font-semibold">Terraform plan initiated</p>
                <p className="text-sm text-muted-foreground font-mono">{successCloud.name}</p>
              </div>
            </div>
            <div className="rounded-xl border border-border bg-card p-5 space-y-2">
              <p className="text-sm"><span className="text-muted-foreground">Provider:</span> <span className="font-medium uppercase">{successCloud.provider}</span></p>
              <p className="text-sm"><span className="text-muted-foreground">Resource:</span> <span className="font-medium">{successCloud.resourceType}</span></p>
              <p className="text-sm"><span className="text-muted-foreground">Stack:</span> <span className="font-mono">{successCloud.name}</span></p>
            </div>
            <p className="text-xs text-muted-foreground">
              The Terraform plan is running. Watch the SSE stream for <span className="font-mono">terraform.run</span> events or check Deployments for status.
            </p>
            <div className="flex gap-3">
              <Button variant="outline" className="flex-1" onClick={() => { setStep('choose'); setCloudName(''); setRegion(''); setCloudAccountID(''); clearOpts() }}>
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
