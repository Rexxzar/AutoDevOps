import { Routes, Route, Navigate } from 'react-router-dom'
import { Sidebar } from '@/components/layout/sidebar'
import { Topbar } from '@/components/layout/topbar'
import { DeploymentsPage } from '@/pages/deployments'
import { ClustersPage } from '@/pages/clusters'
import { OverviewPage } from '@/pages/overview'
import { ResourcesPage } from '@/pages/resources'
import { LocalResourcesPage } from '@/pages/local-resources'
import { ResourceDetailPage } from '@/pages/resource-detail'
import { DeploymentDetailPage } from '@/pages/deployment-detail'
import { CompliancePage } from '@/pages/compliance'
import { SettingsPage } from '@/pages/settings'
import { PlaceholderPage } from '@/pages/placeholder'
import { LoginPage } from '@/pages/login'
import { NewDeploymentPage } from '@/pages/new-deployment'
import { NewResourcePage } from '@/pages/new-resource'
import { useAuth } from '@/lib/auth'

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { token } = useAuth()
  if (!token) return <Navigate to="/login" replace />
  return <>{children}</>
}

function DashboardLayout() {
  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar />
        <main className="flex-1 overflow-y-auto">
          <Routes>
            <Route path="/" element={<OverviewPage />} />
            <Route path="/deployments" element={<DeploymentsPage />} />
            <Route path="/deployments/:id" element={<DeploymentDetailPage />} />
            <Route path="/clusters" element={<ClustersPage />} />
            <Route path="/resources" element={<ResourcesPage />} />
            <Route path="/local-resources" element={<LocalResourcesPage />} />
            <Route path="/resources/:id" element={<ResourceDetailPage />} />
            <Route path="/local-resources/:id" element={<ResourceDetailPage />} />
            <Route path="/compliance" element={<CompliancePage />} />
            <Route
              path="/secrets"
              element={
                <PlaceholderPage
                  title="Secrets"
                  description="AES-256-GCM encrypted vault with argon2id-derived keys and rotation."
                />
              }
            />
            <Route path="/settings" element={<SettingsPage />} />
          </Routes>
        </main>
      </div>
    </div>
  )
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/new-deployment"
        element={
          <RequireAuth>
            <NewDeploymentPage />
          </RequireAuth>
        }
      />
      <Route
        path="/new-resource"
        element={
          <RequireAuth>
            <NewResourcePage />
          </RequireAuth>
        }
      />
      <Route
        path="/*"
        element={
          <RequireAuth>
            <DashboardLayout />
          </RequireAuth>
        }
      />
    </Routes>
  )
}
