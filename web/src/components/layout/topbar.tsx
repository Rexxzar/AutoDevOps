import { Search, Sun, Moon, Plus, ChevronDown, LogOut } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { useTheme } from '@/lib/theme'
import { useAuth } from '@/lib/auth'
import { NotificationBell } from '@/components/layout/notification-bell'

export function Topbar() {
  const { theme, toggleTheme } = useTheme()
  const { logout } = useAuth()
  const navigate = useNavigate()

  return (
    <header className="relative z-50 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background/80 px-4 backdrop-blur lg:px-6">
      <button className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm font-medium hover:bg-accent">
        <span className="flex size-5 items-center justify-center rounded bg-primary/15 text-[0.6rem] font-bold text-primary">
          AD
        </span>
        autodevops
        <ChevronDown className="size-3.5 text-muted-foreground" />
      </button>

      <div className="relative ml-2 hidden max-w-md flex-1 md:block">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <input
          type="text"
          placeholder="Search deployments, pods, resources..."
          className="h-9 w-full rounded-md border border-border bg-card pl-9 pr-16 text-sm outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/40"
        />
        <kbd className="absolute right-2.5 top-1/2 hidden -translate-y-1/2 rounded border border-border bg-muted px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground lg:block">
          ⌘K
        </kbd>
      </div>

      <div className="ml-auto flex items-center gap-1.5">
        <Button size="sm" className="gap-1.5" onClick={() => navigate('/new-deployment')}>
          <Plus className="size-4" />
          <span className="hidden sm:inline">New deployment</span>
        </Button>
        <Button size="sm" className="gap-1.5" onClick={() => navigate('/new-resource')}>
          <Plus className="size-4" />
          <span className="hidden sm:inline">New resource</span>
        </Button>
        <Button variant="ghost" size="icon-sm" onClick={toggleTheme} aria-label="Toggle theme">
          {theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}
        </Button>
        <NotificationBell />
        <Button variant="ghost" size="icon-sm" onClick={logout} aria-label="Sign out">
          <LogOut className="size-4" />
        </Button>
      </div>
    </header>
  )
}
