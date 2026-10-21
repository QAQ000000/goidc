// Adapted from Kiranism/next-shadcn-dashboard-starter v1.0.0 (MIT):
// src/components/layout/page-container.tsx and src/components/ui/heading.tsx.
// See web/licenses/next-shadcn-dashboard-starter.MIT.
import type { ReactNode } from "react";

// Only presentation is imported. Identity and request states stay with the domain.
export function AdminPageContainer({ children, pageTitle, pageDescription, pageHeaderAction }: {
  children: ReactNode;
  pageTitle: string;
  pageDescription?: string;
  pageHeaderAction?: ReactNode;
}) {
  return <div className="flex min-w-0 flex-1 flex-col">
    <div className="mb-5 flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-1.5">
        <h1 className="break-words text-2xl font-semibold tracking-tight">{pageTitle}</h1>
        {pageDescription && <p className="text-sm text-muted-foreground">{pageDescription}</p>}
      </div>
      {pageHeaderAction && <div className="flex max-w-full flex-wrap items-center gap-2">{pageHeaderAction}</div>}
    </div>
    <div className="min-w-0 space-y-5">{children}</div>
  </div>;
}
