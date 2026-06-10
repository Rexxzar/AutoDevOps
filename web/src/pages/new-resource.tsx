import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Loader2, CheckCircle2, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { TargetPill, FormField, inputCls, selectCls } from '@/components/deploy-form-fields'

type Step = 'form' | 'preview' | 'success'
type CloudProvider = 'aws' | 'azure'
type AWSResource = 'ecs' | 'vm' | 'rds'
type AzureResource = 'aci' | 'vm' | 'db'

function SectionDivider({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3 py-1">
      <div className="h-px flex-1 bg-border" />
      <span className="text-[0.65rem] font-semibold uppercase tracking-wider text-muted-foreground">{label}</span>
      <div className="h-px flex-1 bg-border" />
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
export function NewResourcePage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStep] = useState<Step>('form')

  const [provider, setProvider] = useState<CloudProvider>('aws')
  const [cloudAccountID, setCloudAccountID] = useState('')
  const [name, setName] = useState('')
  const [region, setRegion] = useState('')
  const [awsResource, setAWSResource] = useState<AWSResource>('ecs')
  const [azureResource, setAzureResource] = useState<AzureResource>('aci')
  const [resourceOpts, setResourceOpts] = useState<Record<string, string>>({})

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [previewConfig, setPreviewConfig] = useState('')
  const [success, setSuccess] = useState<{ name: string; provider: string; resourceType: string } | null>(null)

  const { data: cloudAccounts = [] } = useQuery({
    queryKey: ['cloud-accounts'],
    queryFn: () => apiFetch<{ id: string; provider: string; name: string }[]>('/cloud/accounts'),
  })
  const filteredAccounts = cloudAccounts.filter((a) => a.provider === provider)
  const awsRegions = ['us-east-1', 'us-west-2', 'eu-west-1', 'eu-west-2', 'ap-southeast-1']
  // NTU Azure for Students subscription policy (sys.regionrestriction) only allows these regions
  const azureLocations = ['norwayeast', 'germanywestcentral', 'polandcentral', 'switzerlandnorth', 'spaincentral']

  function setOpt(k: string, val: string) {
    setResourceOpts(prev => ({ ...prev, [k]: val }))
  }

  function clearOpts() { setResourceOpts({}) }

  async function handlePreview(e: React.FormEvent) {
    e.preventDefault(); setLoading(true); setError('')
    try {
      const resourceType = provider === 'aws' ? awsResource : azureResource
      const data = await apiFetch<{ config: string }>('/resources/preview', {
        method: 'POST',
        body: JSON.stringify({
          name, provider, region, resource_type: resourceType, resource_opts: resourceOpts,
        }),
      })
      setPreviewConfig(data.config)
      setStep('preview')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to generate preview')
    } finally { setLoading(false) }
  }

  async function handleConfirm() {
    setLoading(true); setError('')
    try {
      const resourceType = provider === 'aws' ? awsResource : azureResource
      await apiFetch('/resources', {
        method: 'POST',
        body: JSON.stringify({
          name, provider, cloud_account_id: cloudAccountID,
          region, resource_type: resourceType, resource_opts: resourceOpts,
        }),
      })
      setSuccess({ name, provider, resourceType })
      setStep('success')
      qc.invalidateQueries({ queryKey: ['resources'] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create resource')
    } finally { setLoading(false) }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      {/* Top bar */}
      <div className="flex h-14 items-center gap-3 border-b border-border px-6">
        <button
          onClick={() => {
            if (step === 'form') navigate(-1)
            else if (step === 'preview') setStep('form')
            else navigate('/resources')
          }}
          className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
          {step === 'form' ? 'Back' : step === 'preview' ? 'Edit' : 'View resources'}
        </button>
        <span className="text-sm font-semibold">New Resource</span>
      </div>

      <div className="flex flex-1 items-start justify-center p-8 pt-12">

        {/* ── Form ── */}
        {step === 'form' && (
          <div className="w-full max-w-md space-y-6">
            <div className="space-y-1">
              <h1 className="text-xl font-bold">New Resource</h1>
              <p className="text-sm text-muted-foreground">Provision standalone infrastructure via Terraform</p>
            </div>
            <form onSubmit={handlePreview} className="space-y-4">
              <FormField label="Provider">
                <div className="flex gap-2">
                  <TargetPill label="AWS" active={provider === 'aws'} onClick={() => { setProvider('aws'); setCloudAccountID(''); clearOpts() }} />
                  <TargetPill label="Azure" active={provider === 'azure'} onClick={() => { setProvider('azure'); setCloudAccountID(''); clearOpts() }} />
                </div>
              </FormField>

              <FormField label="Cloud account">
                <select value={cloudAccountID} onChange={e => setCloudAccountID(e.target.value)} required className={selectCls}>
                  <option value="">Select account…</option>
                  {filteredAccounts.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
                </select>
                {filteredAccounts.length === 0 && (
                  <p className="text-xs text-muted-foreground mt-1">
                    No {provider.toUpperCase()} accounts — register one in{' '}
                    <button type="button" onClick={() => navigate('/settings')} className="underline">Settings</button>.
                  </p>
                )}
              </FormField>

              <div className="grid grid-cols-2 gap-3">
                <FormField label="Resource name">
                  <input value={name} onChange={e => setName(e.target.value)} placeholder="my-resource" required className={inputCls} />
                </FormField>
                <FormField label={provider === 'aws' ? 'Region' : 'Location'} hint="type to enter a region not listed">
                  <input
                    value={region} onChange={e => setRegion(e.target.value)} required
                    list="region-suggestions" placeholder={provider === 'aws' ? 'us-east-1' : 'uksouth'}
                    className={inputCls}
                  />
                  <datalist id="region-suggestions">
                    {(provider === 'aws' ? awsRegions : azureLocations).map(r => <option key={r} value={r} />)}
                  </datalist>
                </FormField>
              </div>

              <FormField label="Resource type">
                <div className="flex gap-2">
                  {provider === 'aws' ? (
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
              {provider === 'aws' && awsResource === 'ecs' && <ECSFields v={resourceOpts} set={setOpt} />}
              {provider === 'aws' && awsResource === 'vm'  && <EC2Fields v={resourceOpts} set={setOpt} />}
              {provider === 'aws' && awsResource === 'rds' && <RDSFields v={resourceOpts} set={setOpt} />}
              {provider === 'azure' && azureResource === 'aci' && <ACIFields v={resourceOpts} set={setOpt} />}
              {provider === 'azure' && azureResource === 'vm'  && <AzureVMFields v={resourceOpts} set={setOpt} />}
              {provider === 'azure' && azureResource === 'db'  && <AzureDBFields v={resourceOpts} set={setOpt} />}

              {error && (
                <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
                  <AlertCircle className="size-4 shrink-0" />{error}
                </div>
              )}
              <Button type="submit" className="w-full h-10 gap-2 mt-2" disabled={loading}>
                {loading && <Loader2 className="size-4 animate-spin" />}
                {loading ? 'Generating preview…' : 'Preview Terraform config'}
              </Button>
            </form>
          </div>
        )}

        {/* ── Preview ── */}
        {step === 'preview' && (
          <div className="w-full max-w-2xl space-y-6">
            <div className="space-y-1">
              <h1 className="text-xl font-bold">Review Terraform configuration</h1>
              <p className="text-sm text-muted-foreground">
                This is the Terraform that will be planned for <span className="font-mono">{name}</span>. Confirm to create the resource and run <span className="font-mono">terraform plan</span>.
              </p>
            </div>
            <pre className="max-h-[28rem] overflow-auto rounded-lg border border-border bg-card p-4 text-xs font-mono leading-relaxed whitespace-pre">
              {previewConfig}
            </pre>
            {error && (
              <div className="flex items-center gap-2 rounded-lg border border-critical/25 bg-critical/8 px-3 py-2.5 text-sm text-critical">
                <AlertCircle className="size-4 shrink-0" />{error}
              </div>
            )}
            <div className="flex gap-3">
              <Button variant="outline" className="flex-1" onClick={() => setStep('form')} disabled={loading}>
                Back to edit
              </Button>
              <Button className="flex-1 gap-2" onClick={handleConfirm} disabled={loading}>
                {loading && <Loader2 className="size-4 animate-spin" />}
                {loading ? 'Creating…' : 'Confirm & create'}
              </Button>
            </div>
          </div>
        )}

        {/* ── Success ── */}
        {step === 'success' && success && (
          <div className="w-full max-w-md space-y-6">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="size-6 text-success shrink-0" />
              <div>
                <p className="font-semibold">Terraform plan initiated</p>
                <p className="text-sm text-muted-foreground font-mono">{success.name}</p>
              </div>
            </div>
            <div className="rounded-xl border border-border bg-card p-5 space-y-2">
              <p className="text-sm"><span className="text-muted-foreground">Provider:</span> <span className="font-medium uppercase">{success.provider}</span></p>
              <p className="text-sm"><span className="text-muted-foreground">Resource:</span> <span className="font-medium">{success.resourceType}</span></p>
              <p className="text-sm"><span className="text-muted-foreground">Name:</span> <span className="font-mono">{success.name}</span></p>
            </div>
            <p className="text-xs text-muted-foreground">
              The Terraform plan is running. Watch the Resources page for live status — apply it once the plan succeeds.
            </p>
            <div className="flex gap-3">
              <Button variant="outline" className="flex-1" onClick={() => { setStep('form'); setName(''); setRegion(''); setCloudAccountID(''); setPreviewConfig(''); clearOpts() }}>
                New resource
              </Button>
              <Button className="flex-1" onClick={() => navigate('/resources')}>View resources</Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
