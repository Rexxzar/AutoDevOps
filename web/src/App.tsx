import { Routes, Route, Navigate } from 'react-router-dom'
import { Sidebar } from '@/components/layout/sidebar'
import { Topbar } from '@/components/layout/topbar'
import { DeploymentsPage } from '@/pages/deployments'
import { ClustersPage } from '@/pages/clusters'
import { OverviewPage } from '@/pages/overview'
import { PlaceholderPage } from '@/pages/placeholder'
import { LoginPage } from '@/pages/login'
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
            <Route path="/clusters" element={<ClustersPage />} />
            <Route
              path="/resources"
              element={
                <PlaceholderPage
                  title="Cloud Resources"
                  description="Live AWS + Azure inventory polled via cloud SDKs, with a Terraform 'use existing resource' picker."
                />
              }
            />
            <Route
              path="/compliance"
              element={
                <PlaceholderPage
                  title="Compliance"
                  description="Hadolint, Trivy, tfsec and GitLeaks findings with severity-threshold gating."
                />
              }
            />
            <Route
              path="/secrets"
              element={
                <PlaceholderPage
                  title="Secrets"
                  description="AES-256-GCM encrypted vault with argon2id-derived keys and rotation."
                />
              }
            />
            <Route
              path="/settings"
              element={
                <PlaceholderPage
                  title="Settings"
                  description="Control plane configuration, JWT auth, and cluster registration."
                />
              }
            />
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
