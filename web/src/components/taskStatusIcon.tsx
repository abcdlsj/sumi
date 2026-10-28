import type { TaskStatus } from "../api/client";

/** One glyph per Task status, shared by the Tasks page and Message badges.
 * Shape carries the meaning (dashed, half, dot, check, slash); colour only
 * reinforces it, so the five states stay distinct without colour vision. */
export function TaskStatusIcon({ status }: { status: TaskStatus }) {
  return (
    <svg className={`task-glyph task-glyph--${status}`} viewBox="0 0 14 14" aria-hidden="true">
      {status === "done" ? (
        <>
          <circle cx="7" cy="7" r="6" fill="currentColor" />
          <path d="M4.5 7.2l1.7 1.7 3.4-3.6" fill="none" stroke="var(--bg)" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
        </>
      ) : status === "closed" ? (
        <>
          <circle cx="7" cy="7" r="5.25" fill="none" stroke="currentColor" strokeWidth="1.4" />
          <path d="M3.8 10.2l6.4-6.4" stroke="currentColor" strokeWidth="1.4" />
        </>
      ) : status === "in_review" ? (
        <>
          <circle cx="7" cy="7" r="5.25" fill="none" stroke="currentColor" strokeWidth="1.4" />
          <circle cx="7" cy="7" r="2.1" fill="currentColor" />
        </>
      ) : status === "in_progress" ? (
        <>
          <circle cx="7" cy="7" r="5.25" fill="none" stroke="currentColor" strokeWidth="1.4" />
          <path d="M7 3.4a3.6 3.6 0 010 7.2z" fill="currentColor" />
        </>
      ) : (
        <circle cx="7" cy="7" r="5.25" fill="none" stroke="currentColor" strokeWidth="1.4" strokeDasharray="2.2 1.7" />
      )}
    </svg>
  );
}
