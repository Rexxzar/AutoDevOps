// Diagnosis engine — analyses app state + deploy logs and returns actionable hints.
// Pure function: no side effects, no API calls.

export type DiagAction = 'add_db' | 'edit_files' | 'add_link' | 'redeploy'

export interface DiagHint {
  id: string
  level: 'error' | 'warning' | 'info'
  message: string
  action?: DiagAction
  actionLabel?: string
}

interface AppContext {
  id: string
  kind: string
  framework?: string
  status: string
  links: { depends_on_app_id?: string; depends_on_app_name?: string }[]
  image_ref?: string
}

// ── Log pattern rules ─────────────────────────────────────────────────────────

const LOG_RULES: Array<{ pattern: RegExp; hint: Omit<DiagHint, 'id'> }> = [
  // Database connection refused
  {
    pattern: /ECONNREFUSED|connection refused.*(?:5432|3306|27017|6379)|connect.*ECONNREFUSED/i,
    hint: {
      level: 'error',
      message:
        'App can\'t connect to a database — connection refused. Add a database to this deployment and link it so the host and port are injected automatically.',
      action: 'add_db',
      actionLabel: 'Add database',
    },
  },
  // DB env var is undefined at runtime
  {
    pattern:
      /(?:DATABASE_URL|POSTGRES_(?:HOST|URL|PASSWORD)|MYSQL_HOST|MONGO_URI|DB_HOST|DB_URL)[^\n]*(?:undefined|null|is not set|must be)/i,
    hint: {
      level: 'error',
      message:
        'A database connection variable is undefined. Add a database and link it — AutoDevOps will inject the host, port, and credentials as env vars automatically.',
      action: 'add_db',
      actionLabel: 'Add database',
    },
  },
  // Prisma / Sequelize migration or auth failure
  {
    pattern: /prisma.*(?:database|migration)|sequelize.*(?:auth|connect)|P1001|P1003/i,
    hint: {
      level: 'error',
      message:
        'Database connection or migration failed. Make sure a database is linked to this app and the schema has been migrated.',
      action: 'add_db',
      actionLabel: 'Add database',
    },
  },
  // Entry point not found (built output missing)
  {
    pattern: /cannot find module['"` ]+(?:\.\/|\/workspace\/)(?:dist|build|out)\/[^\s'"`)]+\.js/i,
    hint: {
      level: 'error',
      message:
        'Built output not found. The app built successfully but the start command points to the wrong file. Add a Procfile to the repo root with the correct path, e.g. web: node dist/main.js.',
      action: 'edit_files',
      actionLabel: 'Edit files (add Procfile)',
    },
  },
  // Missing npm start script
  {
    pattern: /npm err.*(?:missing script.*start|no start script)/i,
    hint: {
      level: 'error',
      message:
        'No npm start script found. Add a Procfile to the repo root so the buildpack knows how to start your app, e.g. web: node dist/index.js.',
      action: 'edit_files',
      actionLabel: 'Edit files (add Procfile)',
    },
  },
  // Monorepo / NX CLI missing at runtime
  {
    pattern: /(?:nx|turbo|lerna|pnpm):\s*(?:not found|command not found)|exec.*(?:nx|turbo).*not found/i,
    hint: {
      level: 'error',
      message:
        'Monorepo CLI tool not available at runtime — it\'s a devDependency that gets pruned after build. Add a Procfile that runs the compiled output directly, e.g. web: node dist/apps/api/main.js.',
      action: 'edit_files',
      actionLabel: 'Edit files (add Procfile)',
    },
  },
  // CORS error visible in logs
  {
    pattern: /cors|access-control-allow-origin/i,
    hint: {
      level: 'warning',
      message:
        'CORS errors detected in logs. Your backend is not allowing requests from the frontend domain. Edit the backend config to add your frontend\'s domain (or *) to allowed origins, then redeploy.',
      action: 'edit_files',
      actionLabel: 'Edit backend config',
    },
  },
  // Port already in use
  {
    pattern: /EADDRINUSE|address already in use/i,
    hint: {
      level: 'error',
      message:
        'Port already in use. This usually resolves itself — redeploy to get a fresh container.',
      action: 'redeploy',
      actionLabel: 'Redeploy',
    },
  },
  // General module not found (lower priority — kept last among module rules)
  {
    pattern: /(?:cannot find module|module not found): ['"](?!.*(?:dist|build|out))[^'"]+['"]/i,
    hint: {
      level: 'error',
      message:
        'A required npm module is missing. Make sure it\'s listed in package.json as a production dependency (not devDependencies) and commit the updated package-lock.json.',
      action: 'edit_files',
      actionLabel: 'Edit package.json',
    },
  },
  // Out of memory
  {
    pattern: /(?:killed|oomkilled|out of memory|javascript heap out of memory)/i,
    hint: {
      level: 'error',
      message:
        'App was killed — likely out of memory. Check for memory leaks or reduce the memory footprint. Node.js apps may need --max-old-space-size in the Procfile start command.',
      action: 'edit_files',
      actionLabel: 'Edit Procfile',
    },
  },
  // TypeScript / compile error at runtime
  {
    pattern: /ts-node|SyntaxError.*unexpected token.*import|SyntaxError.*cannot use import/i,
    hint: {
      level: 'error',
      message:
        'TypeScript or ESM syntax error at runtime. Your app is running uncompiled TypeScript. Add a build step (tsc) and a Procfile pointing to the compiled JS output.',
      action: 'edit_files',
      actionLabel: 'Edit Procfile',
    },
  },
  // App listening on wrong interface (127.0.0.1 instead of 0.0.0.0)
  {
    pattern: /listening on.*127\.0\.0\.1|server started.*localhost(?!.*0\.0\.0\.0)/i,
    hint: {
      level: 'warning',
      message:
        'App is listening on localhost (127.0.0.1) instead of 0.0.0.0. It won\'t be reachable from outside the container. Update your app to bind to 0.0.0.0 or use HOST=0.0.0.0.',
      action: 'edit_files',
      actionLabel: 'Edit app config',
    },
  },
]

// ── Context-based rules (no logs needed) ─────────────────────────────────────

export function diagnoseApp(
  app: AppContext,
  logs: string[] | undefined,
  allApps: AppContext[],
): DiagHint[] {
  const hints: DiagHint[] = []
  const logText = (logs ?? []).join('\n')
  const hasDbApp = allApps.some(
    (a) => a.id !== app.id && a.image_ref && /postgres|mysql|mongo|redis/i.test(a.image_ref),
  )

  // ── Log-based rules ──────────────────────────────────────────────────────
  if (app.status === 'failed' && logText) {
    const seen = new Set<string>()
    for (const rule of LOG_RULES) {
      if (rule.pattern.test(logText)) {
        // De-duplicate: if we already suggested 'add_db', don't add another DB hint
        const key = rule.hint.action ?? rule.hint.message.slice(0, 40)
        if (!seen.has(key)) {
          seen.add(key)
          hints.push({ id: `log_${seen.size}`, ...rule.hint })
        }
      }
    }
  }

  // ── Context-based rules ──────────────────────────────────────────────────

  // React / Vite frontend with no links to a backend and another backend app exists
  if (
    app.kind === 'git' &&
    (app.framework === 'react' || app.framework === 'static') &&
    app.links.length === 0 &&
    allApps.some((a) => a.id !== app.id && a.kind === 'git' && a.framework === 'nodejs')
  ) {
    hints.push({
      id: 'frontend_no_link',
      level: 'info',
      message:
        'This frontend has no backend connection. Link it to your backend app — AutoDevOps will inject the backend URL as an env var (AUTODEVOPS_<ALIAS>_HOST).',
      action: 'add_link',
      actionLabel: 'Add link',
    })
  }

  // Linked app is in a failed state
  for (const link of app.links) {
    if (!link.depends_on_app_id) continue
    const dep = allApps.find((a) => a.id === link.depends_on_app_id)
    if (dep && dep.status === 'failed') {
      hints.push({
        id: `linked_failed_${dep.id}`,
        level: 'warning',
        message: `Linked app "${dep.id === link.depends_on_app_id ? (link.depends_on_app_name ?? 'backend') : dep.id}" is in a failed state — API calls from this app will fail until it\'s fixed.`,
      })
    }
  }

  // Logs mention DB errors but no DB app exists in this deployment
  if (
    app.status === 'failed' &&
    !hasDbApp &&
    /ECONNREFUSED|DATABASE_URL|POSTGRES_|MYSQL_|MONGO_URI|DB_HOST/i.test(logText)
  ) {
    const alreadySuggestedDb = hints.some((h) => h.action === 'add_db')
    if (!alreadySuggestedDb) {
      hints.push({
        id: 'no_db_in_deployment',
        level: 'error',
        message:
          'Database errors detected but no database app exists in this deployment. Add a PostgreSQL, MySQL, Redis, or MongoDB container and link it.',
        action: 'add_db',
        actionLabel: 'Add database',
      })
    }
  }

  // React app with no VITE_/REACT_APP_ env hint — check if it links to backend
  // but logs show it calling a hardcoded URL or localhost
  if (
    app.kind === 'git' &&
    app.framework === 'react' &&
    /localhost|127\.0\.0\.1|api\.example\.com|api\.realworld\.io/i.test(logText)
  ) {
    hints.push({
      id: 'react_hardcoded_url',
      level: 'warning',
      message:
        'Hardcoded or localhost API URL detected in logs. React apps bake the API URL at build time — edit your API config file to use a relative path or your backend\'s real domain, then redeploy.',
      action: 'edit_files',
      actionLabel: 'Edit API config',
    })
  }

  return hints
}
