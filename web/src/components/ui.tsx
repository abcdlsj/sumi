import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import { AlertCircle, Check, CheckCircle2, Copy, Loader2, X } from "lucide-react";
import type { TaskStatus } from "../api";

type Variant = "primary" | "secondary" | "ghost" | "danger" | "soft";
type Size = "sm" | "md" | "lg";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-accent text-accent-fg hover:bg-accent-hover shadow-sm",
  secondary: "bg-card text-fg border border-line-strong hover:bg-subtle shadow-sm",
  soft: "bg-accent-soft text-accent-text hover:brightness-95",
  ghost: "text-fg-2 hover:bg-hover hover:text-fg",
  danger: "bg-card text-danger border border-line-strong hover:bg-danger-soft hover:border-danger",
};
const SIZES: Record<Size, string> = {
  sm: "h-8 px-3 text-[13px] gap-1.5 rounded-[8px]",
  md: "h-9 px-3.5 text-[14px] gap-2 rounded-[9px]",
  lg: "h-11 px-5 text-[15px] gap-2 rounded-[10px]",
};

export function Button({
  variant = "primary",
  size = "md",
  loading = false,
  icon,
  className = "",
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: Size; loading?: boolean; icon?: ReactNode }) {
  return (
    <button
      {...rest}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={`inline-flex items-center justify-center font-medium whitespace-nowrap select-none transition-[background-color,border-color,color,opacity,transform,filter] duration-150 active:scale-[0.98] disabled:opacity-50 disabled:pointer-events-none ${VARIANTS[variant]} ${SIZES[size]} ${className}`}
    >
      {loading ? <Loader2 className="size-4 animate-spin shrink-0" /> : icon ? <span className="shrink-0 inline-flex [&>svg]:size-4">{icon}</span> : null}
      {children}
    </button>
  );
}

export function IconButton({
  label,
  children,
  className = "",
  active = false,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; active?: boolean }) {
  return (
    <button
      {...rest}
      aria-label={label}
      title={label}
      className={`size-8 rounded-[8px] inline-flex items-center justify-center transition-colors duration-150 [&>svg]:size-4 ${
        active ? "bg-hover text-fg" : "text-fg-3 hover:bg-hover hover:text-fg"
      } ${className}`}
    >
      {children}
    </button>
  );
}

const inputBase =
  "w-full rounded-[9px] border bg-card text-[14px] text-fg outline-none transition-[border-color,box-shadow] duration-150 placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)] disabled:opacity-60";

function FieldShell({
  id,
  label,
  hint,
  error,
  optional,
  children,
}: {
  id: string;
  label?: string;
  hint?: ReactNode;
  error?: string;
  optional?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      {label && (
        <label htmlFor={id} className="flex items-center gap-1.5 text-[13px] font-medium text-fg-2">
          {label}
          {optional && <span className="text-fg-4 font-normal">Optional</span>}
        </label>
      )}
      {children}
      {error ? (
        <p className="flex items-center gap-1.5 text-[12.5px] text-danger animate-fade-in">
          <AlertCircle className="size-3.5 shrink-0" />
          {error}
        </p>
      ) : hint ? (
        <p className="text-[12.5px] text-fg-3 leading-snug">{hint}</p>
      ) : null}
    </div>
  );
}

export function Field({
  label,
  hint,
  error,
  trailing,
  leading,
  optional,
  className = "",
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & {
  label?: string;
  hint?: ReactNode;
  error?: string;
  trailing?: ReactNode;
  leading?: ReactNode;
  optional?: boolean;
}) {
  const auto = useId();
  const id = rest.id ?? auto;
  return (
    <FieldShell id={id} label={label} hint={hint} error={error} optional={optional}>
      <div className="relative">
        {leading && <div className="absolute inset-y-0 left-3 flex items-center text-fg-3 [&>svg]:size-4">{leading}</div>}
        <input
          {...rest}
          id={id}
          aria-invalid={error ? true : undefined}
          className={`${inputBase} h-10 ${leading ? "pl-9" : "pl-3"} ${trailing ? "pr-10" : "pr-3"} ${
            error ? "border-danger" : "border-line-strong hover:border-fg-4"
          } ${className}`}
        />
        {trailing && <div className="absolute inset-y-0 right-1.5 flex items-center">{trailing}</div>}
      </div>
    </FieldShell>
  );
}

export function TextArea({
  label,
  hint,
  error,
  optional,
  className = "",
  ...rest
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { label?: string; hint?: ReactNode; error?: string; optional?: boolean }) {
  const auto = useId();
  const id = rest.id ?? auto;
  return (
    <FieldShell id={id} label={label} hint={hint} error={error} optional={optional}>
      <textarea
        {...rest}
        id={id}
        className={`${inputBase} px-3 py-2.5 leading-relaxed resize-none ${error ? "border-danger" : "border-line-strong hover:border-fg-4"} ${className}`}
      />
    </FieldShell>
  );
}

export function Select({
  label,
  hint,
  className = "",
  children,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement> & { label?: string; hint?: ReactNode }) {
  const auto = useId();
  const id = rest.id ?? auto;
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <select {...rest} id={id} className={`${inputBase} h-10 px-3 border-line-strong hover:border-fg-4 ${className}`}>
        {children}
      </select>
    </FieldShell>
  );
}

export function Skeleton({ className = "", nav = false }: { className?: string; nav?: boolean }) {
  return <span aria-hidden className={`${nav ? "nav-skeleton" : "skeleton"} block ${className}`} />;
}

export function EmptyState({
  icon,
  title,
  body,
  action,
}: {
  icon?: ReactNode;
  title: string;
  body?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="h-full min-h-[280px] flex flex-col items-center justify-center text-center px-6 py-10 animate-fade-in">
      {icon && (
        <div className="size-12 rounded-[14px] bg-subtle border border-line text-fg-3 flex items-center justify-center mb-4 [&>svg]:size-5">
          {icon}
        </div>
      )}
      <div className="text-[16px] font-semibold text-fg">{title}</div>
      {body && <div className="mt-1.5 text-[14px] text-fg-3 max-w-[380px] leading-relaxed">{body}</div>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  full = false,
}: {
  value: T;
  options: { value: T; label: ReactNode; disabled?: boolean }[];
  onChange: (v: T) => void;
  full?: boolean;
}) {
  return (
    <div role="tablist" className={`inline-flex items-center p-[3px] rounded-[10px] bg-subtle border border-line ${full ? "w-full" : ""}`}>
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            disabled={o.disabled}
            onClick={() => onChange(o.value)}
            aria-selected={active}
            className={`${full ? "flex-1" : ""} h-8 px-3 rounded-[7px] text-[13px] inline-flex items-center justify-center gap-1.5 transition-[background-color,color,box-shadow] duration-150 disabled:opacity-40 [&>svg]:size-3.5 ${
              active ? "bg-card text-fg font-medium shadow-sm" : "text-fg-3 hover:text-fg"
            }`}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

export function Badge({
  tone = "neutral",
  children,
  className = "",
}: {
  tone?: "neutral" | "accent" | "warm" | "work" | "danger";
  children: ReactNode;
  className?: string;
}) {
  const tones = {
    neutral: "bg-subtle text-fg-2 border-line",
    accent: "bg-accent-soft text-accent-text border-transparent",
    warm: "bg-warm-soft text-warm border-transparent",
    work: "bg-work-soft text-work border-transparent",
    danger: "bg-danger-soft text-danger border-transparent",
  };
  return (
    <span className={`inline-flex items-center gap-1 h-[22px] px-2 rounded-[6px] border text-[12px] font-medium whitespace-nowrap [&>svg]:size-3 ${tones[tone]} ${className}`}>
      {children}
    </span>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="secondary"
      size="sm"
      icon={copied ? <Check className="text-work" /> : <Copy />}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        });
      }}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}

export function Modal({
  open,
  onClose,
  children,
  width = 480,
  label,
  title,
  description,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  width?: number;
  label: string;
  title?: string;
  description?: ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-40 flex items-start justify-center pt-[10vh] px-4">
      <div className="absolute inset-0 bg-black/30 dark:bg-black/55 backdrop-blur-[2px] animate-fade-in" onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={label}
        style={{ width }}
        className="relative max-w-full max-h-[80vh] flex flex-col bg-panel border border-line rounded-[14px] shadow-lg animate-pop-in"
      >
        {title ? (
          <div className="px-6 pt-5 pb-1 flex items-start justify-between gap-4">
            <div>
              <h2 className="text-[17px] font-semibold text-fg leading-tight">{title}</h2>
              {description && <p className="mt-1 text-[13.5px] text-fg-3 leading-relaxed">{description}</p>}
            </div>
            <IconButton label="Close dialog" onClick={onClose} className="-mr-2 -mt-1">
              <X />
            </IconButton>
          </div>
        ) : (
          <IconButton label="Close dialog" onClick={onClose} className="absolute top-3 right-3 z-10">
            <X />
          </IconButton>
        )}
        <div className="overflow-y-auto scroll-thin">{children}</div>
      </div>
    </div>,
    document.body,
  );
}

type ToastItem = { id: number; tone: "error" | "info" | "success"; text: string };
const ToastContext = createContext<(text: string, tone?: ToastItem["tone"]) => void>(() => {});

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextID = useRef(1);
  const push = useCallback((text: string, tone: ToastItem["tone"] = "info") => {
    const id = nextID.current++;
    setItems((prev) => [...prev.slice(-2), { id, tone, text }]);
    window.setTimeout(() => setItems((prev) => prev.filter((t) => t.id !== id)), 4500);
  }, []);
  return (
    <ToastContext.Provider value={push}>
      {children}
      <div className="fixed bottom-5 right-5 z-50 flex flex-col items-end gap-2 pointer-events-none">
        {items.map((t) => (
          <div
            key={t.id}
            role="status"
            className="pointer-events-auto max-w-[420px] pl-3 pr-4 py-2.5 rounded-[10px] bg-card border border-line shadow-lg text-[13.5px] text-fg flex items-start gap-2.5 animate-rise-in"
          >
            {t.tone === "error" ? (
              <AlertCircle className="size-4 text-danger shrink-0 mt-0.5" />
            ) : (
              <CheckCircle2 className="size-4 text-work shrink-0 mt-0.5" />
            )}
            <span className="leading-snug">{t.text}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast() {
  return useContext(ToastContext);
}

/** Wraps an async action with busy state and turns failures into a toast. */
export function useAction() {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const run = useCallback(
    async (fn: () => Promise<unknown>) => {
      setBusy(true);
      try {
        await fn();
        return true;
      } catch (err) {
        toast(err instanceof Error ? err.message : "Something went wrong", "error");
        return false;
      } finally {
        setBusy(false);
      }
    },
    [toast],
  );
  return { busy, run };
}

/** Small anchored menu; closes on outside click and Escape. */
export function Menu({
  trigger,
  children,
  align = "right",
  width = 240,
}: {
  trigger: (open: boolean, toggle: () => void) => ReactNode;
  children: (close: () => void) => ReactNode;
  align?: "left" | "right";
  width?: number;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <div ref={ref} className="relative">
      {trigger(open, () => setOpen((v) => !v))}
      {open && (
        <div
          role="menu"
          style={{ width }}
          className={`absolute z-30 top-full mt-1.5 ${align === "right" ? "right-0" : "left-0"} bg-card border border-line rounded-[12px] shadow-lg p-1.5 animate-pop-in`}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}

export function MenuItem({
  onSelect,
  active,
  icon,
  children,
  hint,
}: {
  onSelect: () => void;
  active?: boolean;
  icon?: ReactNode;
  children: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <button
      role="menuitemradio"
      aria-checked={active}
      onClick={onSelect}
      className="w-full px-2.5 py-2 rounded-[8px] text-left hover:bg-hover flex items-start gap-2.5 transition-colors"
    >
      <span className="size-5 shrink-0 flex items-center justify-center text-fg-3 [&>svg]:size-4">{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="block text-[14px] text-fg leading-5">{children}</span>
        {hint && <span className="block text-[12.5px] text-fg-3 leading-snug mt-0.5">{hint}</span>}
      </span>
      <span className="size-5 shrink-0 flex items-center justify-center text-accent">{active && <Check className="size-4" />}</span>
    </button>
  );
}

export function SumiMark({ size = 32 }: { size?: number }) {
  return (
    <span
      style={{ width: size, height: size, borderRadius: size * 0.3 }}
      className="bg-gradient-to-br from-[#6d6ef0] to-[#4a4bc4] text-white inline-flex items-center justify-center shrink-0 select-none shadow-sm"
    >
      <svg viewBox="0 0 24 24" width={size * 0.56} height={size * 0.56} fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round">
        <path d="M16.5 7.5c-1-1.3-2.6-2-4.5-2-2.6 0-4.5 1.4-4.5 3.4 0 4.6 9 2.6 9 7.2 0 2-2 3.4-4.6 3.4-2 0-3.7-.8-4.7-2.2" />
      </svg>
    </span>
  );
}

const STATUS_META: Record<TaskStatus, { label: string; color: string }> = {
  todo: { label: "To do", color: "var(--text-3)" },
  in_progress: { label: "In progress", color: "var(--work)" },
  in_review: { label: "In review", color: "var(--warm)" },
  done: { label: "Done", color: "var(--accent)" },
  closed: { label: "Closed", color: "var(--text-4)" },
};

export function taskStatusLabel(s: TaskStatus) {
  return STATUS_META[s].label;
}

export function TaskStatusIcon({ status, size = 16 }: { status: TaskStatus; size?: number }) {
  const c = STATUS_META[status].color;
  const r = 6.5;
  const circ = 2 * Math.PI * 3.25;
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" className="shrink-0" aria-label={STATUS_META[status].label}>
      {status === "todo" && <circle cx="8" cy="8" r={r} fill="none" stroke={c} strokeWidth="1.5" strokeDasharray="2.4 2" />}
      {(status === "in_progress" || status === "in_review") && (
        <>
          <circle cx="8" cy="8" r={r} fill="none" stroke={c} strokeWidth="1.5" />
          <circle
            cx="8"
            cy="8"
            r="3.25"
            fill="none"
            stroke={c}
            strokeWidth="6.5"
            strokeDasharray={`${circ * (status === "in_progress" ? 0.5 : 0.75)} ${circ}`}
            transform="rotate(-90 8 8)"
          />
        </>
      )}
      {status === "done" && (
        <>
          <circle cx="8" cy="8" r="7.25" fill={c} />
          <path d="M5 8.2l2 2 4-4.2" fill="none" stroke="white" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
        </>
      )}
      {status === "closed" && (
        <>
          <circle cx="8" cy="8" r="7.25" fill={c} />
          <path d="M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8" stroke="white" strokeWidth="1.5" strokeLinecap="round" />
        </>
      )}
    </svg>
  );
}
