import { NavLink } from 'react-router-dom'
import {
  LayoutDashboard,
  Rocket,
  Boxes,
  Cloud,
  Server,
  ShieldCheck,
  KeyRound,
  Settings,
  Activity,
  Terminal,
  Package,
  GitBranch,
  FileCode2,
  ScrollText,
} from 'lucide-react'
import { cn } from '@/lib/utils'

const nav = [
  { to: '/', label: 'Overview', icon: LayoutDashboard, end: true },
  { to: '/deployments', label: 'Deployments', icon: Rocket, badge: 'live' },
  { to: '/repos', label: 'Repositories', icon: GitBranch },
  { to: '/clusters', label: 'Clusters', icon: Boxes, badge: 'live' },
  { to: '/resources', label: 'Cloud Resources', icon: Cloud },
  { to: '/terraform', label: 'Terraform', icon: FileCode2 },
  { to: '/local-resources', label: 'Local Resources', icon: Server },
  { to: '/image-registry', label: 'Image Registry', icon: Package },
  { to: '/compliance', label: 'Compliance', icon: ShieldCheck },
  { to: '/logs', label: 'Logs', icon: ScrollText },
  { to: '/secrets', label: 'Secrets', icon: KeyRound },
]

const secondary = [{ to: '/settings', label: 'Settings', icon: Settings }]

export function Sidebar() {
  return (
    <aside className="hidden w-60 shrink-0 flex-col border-r border-sidebar-border bg-sidebar lg:flex">
      <div className="flex h-14 items-center gap-2.5 border-b border-sidebar-border px-5">
        <div className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <Terminal className="size-4" strokeWidth={2.5} />
        </div>
        <div className="flex flex-col leading-none">
          <span className="text-sm font-semibold tracking-tight">AutoDevOps</span>
          <span className="text-[0.65rem] text-muted-foreground">control plane</span>
        </div>
      </div>

      <nav className="flex flex-1 flex-col gap-0.5 overflow-y-auto p-3">
        <p className="px-3 pb-1.5 pt-2 text-[0.65rem] font-medium uppercase tracking-wider text-muted-foreground">
          Platform
        </p>
        {nav.map((item) => (
          <NavItem key={item.to} {...item} />
        ))}

        <p className="px-3 pb-1.5 pt-5 text-[0.65rem] font-medium uppercase tracking-wider text-muted-foreground">
          Workspace
        </p>
        {secondary.map((item) => (
          <NavItem key={item.to} {...item} />
        ))}
      </nav>

      <div className="border-t border-sidebar-border p-3">
        <div className="flex items-center gap-2 rounded-lg bg-sidebar-accent px-3 py-2.5">
          <Activity className="size-4 text-success" />
          <div className="flex flex-col leading-tight">
            <span className="text-xs font-medium">Control plane</span>
            <span className="text-[0.65rem] text-muted-foreground">
              SSE connected · live
            </span>
          </div>
        </div>
      </div>
    </aside>
  )
}

function NavItem({
  to,
  label,
  icon: Icon,
  end,
  badge,
}: {
  to: string
  label: string
  icon: typeof LayoutDashboard
  end?: boolean
  badge?: string
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium transition-colors',
          isActive
            ? 'bg-sidebar-accent text-foreground'
            : 'text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground',
        )
      }
    >
      <Icon className="size-4" />
      <span className="flex-1">{label}</span>
      {badge === 'live' && (
        <span className="flex items-center gap-1 text-[0.6rem] font-medium text-success">
          <span className="size-1.5 rounded-full bg-success pulse-dot" />
        </span>
      )}
    </NavLink>
  )
}
