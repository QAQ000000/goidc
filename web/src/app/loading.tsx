import { Skeleton } from "@/components/ui/skeleton";

export default function Loading() {
  return (
    <main aria-label="加载中" role="status" className="min-h-screen bg-background">
      <div className="mx-auto max-w-5xl space-y-6 px-6 py-10">
        <Skeleton className="h-10 w-56" />
        <Skeleton className="h-4 w-full max-w-xl" />
        <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-40 w-full" />
        </div>
        <Skeleton className="h-56 w-full" />
      </div>
    </main>
  );
}
