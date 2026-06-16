import { useState } from 'react'
import { useNavigate, Link } from 'react-router-dom'
import { Plus, ExternalLink, Terminal, RotateCcw, FileText } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Button } from '@/components/ui/button'
import { ProvisionedTable } from '@/components/provisioned-table'
import { useProvisionedResources } from '@/lib/use-provisioned-resources'
import { useHostInventory, containerStateKind, podPhaseKind } from '@/lib/use-host-inventory'
import type { InventoryContainer, InventoryPod } from '@/lib/use-host-inventory'
import { StatusBadge } from '@/components/ui/status'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { InventoryLogsDialog } from '@/components/inventory-logs-dialog'
import { ResourceConsole } from '@/components/resource-console'
import { apiFetch } from '@/lib/api'

// Truncate a container image ref to keep the table readable.
function truncateImage(image: string, maxLen = 48) {
  return image.length > maxLen ? '…' + image.slice(-(maxLen - 1)) : image
}

export function LocalResourcesPage() {
  const navigate = useNavigate()
  const {
    provisioned, runs, expanded, setExpanded, actingOn, provisionedLoading,
    handleApply, handleDestroy, handleDismiss,
  } = useProvisionedResources()

  const { data: inventory } = useHostInventory()

  const [showAll, setShowAll] = useState(false)

  // Logs dialog state.
  const [logsItem, setLogsItem] = useState<{ id: string; name: string; containers: string[] } | null>(null)

  // Console dialog state.
  const [consoleItem, setConsoleItem] = useState<{ id: string; name: string } | null>(null)

  // Per-row restart loading state.
  const [restarting, setRestarting] = useState<Record<string, boolean>>({})

  const localProvisioned = provisioned.filter((r) => r.provider === 'local')

  const containers: InventoryContainer[] = inventory?.containers ?? []
  const pods: InventoryPod[] = inventory?.pods ?? []

  const visibleContainers = showAll ? containers : containers.filter((c) => c.managed)
  const visiblePods = showAll ? pods : pods.filter((p) => p.managed)

  async function handleRestart(id: string) {
    if (!confirm('Restart this resource?')) return
    setRestarting((prev) => ({ ...prev, [id]: true }))
    try {
      await apiFetch(`/local/inventory/${encodeURIComponent(id)}/restart`, { method: 'POST' })
    } catch {
      // Silently ignore — the user can see state via the inventory refetch.
    } finally {
      setRestarting((prev) => { const next = { ...prev }; delete next[id]; return next })
    }
  }

  return (
    <div>
      <PageHeader
        title="Local Resources"
        description="Run standalone Docker containers and Kubernetes workloads on this machine."
      >
        <Button size="sm" className="gap-1.5" onClick={() => navigate('/new-resource')}>
          <Plus className="size-4" />
          New Resource
        </Button>
      </PageHeader>

      <div className="p-4 lg:p-6 space-y-6">
        {/* AutoDevOps-provisioned resources */}
        <div className="space-y-3">
          <h2 className="text-sm font-semibold">AutoDevOps Resources</h2>
          <ProvisionedTable
            items={localProvisioned}
            loading={provisionedLoading}
            emptyMessage="No local resources yet. Click New Resource and choose Local to run a Docker container or Kubernetes workload on this machine."
            runs={runs}
            expanded={expanded}
            setExpanded={setExpanded}
            actingOn={actingOn}
            onApply={handleApply}
            onDestroy={handleDestroy}
            onDismiss={handleDismiss}
            navigate={navigate}
            showRegion={false}
            detailBasePath="/local-resources"
          />
        </div>

        {/* Host inventory header + toggle */}
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold">Host Inventory</h2>
          <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer select-none">
            <input
              type="checkbox"
              className="rounded"
              checked={showAll}
              onChange={(e) => setShowAll(e.target.checked)}
            />
            Show all (including non-AutoDevOps)
          </label>
        </div>

        {/* Docker containers */}
        {inventory?.docker_available === false ? (
          <p className="text-xs text-muted-foreground">Docker not available on this host.</p>
        ) : (
          <Card>
            <CardHeader className="py-3 px-4">
              <CardTitle className="text-sm">Docker Containers</CardTitle>
            </CardHeader>
            <CardContent className="p-0">
              {visibleContainers.length === 0 ? (
                <p className="text-xs text-muted-foreground px-4 py-3">
                  {showAll ? 'No Docker containers found.' : 'No AutoDevOps-managed Docker containers. Toggle "Show all" to see all host containers.'}
                </p>
              ) : (
                <table className="w-full text-xs">
                  <thead>
                    <tr className="border-b bg-muted/30">
                      <th className="text-left px-4 py-2 font-medium">Name</th>
                      <th className="text-left px-4 py-2 font-medium">Image</th>
                      <th className="text-left px-4 py-2 font-medium">State</th>
                      <th className="text-right px-4 py-2 font-medium">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visibleContainers.map((c) => (
                      <tr key={c.id} className="border-b last:border-0 hover:bg-muted/20">
                        <td className="px-4 py-2 font-mono">
                          <span>{c.name}</span>
                          {c.managed && c.link_path && (
                            <Link
                              to={c.link_path}
                              className="ml-2 inline-flex items-center gap-0.5 text-primary text-[10px] hover:underline"
                            >
                              <ExternalLink className="w-2.5 h-2.5" />
                              {c.link_label}
                            </Link>
                          )}
                        </td>
                        <td className="px-4 py-2 font-mono text-muted-foreground" title={c.image}>
                          {truncateImage(c.image)}
                        </td>
                        <td className="px-4 py-2">
                          <StatusBadge status={containerStateKind(c.state)} label={c.state} />
                        </td>
                        <td className="px-4 py-2">
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 px-2 gap-1"
                              title="View logs"
                              onClick={() => setLogsItem({ id: c.id, name: c.name, containers: [] })}
                            >
                              <FileText className="w-3 h-3" />
                              Logs
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 px-2 gap-1"
                              title="Open terminal"
                              onClick={() => setConsoleItem({ id: c.id, name: c.name })}
                            >
                              <Terminal className="w-3 h-3" />
                              Connect
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 px-2 gap-1 text-muted-foreground hover:text-foreground"
                              title="Restart container"
                              disabled={restarting[c.id]}
                              onClick={() => handleRestart(c.id)}
                            >
                              <RotateCcw className={`w-3 h-3 ${restarting[c.id] ? 'animate-spin' : ''}`} />
                              Restart
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </CardContent>
          </Card>
        )}

        {/* K8s pods */}
        {inventory?.k8s_available === false ? (
          <p className="text-xs text-muted-foreground">Kubernetes not available on this host.</p>
        ) : (
          <Card>
            <CardHeader className="py-3 px-4">
              <CardTitle className="text-sm">Kubernetes Pods</CardTitle>
            </CardHeader>
            <CardContent className="p-0">
              {visiblePods.length === 0 ? (
                <p className="text-xs text-muted-foreground px-4 py-3">
                  {showAll ? 'No pods found in non-system namespaces.' : 'No AutoDevOps-managed pods. Toggle "Show all" to see all cluster pods.'}
                </p>
              ) : (
                <table className="w-full text-xs">
                  <thead>
                    <tr className="border-b bg-muted/30">
                      <th className="text-left px-4 py-2 font-medium">Pod</th>
                      <th className="text-left px-4 py-2 font-medium">Namespace</th>
                      <th className="text-left px-4 py-2 font-medium">Phase</th>
                      <th className="text-right px-4 py-2 font-medium">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visiblePods.map((p) => (
                      <tr key={p.id} className="border-b last:border-0 hover:bg-muted/20">
                        <td className="px-4 py-2 font-mono">
                          <span>{p.name}</span>
                          {p.managed && p.link_path && (
                            <Link
                              to={p.link_path}
                              className="ml-2 inline-flex items-center gap-0.5 text-primary text-[10px] hover:underline"
                            >
                              <ExternalLink className="w-2.5 h-2.5" />
                              {p.link_label}
                            </Link>
                          )}
                        </td>
                        <td className="px-4 py-2 font-mono text-muted-foreground">{p.namespace}</td>
                        <td className="px-4 py-2">
                          <StatusBadge status={podPhaseKind(p.phase)} label={p.phase} />
                        </td>
                        <td className="px-4 py-2">
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 px-2 gap-1"
                              title="View logs"
                              onClick={() => setLogsItem({ id: p.id, name: p.name, containers: p.containers })}
                            >
                              <FileText className="w-3 h-3" />
                              Logs
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 px-2 gap-1"
                              title="Open terminal"
                              onClick={() => setConsoleItem({ id: p.id, name: p.name })}
                            >
                              <Terminal className="w-3 h-3" />
                              Connect
                            </Button>
                            {p.restartable && (
                              <Button
                                size="sm"
                                variant="ghost"
                                className="h-7 px-2 gap-1 text-muted-foreground hover:text-foreground"
                                title="Delete pod (controller will recreate it)"
                                disabled={restarting[p.id]}
                                onClick={() => handleRestart(p.id)}
                              >
                                <RotateCcw className={`w-3 h-3 ${restarting[p.id] ? 'animate-spin' : ''}`} />
                                Restart
                              </Button>
                            )}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </CardContent>
          </Card>
        )}
      </div>

      {logsItem && (
        <InventoryLogsDialog
          open={!!logsItem}
          onClose={() => setLogsItem(null)}
          inventoryId={logsItem.id}
          name={logsItem.name}
          containers={logsItem.containers}
        />
      )}

      {consoleItem && (
        <ResourceConsole
          resourceId={consoleItem.id}
          resourceName={consoleItem.name}
          open={!!consoleItem}
          onClose={() => setConsoleItem(null)}
        />
      )}
    </div>
  )
}
