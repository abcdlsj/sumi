import { isDesktopShell } from "../shell";
import { readSavedSidebarWidth } from "./Resize";
import { Skeleton } from "./ui";

const W = ["w-[58%]", "w-[82%]", "w-[44%]", "w-[70%]", "w-[52%]", "w-[88%]"];

export function MessageListSkeleton({ rows = 6, compact = false }: { rows?: number; compact?: boolean }) {
  return (
    <div className={compact ? "px-4 py-4 space-y-5" : "px-6 py-5 space-y-6"} aria-label="Loading messages">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex items-start gap-3">
          <Skeleton className={`${compact ? "size-7" : "size-9"} rounded-[6px] shrink-0`} />
          <div className="flex-1 space-y-2 pt-0.5">
            <div className="flex items-center gap-2">
              <Skeleton className="h-3.5 w-24" />
              <Skeleton className="h-3 w-10" />
            </div>
            <Skeleton className={`h-3.5 ${W[i % W.length]}`} />
            {i % 2 === 0 && <Skeleton className={`h-3.5 ${W[(i + 3) % W.length]}`} />}
          </div>
        </div>
      ))}
    </div>
  );
}

export function RowListSkeleton({ rows = 7 }: { rows?: number }) {
  return (
    <div className="divide-y divide-line border-b border-line" aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="h-11 px-6 flex items-center gap-3">
          <Skeleton className="size-4 rounded-full shrink-0" />
          <Skeleton className="h-3.5 w-9 shrink-0" />
          <Skeleton className={`h-3.5 ${W[i % W.length]} max-w-[420px]`} />
          <span className="flex-1" />
          <Skeleton className="size-6 rounded-[4px] shrink-0" />
        </div>
      ))}
    </div>
  );
}

export function InboxSkeleton() {
  return (
    <div className="divide-y divide-line border-b border-line" aria-label="Loading inbox">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="px-6 py-3 flex items-center gap-3">
          <Skeleton className="size-8 rounded-[5px] shrink-0" />
          <div className="flex-1 space-y-2">
            <div className="flex items-center gap-2">
              <Skeleton className="h-3.5 w-24" />
              <Skeleton className="h-3.5 w-28" />
            </div>
            <Skeleton className={`h-3.5 ${W[i % W.length]}`} />
          </div>
          <Skeleton className="h-7 w-16 rounded-[5px]" />
        </div>
      ))}
    </div>
  );
}

export function DetailSkeleton() {
  return (
    <div className="flex flex-col h-full" aria-label="Loading details">
      <div className="h-[52px] px-5 border-b border-line flex items-center gap-2.5">
        <Skeleton className="h-4 w-12" />
        <Skeleton className="h-3.5 w-24" />
      </div>
      <div className="p-5 space-y-4 border-b border-line">
        <Skeleton className="h-5 w-3/4" />
        <Skeleton className="h-3.5 w-full" />
        <div className="grid grid-cols-[96px_1fr] gap-3 pt-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="contents">
              <Skeleton className="h-3.5 w-16" />
              <Skeleton className="h-7 w-36 rounded-[6px]" />
            </div>
          ))}
        </div>
      </div>
      <MessageListSkeleton rows={3} compact />
    </div>
  );
}

function SidebarSkeleton({ desktop }: { desktop: boolean }) {
  return (
    <aside className="bg-nav flex flex-col">
      {!desktop && (
        <div className="h-[52px] px-4 border-b border-nav-line flex items-center">
          <Skeleton nav className="h-4 w-32" />
        </div>
      )}
      <div className="px-3 pt-3 space-y-5">
        <Skeleton nav className="h-8 w-full rounded-[5px]" />
        <div className="space-y-3 px-1">
          <Skeleton nav className="h-4 w-24" />
          <Skeleton nav className="h-4 w-20" />
        </div>
        {[3, 3, 2].map((n, g) => (
          <div key={g} className="space-y-3 px-1">
            <Skeleton nav className="h-3 w-16" />
            {Array.from({ length: n }).map((_, i) => (
              <div key={i} className="flex items-center gap-2.5">
                <Skeleton nav className={`size-6 ${g === 0 ? "rounded-[4px]" : "rounded-[5px]"}`} />
                <Skeleton nav className={`h-3.5 ${["w-24", "w-32", "w-20"][i]}`} />
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
  const desktop = isDesktopShell();
  const sidebarWidth = readSavedSidebarWidth();
  return (
    <div className="h-full grid bg-bg" style={{ gridTemplateColumns: `${56 + sidebarWidth}px minmax(0, 1fr)` }}>
      <div className="flex flex-col min-h-0 border-r border-[var(--nav-edge)] bg-nav">
        {desktop && (
          <div className="h-[52px] pl-[92px] pr-4 border-b border-nav-line flex items-center shrink-0 titlebar-drag">
            <Skeleton nav className="h-4 w-32" />
          </div>
        )}
        <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "56px minmax(0, 1fr)" }}>
          <nav className="bg-nav-rail border-r border-nav-line flex flex-col items-center py-3 gap-2">
            <Skeleton nav className="size-9 rounded-[6px]" />
          </nav>
          <SidebarSkeleton desktop={desktop} />
        </div>
      </div>
      <main className="flex flex-col min-w-0 bg-panel">
        <div className="h-[52px] px-6 border-b border-line flex items-center gap-3 titlebar-drag">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-3.5 w-48" />
        </div>
        <div className="flex-1 min-h-0 overflow-hidden">
          <MessageListSkeleton rows={6} />
        </div>
        <div className="px-5 pb-4 pt-2">
          <Skeleton className="h-[88px] w-full rounded-[6px]" />
        </div>
      </main>
    </div>
  );
}
