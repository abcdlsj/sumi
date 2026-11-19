import { Skeleton } from "./ui";

const W = ["w-[62%]", "w-[84%]", "w-[45%]", "w-[73%]", "w-[56%]", "w-[90%]"];

export function MessageListSkeleton({ rows = 6, compact = false }: { rows?: number; compact?: boolean }) {
  return (
    <div className={compact ? "p-3.5 space-y-4" : "py-3 space-y-1"} aria-label="Loading messages">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className={`${compact ? "" : "px-5 py-2"} flex items-start gap-3`}>
          <Skeleton className={`${compact ? "w-4 h-4" : "w-7 h-7"} ${i % 3 === 1 ? "rounded-[4px]" : "rounded-full"} shrink-0`} />
          <div className="flex-1 space-y-1.5 pt-0.5">
            <div className="flex items-center gap-2">
              <Skeleton className="h-3 w-20" />
              <Skeleton className="h-2.5 w-9" />
            </div>
            <Skeleton className={`h-3 ${W[i % W.length]}`} />
            {i % 2 === 0 && <Skeleton className={`h-3 ${W[(i + 3) % W.length]}`} />}
          </div>
        </div>
      ))}
    </div>
  );
}

export function RowListSkeleton({ rows = 7, height = "h-9" }: { rows?: number; height?: string }) {
  return (
    <div className="divide-y divide-line" aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className={`${height} px-4 flex items-center gap-3`}>
          <Skeleton className="w-3 h-3 rounded-full shrink-0" />
          <Skeleton className="h-3 w-7 shrink-0" />
          <Skeleton className={`h-3 ${W[i % W.length]} max-w-[420px]`} />
          <span className="flex-1" />
          <Skeleton className="h-3 w-16 shrink-0" />
        </div>
      ))}
    </div>
  );
}

export function InboxSkeleton() {
  return (
    <div className="divide-y divide-line" aria-label="Loading inbox">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="px-4 py-3 flex items-center gap-4">
          <div className="flex-1 space-y-2">
            <div className="flex items-center gap-2">
              <Skeleton className="h-3 w-14" />
              <Skeleton className="h-3 w-24" />
              <Skeleton className="h-2.5 w-9" />
            </div>
            <Skeleton className={`h-3 ${W[i % W.length]}`} />
          </div>
          <Skeleton className="h-6 w-12 rounded-[6px]" />
        </div>
      ))}
    </div>
  );
}

export function DetailSkeleton() {
  return (
    <div className="flex flex-col min-h-0 h-full" aria-label="Loading details">
      <div className="h-11 px-3.5 border-b border-line flex items-center gap-2 bg-white">
        <Skeleton className="h-3.5 w-10" />
        <Skeleton className="h-3 w-24" />
      </div>
      <div className="p-3.5 space-y-3 bg-white border-b border-line">
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-3 w-full" />
        <div className="grid grid-cols-[76px_1fr] gap-2.5 pt-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="contents">
              <Skeleton className="h-3 w-14" />
              <Skeleton className="h-5 w-28 rounded-[5px]" />
            </div>
          ))}
        </div>
      </div>
      <MessageListSkeleton rows={3} compact />
    </div>
  );
}

function SidebarSkeleton() {
  return (
    <aside className="bg-paper border-r border-line-strong flex flex-col">
      <div className="h-11 px-3 border-b border-line flex items-center">
        <Skeleton className="h-3.5 w-28" />
      </div>
      <div className="p-2 space-y-5">
        <div className="space-y-2 px-2 pt-1">
          <Skeleton className="h-3.5 w-24" />
          <Skeleton className="h-3.5 w-20" />
        </div>
        {[4, 3].map((n, g) => (
          <div key={g} className="space-y-2.5 px-2">
            <Skeleton className="h-2.5 w-16" />
            {Array.from({ length: n }).map((_, i) => (
              <div key={i} className="flex items-center gap-2">
                <Skeleton className={`w-4 h-4 ${g === 1 ? "rounded-full" : "rounded-[3px]"}`} />
                <Skeleton className={`h-3 ${["w-24", "w-32", "w-20", "w-28"][i]}`} />
              </div>
            ))}
          </div>
        ))}
      </div>
    </aside>
  );
}

/** Same column geometry as the Workbench so the swap is layout-stable. */
export function WorkbenchSkeleton() {
  return (
    <div className="h-full grid bg-white animate-fade-in" style={{ gridTemplateColumns: "44px 228px minmax(0, 1fr)" }}>
      <nav className="bg-rail border-r border-line-strong flex flex-col items-center py-2 gap-2">
        <Skeleton className="w-7 h-7 rounded-[6px]" />
      </nav>
      <SidebarSkeleton />
      <main className="flex flex-col min-w-0">
        <div className="h-11 px-4 border-b border-line flex items-center gap-3">
          <Skeleton className="h-3.5 w-28" />
          <Skeleton className="h-3 w-40" />
        </div>
        <div className="flex-1 min-h-0 overflow-hidden">
          <MessageListSkeleton rows={7} />
        </div>
        <div className="border-t border-line bg-paper-2 h-[100px]" />
      </main>
    </div>
  );
}
