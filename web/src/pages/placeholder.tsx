import { Construction } from 'lucide-react'
import { PageHeader } from '@/components/layout/page-header'

export function PlaceholderPage({
  title,
  description,
}: {
  title: string
  description: string
}) {
  return (
    <div>
      <PageHeader title={title} description={description} />
      <div className="flex flex-col items-center justify-center gap-3 px-6 py-24 text-center">
        <div className="flex size-12 items-center justify-center rounded-xl border border-border bg-card">
          <Construction className="size-5 text-muted-foreground" />
        </div>
        <p className="max-w-sm text-sm text-muted-foreground text-pretty">
          This page is part of the roadmap (S6 · T18). The design system and live
          data layer are ready to plug in.
        </p>
      </div>
    </div>
  )
}
