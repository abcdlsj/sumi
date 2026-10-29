// Quiet placeholders that hold the shape of content while it loads, so the
// page doesn't jump when data arrives. Widths vary per row to read as text.
const WIDTHS = [72, 54, 86, 63, 44, 78, 58, 68];

function Bar({ width, className = "" }: { width: number | string; className?: string }) {
  return <span className={`skeleton-bar ${className}`} style={{ width: typeof width === "number" ? `${width}%` : width }} />;
}

/** Message rows: avatar, a name line and one or two body lines. */
export function TimelineSkeleton({ rows = 5, label = "Loading messages" }: { rows?: number; label?: string }) {
  return (
    <div className="skeleton skeleton-timeline" role="status" aria-label={label}>
      {Array.from({ length: rows }, (_, index) => (
        <div className="skeleton-message" key={index}>
          <span className="skeleton-avatar" />
          <div>
            <Bar width={index % 2 ? 18 : 24} className="skeleton-bar--name" />
            <Bar width={WIDTHS[index % WIDTHS.length]} />
            {index % 3 === 0 ? <Bar width={WIDTHS[(index + 3) % WIDTHS.length] - 20} /> : null}
          </div>
        </div>
      ))}
    </div>
  );
}

/** List rows for Tasks, Inbox and Members. */
export function ListSkeleton({ rows = 6, label, avatar = true }: { rows?: number; label: string; avatar?: boolean }) {
  return (
    <div className="skeleton skeleton-list" role="status" aria-label={label}>
      {Array.from({ length: rows }, (_, index) => (
        <div className="skeleton-row" key={index}>
          {avatar ? <span className="skeleton-avatar skeleton-avatar--small" /> : <span className="skeleton-dot" />}
          <Bar width={WIDTHS[index % WIDTHS.length] * 0.6} />
          <Bar width="72px" className="skeleton-bar--meta" />
        </div>
      ))}
    </div>
  );
}

/** A detail page: title block, a row of facts and two content blocks. */
export function DetailSkeleton({ label }: { label: string }) {
  return (
    <div className="skeleton skeleton-detail" role="status" aria-label={label}>
      <div className="skeleton-detail-head">
        <span className="skeleton-avatar skeleton-avatar--large" />
        <div>
          <Bar width="180px" className="skeleton-bar--title" />
          <Bar width="260px" />
        </div>
      </div>
      <div className="skeleton-facts">
        {Array.from({ length: 4 }, (_, index) => (
          <div key={index}><Bar width="40%" className="skeleton-bar--meta" /><Bar width="65%" /></div>
        ))}
      </div>
      <span className="skeleton-block" />
      <span className="skeleton-block skeleton-block--short" />
    </div>
  );
}

/** The whole Space shell before the Space itself has loaded. */
export function ShellSkeleton() {
  return (
    <div className="skeleton-shell" role="status" aria-label="Opening Space">
      <aside className="skeleton-shell-nav">
        <div className="skeleton-shell-space"><span className="skeleton-avatar skeleton-avatar--square" /><Bar width="90px" /></div>
        {Array.from({ length: 4 }, (_, index) => <Bar key={`tool-${index}`} width={`${50 + (index % 3) * 12}%`} className="skeleton-bar--nav" />)}
        <Bar width="64px" className="skeleton-bar--meta skeleton-bar--section" />
        {Array.from({ length: 3 }, (_, index) => <Bar key={`channel-${index}`} width={`${40 + index * 14}%`} className="skeleton-bar--nav" />)}
        <Bar width="88px" className="skeleton-bar--meta skeleton-bar--section" />
        {Array.from({ length: 3 }, (_, index) => <Bar key={`dm-${index}`} width={`${46 + index * 10}%`} className="skeleton-bar--nav" />)}
      </aside>
      <div className="skeleton-shell-main">
        <div className="skeleton-shell-header"><Bar width="140px" className="skeleton-bar--title" /></div>
        <TimelineSkeleton rows={6} label="Loading" />
      </div>
    </div>
  );
}
