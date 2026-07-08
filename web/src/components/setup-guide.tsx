import { useState } from 'react'
import { CheckCircle2, Circle, X, ChevronDown, ChevronRight, Link2, Database, RefreshCw } from 'lucide-react'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'

// ── Types ─────────────────────────────────────────────────────────────────────

interface AppInfo {
  id: string
  name: string
  kind: string
  framework?: string
  declared_framework?: string
  status: string
  image_ref?: string
  links: { depends_on_app_id?: string; depends_on_resource_id?: string; depends_on_resource_name?: string }[]
}

interface SetupStep {
  id: string
  title: string
  description: string
  codeSnippet?: string
  completed: boolean
  inProgress?: boolean
  actionLabel?: string
  actionKind?: 'link' | 'add_db' | 'redeploy'
  targetAppId?: string
}

interface SetupGuideProps {
  apps: AppInfo[]
  stackId: string
  onDismiss: () => void
  onScrollToApp: (appId: string) => void
  onOpenAddDb: () => void
  onRedeploy: (appId: string) => void
}

// ── Framework classification ──────────────────────────────────────────────────

const FRONTEND_FRAMEWORKS = new Set(['react', 'vite', 'nextjs'])
const BACKEND_FRAMEWORKS = new Set(['php', 'laravel', 'django', 'rails', 'nestjs', 'go', 'java', 'ruby'])
const DB_FRAMEWORKS = new Set(['nestjs', 'laravel', 'php', 'django', 'rails', 'nodejs'])

function classifyApps(apps: AppInfo[]) {
  const frontends: AppInfo[] = []
  const backends: AppInfo[] = []
  const databases: AppInfo[] = []

  const hasExplicitBackend = apps.some(a => {
    const fw = a.declared_framework || a.framework || ''
    return a.kind === 'git' && BACKEND_FRAMEWORKS.has(fw)
  })

  for (const app of apps) {
    if (app.kind === 'image' && app.image_ref && /postgres|mysql|mongo|redis/i.test(app.image_ref)) {
      databases.push(app)
      continue
    }
    if (app.kind !== 'git') continue

    const fw = app.declared_framework || ''
    const detected = app.framework || ''

    if (FRONTEND_FRAMEWORKS.has(fw)) {
      frontends.push(app)
    } else if (BACKEND_FRAMEWORKS.has(fw)) {
      backends.push(app)
    } else if (detected === 'static' || detected === 'react') {
      frontends.push(app)
    } else if (BACKEND_FRAMEWORKS.has(detected)) {
      backends.push(app)
    } else if (detected === 'node' && hasExplicitBackend) {
      frontends.push(app)
    } else {
      backends.push(app)
    }
  }
  return { frontends, backends, databases }
}

function backendNeedsDb(app: AppInfo): boolean {
  const fw = app.declared_framework || app.framework || ''
  return DB_FRAMEWORKS.has(fw)
}

// ── Code snippets ─────────────────────────────────────────────────────────────

function frontendSnippet(fw: string): string {
  if (fw === 'nextjs') {
    return `// next.config.js
module.exports = {
  env: { API_URL: process.env.AUTODEVOPS_BACKEND_URL }
}`
  }
  return `# start.sh, generate config before serving
#!/bin/sh
URL=$(printenv AUTODEVOPS_BACKEND_URL)
echo "window.API_URL = '\${URL:-}';" > dist/config.js
exec npx serve dist -s -l "tcp://0.0.0.0:\${PORT:-8080}"`
}

function backendDbSnippet(fw: string): string {
  switch (fw) {
    case 'laravel':
    case 'php':
      return `// Auto-injected by AutoDevOps after linking:
env('DB_HOST')       // database host
env('DB_USERNAME')   // database user
env('DB_PASSWORD')   // database password
env('DB_DATABASE')   // database name
env('DB_CONNECTION') // pgsql or mysql`
    case 'django':
      return `# Auto-injected by AutoDevOps after linking:
import os
os.environ['DATABASE_URL']
# e.g. postgresql://user:pass@host:5432/db`
    case 'rails':
      return `# Auto-injected by AutoDevOps after linking:
ENV['DATABASE_URL']
# e.g. postgresql://user:pass@host:5432/db`
    case 'go':
      return `// Read manually from env:
os.Getenv("AUTODEVOPS_DB_HOST")
os.Getenv("AUTODEVOPS_DB_PORT")
os.Getenv("AUTODEVOPS_DB_USER")`
    default:
      return `// Auto-injected by AutoDevOps after linking:
process.env.DATABASE_URL
// e.g. postgresql://user:pass@host:5432/db`
  }
}

// ── Step computation ──────────────────────────────────────────────────────────

function computeSteps(apps: AppInfo[]): SetupStep[] {
  const { frontends, backends, databases } = classifyApps(apps)
  const steps: SetupStep[] = []

  const fe = frontends[0]
  const be = backends[0]
  const db = databases[0]

  const allRunning = apps.every(a => a.status === 'running')
  const anyBuilding = apps.some(a => a.status === 'building' || a.status === 'deploying')

  // Step: Deploy apps
  if (!allRunning && !anyBuilding) {
    steps.push({
      id: 'deploy_apps',
      title: 'Deploy your apps',
      description: 'All apps need to be running before you can wire them together.',
      completed: allRunning,
    })
  } else if (anyBuilding) {
    steps.push({
      id: 'deploy_apps',
      title: 'Deploying apps...',
      description: 'Waiting for builds to complete.',
      completed: false,
      inProgress: true,
    })
  }

  // Step: Connect frontend → backend
  if (fe && be) {
    const feLinkedToBe = fe.links.some(l =>
      l.depends_on_app_id && backends.some(b => b.id === l.depends_on_app_id)
    )
    steps.push({
      id: 'connect_fe_be',
      title: `Connect ${fe.name} → ${be.name}`,
      description: feLinkedToBe
        ? `Frontend is connected to the backend. The env var AUTODEVOPS_<ALIAS>_URL is injected into ${fe.name}.`
        : `Link your frontend to the backend so it receives the backend URL as an environment variable.`,
      completed: feLinkedToBe,
      actionLabel: feLinkedToBe ? undefined : 'Connect',
      actionKind: 'link',
      targetAppId: fe.id,
    })
  }

  // Step: Add a database
  if (be && backendNeedsDb(be)) {
    const hasDb = db !== undefined
    const beHasDbResourceLink = be.links.some(l => l.depends_on_resource_id)
    const dbAvailable = hasDb || beHasDbResourceLink

    if (!dbAvailable) {
      steps.push({
        id: 'add_db',
        title: 'Add a database',
        description: `${be.name} (${be.declared_framework || be.framework || 'backend'}) typically needs a database. Add a PostgreSQL, MySQL, or MongoDB container.`,
        completed: false,
        actionLabel: 'Add database',
        actionKind: 'add_db',
      })
    }
  }

  // Step: Connect backend → database
  if (be && db) {
    const beLinkedToDb = be.links.some(l =>
      l.depends_on_app_id === db.id
    )
    const beFw = be.declared_framework || be.framework || ''
    steps.push({
      id: 'connect_be_db',
      title: `Connect ${be.name} → ${db.name}`,
      description: beLinkedToDb
        ? `Backend is connected to the database. Credentials are injected automatically.`
        : `Link the backend to the database so connection credentials are injected as env vars.`,
      codeSnippet: beLinkedToDb ? undefined : backendDbSnippet(beFw),
      completed: beLinkedToDb,
      actionLabel: beLinkedToDb ? undefined : 'Connect',
      actionKind: 'link',
      targetAppId: be.id,
    })
  }

  // Step: Configure frontend env vars (informational)
  if (fe && be) {
    const feLinkedToBe = fe.links.some(l =>
      l.depends_on_app_id && backends.some(b => b.id === l.depends_on_app_id)
    )
    if (feLinkedToBe) {
      const feFw = fe.declared_framework || fe.framework || 'react'
      steps.push({
        id: 'fe_env_config',
        title: 'Read the backend URL in your frontend',
        description: `Your frontend needs to read the AUTODEVOPS_<ALIAS>_URL env var to connect to the backend. Here's how for ${feFw}:`,
        codeSnippet: frontendSnippet(feFw),
        completed: fe.status === 'running',
      })
    }
  }

  // Step: Redeploy in progress
  const linkedApps = apps.filter(a => a.links.length > 0)
  const anyLinkedBuilding = linkedApps.some(a => a.status === 'building' || a.status === 'deploying')
  if (anyLinkedBuilding) {
    const buildingApp = linkedApps.find(a => a.status === 'building' || a.status === 'deploying')
    steps.push({
      id: 'redeploy_pending',
      title: `Redeploying ${buildingApp?.name || 'app'}...`,
      description: 'Applying connection changes. This usually takes 1-2 minutes.',
      completed: false,
      inProgress: true,
    })
  }

  return steps
}

// ── Component ─────────────────────────────────────────────────────────────────

export function SetupGuide({ apps, stackId, onDismiss, onScrollToApp, onOpenAddDb, onRedeploy }: SetupGuideProps) {
  const steps = computeSteps(apps)
  const [expandedId, setExpandedId] = useState<string | null>(null)

  if (steps.length === 0) return null

  const completedCount = steps.filter(s => s.completed).length
  const allDone = completedCount === steps.length

  if (allDone) {
    return (
      <Card className="border-l-4 border-l-green-500">
        <CardHeader>
          <div className="flex items-center gap-2">
            <CheckCircle2 className="size-4 text-green-500" />
            <CardTitle>Setup complete</CardTitle>
          </div>
          <Button variant="ghost" size="icon-sm" onClick={onDismiss}><X className="size-3.5" /></Button>
        </CardHeader>
        <CardContent className="py-3 text-sm text-muted-foreground">
          All apps are wired and running. You can dismiss this guide.
        </CardContent>
      </Card>
    )
  }

  const firstIncomplete = steps.find(s => !s.completed)
  const activeId = expandedId ?? firstIncomplete?.id ?? null

  function handleAction(step: SetupStep) {
    if (step.actionKind === 'link' && step.targetAppId) {
      onScrollToApp(step.targetAppId)
    } else if (step.actionKind === 'add_db') {
      onOpenAddDb()
    } else if (step.actionKind === 'redeploy' && step.targetAppId) {
      onRedeploy(step.targetAppId)
    }
  }

  return (
    <Card className="border-l-4 border-l-primary">
      <CardHeader>
        <div className="flex items-center gap-2">
          <CardTitle>Setup Guide</CardTitle>
          <span className="text-xs text-muted-foreground">{completedCount} of {steps.length} complete</span>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onDismiss}><X className="size-3.5" /></Button>
      </CardHeader>
      <CardContent className="py-3">
        <div className="space-y-1">
          {steps.map((step, i) => {
            const isActive = activeId === step.id
            return (
              <div key={step.id}>
                <button
                  className="flex w-full items-start gap-3 rounded-md px-2 py-2 text-left text-sm hover:bg-accent/50 transition-colors"
                  onClick={() => setExpandedId(isActive ? null : step.id)}
                >
                  <div className="mt-0.5 shrink-0">
                    {step.completed ? (
                      <CheckCircle2 className="size-4 text-green-500" />
                    ) : step.inProgress ? (
                      <RefreshCw className="size-4 text-primary animate-spin" />
                    ) : (
                      <div className="flex size-4 items-center justify-center rounded-full border border-muted-foreground/40 text-[0.6rem] font-medium text-muted-foreground">
                        {i + 1}
                      </div>
                    )}
                  </div>
                  <span className={step.completed ? 'text-muted-foreground line-through' : ''}>
                    {step.title}
                  </span>
                  <div className="ml-auto shrink-0">
                    {isActive ? <ChevronDown className="size-3.5 text-muted-foreground" /> : <ChevronRight className="size-3.5 text-muted-foreground" />}
                  </div>
                </button>

                {isActive && (
                  <div className="ml-9 mb-2 space-y-2">
                    <p className="text-xs text-muted-foreground">{step.description}</p>
                    {step.codeSnippet && (
                      <pre className="rounded-lg border border-border bg-background px-3 py-2 font-mono text-[0.65rem] text-muted-foreground overflow-x-auto">
                        {step.codeSnippet}
                      </pre>
                    )}
                    {step.actionLabel && !step.completed && (
                      <Button variant="outline" size="sm" onClick={() => handleAction(step)}>
                        {step.actionKind === 'link' && <Link2 className="size-3" />}
                        {step.actionKind === 'add_db' && <Database className="size-3" />}
                        {step.actionKind === 'redeploy' && <RefreshCw className="size-3" />}
                        {step.actionLabel}
                      </Button>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </CardContent>
    </Card>
  )
}
