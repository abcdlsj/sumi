import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

/** A centred empty view: one icon, one sentence of title, a line of help and an optional next step. */
export function EmptyState({
  icon: Icon,
  title,
  children,
  action,
  titleId,
}: {
  icon: LucideIcon;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  titleId?: string;
}) {
  return (
    <section className="empty-state" role="status" aria-labelledby={titleId}>
      <span className="empty-state-icon" aria-hidden="true"><Icon /></span>
      <h2 id={titleId}>{title}</h2>
      {children ? <p>{children}</p> : null}
      {action ? <div className="empty-state-action">{action}</div> : null}
    </section>
  );
}
