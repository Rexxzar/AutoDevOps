import { useState } from 'react'
import { Loader2, Database, Copy, Check, Eye, EyeOff } from 'lucide-react'
import { Dialog } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { TargetPill } from '@/components/deploy-form-fields'
import { apiFetch } from '@/lib/api'

type DbType = 'postgres' | 'mysql' | 'redis' | 'mongo'
type TargetType = 'container' | 'k8s'

interface AppStub {
  id: string
  name: string
  kind: string
  target_type: string
  framework?: string
}

interface AddDatabaseDialogProps {
  stackId: string
  allApps: AppStub[]
  open: boolean
  onClose: () => void
  onAdded: () => void
}

function randomPassword(len = 18) {
  const chars = 'ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789'
  return Array.from(crypto.getRandomValues(new Uint8Array(len)))
    .map((b) => chars[b % chars.length])
    .join('')
}

interface DbConfig {
  label: string
  image: string
  port: number
  volumePath: string
  alias: string
  defaultDbName: string
  defaultUser: string
  envFn: (dbName: string, user: string, pass: string) => Record<string, string>
  connStrFn: (dbName: string, user: string, pass: string, host: string) => string
  hasAuth: boolean
}

const DB_CONFIGS: Record<DbType, DbConfig> = {
  postgres: {
    label: 'PostgreSQL',
    image: 'postgres:16-alpine',
    port: 5432,
    volumePath: '/var/lib/postgresql/data',
    alias: 'DB',
    defaultDbName: 'app',
    defaultUser: 'autodevops',
    hasAuth: true,
    envFn: (dbName, user, pass) => ({
      POSTGRES_DB: dbName,
      POSTGRES_USER: user,
      POSTGRES_PASSWORD: pass,
    }),
    connStrFn: (dbName, user, pass, host) =>
      `postgresql://${user}:${pass}@${host}:5432/${dbName}`,
  },
  mysql: {
    label: 'MySQL',
    image: 'mysql:8',
    port: 3306,
    volumePath: '/var/lib/mysql',
    alias: 'DB',
    defaultDbName: 'app',
    defaultUser: 'autodevops',
    hasAuth: true,
    envFn: (dbName, user, pass) => ({
      MYSQL_DATABASE: dbName,
      MYSQL_USER: user,
      MYSQL_PASSWORD: pass,
      MYSQL_ROOT_PASSWORD: pass + '_root',
    }),
    connStrFn: (dbName, user, pass, host) =>
      `mysql://${user}:${pass}@${host}:3306/${dbName}`,
  },
  redis: {
    label: 'Redis',
    image: 'redis:7-alpine',
    port: 6379,
    volumePath: '/data',
    alias: 'REDIS',
    defaultDbName: '',
    defaultUser: '',
    hasAuth: false,
    envFn: () => ({}),
    connStrFn: (_dbName, _user, _pass, host) => `redis://${host}:6379`,
  },
  mongo: {
    label: 'MongoDB',
    image: 'mongo:7',
    port: 27017,
    volumePath: '/data/db',
    alias: 'MONGO',
    defaultDbName: 'app',
    defaultUser: 'autodevops',
    hasAuth: true,
    envFn: (dbName, user, pass) => ({
      MONGO_INITDB_ROOT_USERNAME: user,
      MONGO_INITDB_ROOT_PASSWORD: pass,
      MONGO_INITDB_DATABASE: dbName,
    }),
    connStrFn: (dbName, user, pass, host) =>
      `mongodb://${user}:${pass}@${host}:27017/${dbName}`,
  },
}

function CopyBtn({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => { navigator.clipboard.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
      className="ml-1 shrink-0 rounded p-0.5 text-muted-foreground hover:text-foreground transition-colors"
    >
      {copied ? <Check className="size-3 text-green-500" /> : <Copy className="size-3" />}
    </button>
  )
}

interface DoneState {
  connStr: string
  credentials: { label: string; value: string; secret?: boolean }[]
  linkedFrameworks: string[]
}

function Field({ label, value, onChange, placeholder }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  return (
    <div>
      <label className="block text-xs font-medium text-muted-foreground mb-1">{label}</label>
      <input
        className="h-8 w-full rounded border bg-background px-3 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </div>
  )
}

export function AddDatabaseDialog({ stackId, allApps, open, onClose, onAdded }: AddDatabaseDialogProps) {
  const autoTarget: TargetType = allApps.some((a) => a.target_type === 'k8s') ? 'k8s' : 'container'

  const [dbType, setDbType] = useState<DbType>('postgres')
  const [target, setTarget] = useState<TargetType>(autoTarget)
  const [name, setName] = useState('postgres')
  const [dbName, setDbName] = useState(DB_CONFIGS.postgres.defaultDbName)
  const [dbUser, setDbUser] = useState(DB_CONFIGS.postgres.defaultUser)
  const [dbPass, setDbPass] = useState(() => randomPassword())
  const [linkedAppIds, setLinkedAppIds] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<DoneState | null>(null)

  const cfg = DB_CONFIGS[dbType]
  const linkableApps = allApps.filter((a) => a.kind === 'git' || (a.kind === 'image' && !['postgres', 'mysql', 'redis', 'mongo'].some((d) => a.name === d || a.name.startsWith(d + '-'))))

  function selectDbType(t: DbType) {
    const c = DB_CONFIGS[t]
    setDbType(t)
    setName(t)
    setDbName(c.defaultDbName)
    setDbUser(c.defaultUser)
    setDbPass(randomPassword())
  }

  function toggleApp(id: string) {
    setLinkedAppIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) { next.delete(id) } else { next.add(id) }
      return next
    })
  }

  function handleClose() {
    if (loading) return
    setDone(null)
    setError('')
    selectDbType('postgres')
    setTarget(autoTarget)
    setLinkedAppIds(new Set())
    onClose()
  }

  async function submit() {
    if (!name.trim()) { setError('Name is required'); return }
    if (cfg.hasAuth && (!dbUser.trim() || !dbPass.trim())) { setError('Username and password are required'); return }
    setLoading(true)
    setError('')
    try {
      const created = await apiFetch<{ id: string }>(`/deployments/${stackId}/apps`, {
        method: 'POST',
        body: JSON.stringify({
          name: name.trim(),
          kind: 'image',
          image_ref: cfg.image,
          port: cfg.port,
          volume_path: cfg.volumePath,
          env: cfg.hasAuth ? cfg.envFn(dbName, dbUser, dbPass) : {},
          target_type: target,
        }),
      })

      await Promise.allSettled(
        [...linkedAppIds].map((appId) =>
          apiFetch(`/deployments/${stackId}/apps/${appId}/links`, {
            method: 'POST',
            body: JSON.stringify({ alias: cfg.alias, depends_on_app_id: created.id }),
          })
        )
      )

      const creds: DoneState['credentials'] = cfg.hasAuth
        ? [
            { label: 'Host env var', value: `AUTODEVOPS_${cfg.alias}_HOST` },
            { label: 'Port env var', value: `AUTODEVOPS_${cfg.alias}_PORT` },
            { label: 'Database', value: dbName },
            { label: 'Username', value: dbUser },
            { label: 'Password', value: dbPass, secret: true },
          ]
        : [
            { label: 'Host env var', value: `AUTODEVOPS_${cfg.alias}_HOST` },
            { label: 'Port env var', value: `AUTODEVOPS_${cfg.alias}_PORT` },
          ]

      const linkedFrameworks = allApps
        .filter((a) => linkedAppIds.has(a.id) && a.framework)
        .map((a) => a.framework!)
      setDone({ connStr: cfg.connStrFn(dbName, dbUser, dbPass, name.trim()), credentials: creds, linkedFrameworks })
      onAdded()
    } catch (e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onClose={handleClose} title="Add database">
      <div className="space-y-4 p-4">
        {done ? (
          <SuccessScreen done={done} alias={cfg.alias} dbType={dbType} onClose={handleClose} />
        ) : (
          <>
            {/* DB type */}
            <div>
              <p className="mb-2 text-xs font-medium text-muted-foreground">Database type</p>
              <div className="flex flex-wrap gap-2">
                {(Object.keys(DB_CONFIGS) as DbType[]).map((t) => (
                  <TargetPill key={t} label={DB_CONFIGS[t].label} sub={DB_CONFIGS[t].image} active={dbType === t} onClick={() => selectDbType(t)} />
                ))}
              </div>
            </div>

            {/* Target */}
            <div>
              <p className="mb-2 text-xs font-medium text-muted-foreground">Run as</p>
              <div className="flex gap-2">
                <TargetPill label="Docker container" sub="local_docker" active={target === 'container'} onClick={() => setTarget('container')} />
                <TargetPill label="Kubernetes pod" sub="local_k8s" active={target === 'k8s'} onClick={() => setTarget('k8s')} />
              </div>
            </div>

            {/* App name */}
            <Field label="App name" value={name} onChange={setName} />

            {/* Credentials — only for auth-enabled DBs */}
            {cfg.hasAuth && (
              <div className="space-y-3">
                <p className="text-xs font-medium text-muted-foreground">Credentials</p>
                <Field label="Database name" value={dbName} onChange={setDbName} placeholder="app" />
                <Field label="Username" value={dbUser} onChange={setDbUser} placeholder="autodevops" />
                <div>
                  <label className="block text-xs font-medium text-muted-foreground mb-1">Password</label>
                  <div className="flex gap-2">
                    <input
                      className="h-8 flex-1 rounded border bg-background px-3 font-mono text-sm focus:outline-none focus:ring-1 focus:ring-ring"
                      value={dbPass}
                      onChange={(e) => setDbPass(e.target.value)}
                    />
                    <Button size="sm" variant="outline" type="button" onClick={() => setDbPass(randomPassword())}>
                      Generate
                    </Button>
                  </div>
                </div>
              </div>
            )}

            {/* Link apps */}
            {linkableApps.length > 0 && (
              <div>
                <p className="mb-2 text-xs font-medium text-muted-foreground">Auto-link (injects HOST + PORT env vars)</p>
                <div className="space-y-1.5">
                  {linkableApps.map((a) => (
                    <label key={a.id} className="flex items-center gap-2 cursor-pointer text-sm">
                      <input type="checkbox" checked={linkedAppIds.has(a.id)} onChange={() => toggleApp(a.id)} className="rounded" />
                      {a.name}
                      <span className="text-xs text-muted-foreground">{a.kind}</span>
                    </label>
                  ))}
                </div>
              </div>
            )}

            {error && <p className="text-xs text-red-500">{error}</p>}

            <div className="flex justify-end gap-2 pt-1">
              <Button size="sm" variant="outline" onClick={handleClose} disabled={loading}>Cancel</Button>
              <Button size="sm" className="gap-1.5" onClick={submit} disabled={loading}>
                {loading ? <Loader2 className="size-3.5 animate-spin" /> : <Database className="size-3.5" />}
                Add database
              </Button>
            </div>
          </>
        )}
      </div>
    </Dialog>
  )
}

function nextSteps(dbType: DbType, connStr: string, frameworks: string[]): { file: string; content: string; note: string }[] {
  const steps: { file: string; content: string; note: string }[] = []
  const hasNodeOrPrisma = frameworks.includes('nodejs') || frameworks.length === 0
  const hasPHP = frameworks.includes('php')
  const hasReact = frameworks.includes('react')

  if (dbType === 'postgres' || dbType === 'mysql' || dbType === 'mongo') {
    if (hasNodeOrPrisma && !hasReact) {
      steps.push({
        file: '.env',
        content: dbType === 'postgres'
          ? `DATABASE_URL=${connStr}`
          : dbType === 'mysql'
            ? `DATABASE_URL=${connStr}`
            : `MONGODB_URI=${connStr}`,
        note: 'Create this file at the repo root. Prisma, Mongoose, Sequelize and most ORMs read DATABASE_URL automatically.',
      })
      steps.push({
        file: 'Procfile',
        content: dbType === 'postgres' || dbType === 'mysql'
          ? 'web: npx prisma migrate deploy && node dist/main.js'
          : 'web: node dist/main.js',
        note: 'Run migrations before starting the app. Update the node path to match your compiled output (e.g. dist/api/main.js).',
      })
    }
    if (hasPHP) {
      steps.push({
        file: '.env',
        content: dbType === 'postgres'
          ? `DB_CONNECTION=pgsql\nDB_HOST=$AUTODEVOPS_DB_HOST\nDB_PORT=5432\nDB_DATABASE=app\nDB_USERNAME=autodevops\nDB_PASSWORD=<your-password>`
          : `DB_CONNECTION=mysql\nDB_HOST=$AUTODEVOPS_DB_HOST\nDB_PORT=3306\nDB_DATABASE=app\nDB_USERNAME=autodevops\nDB_PASSWORD=<your-password>`,
        note: 'Laravel reads these env vars automatically. Replace <your-password> with the password shown above.',
      })
    }
  }

  if (dbType === 'redis' && (hasNodeOrPrisma || frameworks.length === 0)) {
    steps.push({
      file: '.env',
      content: `REDIS_URL=redis://$AUTODEVOPS_REDIS_HOST:6379`,
      note: 'Or use AUTODEVOPS_REDIS_HOST and AUTODEVOPS_REDIS_PORT separately in your app config.',
    })
  }

  return steps
}

function SuccessScreen({ done, alias, dbType, onClose }: { done: DoneState; alias: string; dbType: DbType; onClose: () => void }) {
  const [revealed, setRevealed] = useState<Set<string>>(new Set())
  const steps = nextSteps(dbType, done.connStr, done.linkedFrameworks)

  function toggle(label: string) {
    setRevealed((prev) => { const n = new Set(prev); n.has(label) ? n.delete(label) : n.add(label); return n })
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Database created. Linked apps have{' '}
        <code className="rounded bg-muted px-1 text-xs">AUTODEVOPS_{alias}_HOST</code> and{' '}
        <code className="rounded bg-muted px-1 text-xs">AUTODEVOPS_{alias}_PORT</code> injected automatically.
      </p>

      {/* Credentials */}
      <div>
        <p className="mb-1.5 text-xs font-medium text-muted-foreground">Credentials — save the password now, it won't be shown again</p>
        <div className="rounded-lg border divide-y text-xs">
          {done.credentials.map((c) => {
            const isHidden = c.secret && !revealed.has(c.label)
            return (
              <div key={c.label} className="flex items-center justify-between px-3 py-2 gap-3">
                <span className="text-muted-foreground shrink-0 w-28">{c.label}</span>
                <div className="flex items-center min-w-0 flex-1">
                  <code className={`truncate font-mono ${isHidden ? 'blur-sm select-none' : ''}`}>
                    {isHidden ? '••••••••••••••••••' : c.value}
                  </code>
                  {c.secret && (
                    <button onClick={() => toggle(c.label)} className="ml-1 shrink-0 rounded p-0.5 text-muted-foreground hover:text-foreground">
                      {revealed.has(c.label) ? <EyeOff className="size-3" /> : <Eye className="size-3" />}
                    </button>
                  )}
                  {!isHidden && <CopyBtn value={c.value} />}
                </div>
              </div>
            )
          })}
          <div className="flex items-center justify-between px-3 py-2 gap-3">
            <span className="text-muted-foreground shrink-0 w-28">Connection URL</span>
            <div className="flex items-center min-w-0 flex-1">
              <code className="truncate font-mono text-[0.65rem]">{done.connStr}</code>
              <CopyBtn value={done.connStr} />
            </div>
          </div>
        </div>
      </div>

      {/* Next steps */}
      {steps.length > 0 && (
        <div>
          <p className="mb-2 text-xs font-medium text-muted-foreground">What to do next — edit these files in your repo</p>
          <div className="space-y-2">
            {steps.map((s) => (
              <div key={s.file} className="rounded-lg border bg-muted/30 text-xs">
                <div className="flex items-center justify-between border-b px-3 py-1.5">
                  <code className="font-mono font-medium">{s.file}</code>
                  <CopyBtn value={s.content} />
                </div>
                <pre className="px-3 py-2 font-mono text-[0.65rem] overflow-x-auto">{s.content}</pre>
                <p className="border-t px-3 py-1.5 text-muted-foreground">{s.note}</p>
              </div>
            ))}
          </div>
          <p className="mt-2 text-xs text-muted-foreground">
            Use <span className="font-medium text-foreground">Edit files</span> on the backend app card to add these without leaving the dashboard, then Commit &amp; push to trigger a redeploy.
          </p>
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={onClose}>Done</Button>
      </div>
    </div>
  )
}
