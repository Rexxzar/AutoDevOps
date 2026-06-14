import { useNavigate } from 'react-router-dom'
import { Plus } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'
import { Button } from '@/components/ui/button'
import { ProvisionedTable } from '@/components/provisioned-table'
import { useProvisionedResources } from '@/lib/use-provisioned-resources'

export function LocalResourcesPage() {
  const navigate = useNavigate()
  const {
    provisioned, runs, expanded, setExpanded, actingOn, provisionedLoading,
    handleApply, handleDestroy, handleDismiss,
  } = useProvisionedResources()

  const localProvisioned = provisioned.filter((r) => r.provider === 'local')

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
        <div className="space-y-3">
          <h2 className="text-sm font-semibold">Local Resources</h2>
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
      </div>
    </div>
  )
}
