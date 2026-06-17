export const DECLARED_FRAMEWORK_LABELS: Record<string, string> = {
  react: 'React', vite: 'Vite', nextjs: 'Next.js', nodejs: 'Node.js',
  nestjs: 'NestJS', laravel: 'Laravel', django: 'Django', rails: 'Rails', php: 'PHP',
}

export const DECLARED_FRAMEWORK_HINTS: Record<string, string> = {
  nestjs: '→ DATABASE_URL', django: '→ DATABASE_URL', rails: '→ DATABASE_URL',
  laravel: '→ DB_HOST/DB_USERNAME…', php: '→ DB_HOST/DB_USERNAME…',
}

export const DECLARED_FRAMEWORK_OPTIONS: { value: string; label: string; sub?: string }[] = [
  { value: '', label: 'Auto-detect', sub: 'Paketo decides' },
  { value: 'react', label: 'React', sub: 'CRA / Vite' },
  { value: 'vite', label: 'Vite', sub: 'non-React Vite' },
  { value: 'nextjs', label: 'Next.js' },
  { value: 'nodejs', label: 'Node.js', sub: 'generic' },
  { value: 'nestjs', label: 'NestJS', sub: '→ DATABASE_URL' },
  { value: 'laravel', label: 'Laravel', sub: '→ DB_HOST…' },
  { value: 'django', label: 'Django', sub: '→ DATABASE_URL' },
  { value: 'rails', label: 'Rails', sub: '→ DATABASE_URL' },
  { value: 'php', label: 'PHP', sub: 'generic' },
]
