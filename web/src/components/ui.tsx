import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { Check, Copy, Loader2, X } from "lucide-react";

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "xs" | "sm" | "md";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-ink text-white hover:bg-[#2F2E2B] active:bg-black",
  secondary: "bg-white text-ink border border-field hover:border-ink hover:bg-paper-2",
  ghost: "text-ink-2 hover:bg-hover hover:text-ink",
  danger: "bg-white text-danger border border-field hover:bg-danger-bg hover:border-danger",
};
const SIZES: Record<Size, string> = {
  xs: "h-6 px-2 text-[11.5px] gap-1",
  sm: "h-7 px-2.5 text-[12px] gap-1.5",
  md: "h-9 px-3.5 text-[13px] gap-2",
};

export function Button({
  variant = "primary",
  size = "sm",
  loading = false,
  className = "",
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: Size; loading?: boolean }) {
  return (
    <button
      {...rest}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={`inline-flex items-center justify-center rounded-[6px] font-medium select-none transition-[background-color,border-color,color,opacity,transform] duration-150 active:scale-[0.98] disabled:opacity-45 disabled:active:scale-100 ${VARIANTS[variant]} ${SIZES[size]} ${className}`}
    >
      {loading && <Loader2 className="w-3.5 h-3.5 animate-spin shrink-0" />}
      {children}
    </button>
  );
}

export function Field({
  label,
  hint,
  error,
  trailing,
  className = "",
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: ReactNode; error?: string; trailing?: ReactNode }) {
  const id = rest.id ?? `f-${label.toLowerCase().replace(/\W+/g, "-")}`;
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-[12px] font-medium text-ink-2">
        {label}
      </label>
      <div className="relative">
        <input
          {...rest}
          id={id}
          aria-invalid={error ? true : undefined}
          className={`w-full h-9 px-3 rounded-[6px] border bg-white text-[13.5px] outline-none transition-[border-color,box-shadow] duration-150 placeholder:text-faint ${
            error ? "border-danger" : "border-field hover:border-[#BDB9AE] focus:border-ink"
          } focus:shadow-[0_0_0_3px_rgba(22,22,20,0.06)] ${trailing ? "pr-9" : ""} ${className}`}
        />
        {trailing && <div className="absolute inset-y-0 right-1 flex items-center">{trailing}</div>}
      </div>
      {error ? (
        <p className="text-[11.5px] text-danger animate-fade-in">{error}</p>
      ) : hint ? (
        <p className="text-[11.5px] text-faint">{hint}</p>
      ) : null}
    </div>
  );
}

export function Skeleton({ className = "" }: { className?: string }) {
  return <span aria-hidden className={`skeleton block ${className}`} />;
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
    <div className="h-full min-h-[220px] flex flex-col items-center justify-center text-center px-6 animate-fade-in">
      {icon && (
        <div className="w-10 h-10 rounded-[10px] border border-line bg-paper-2 text-mute flex items-center justify-center mb-3">
          {icon}
        </div>
      )}
      <div className="text-[13.5px] font-semibold text-ink">{title}</div>
      {body && <div className="mt-1 text-[12.5px] text-mute max-w-[320px] leading-relaxed">{body}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  size = "sm",
}: {
  value: T;
  options: { value: T; label: ReactNode }[];
  onChange: (v: T) => void;
  size?: "sm" | "md";
}) {
  return (
    <div className={`inline-flex items-center p-0.5 rounded-[7px] bg-paper border border-line ${size === "md" ? "w-full" : ""}`}>
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            onClick={() => onChange(o.value)}
            aria-pressed={active}
            className={`${size === "md" ? "flex-1 h-8 text-[13px]" : "h-6 px-2.5 text-[12px]"} rounded-[5px] transition-[background-color,color,box-shadow] duration-150 ${
              active ? "bg-white text-ink font-medium shadow-[0_0_0_1px_#E4E1D9]" : "text-mute hover:text-ink"
            }`}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="secondary"
      size="xs"
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        });
      }}
    >
      {copied ? <Check className="w-3 h-3 text-work" /> : <Copy className="w-3 h-3" />}
      {copied ? "Copied" : label}
    </Button>
  );
}

export function Modal({
  open,
  onClose,
  children,
  width = 440,
  label,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  width?: number;
  label: string;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-40 flex items-start justify-center pt-[12vh] px-4">
      <div className="absolute inset-0 bg-[#161614]/20 animate-fade-in" onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={label}
        style={{ width }}
        className="relative max-w-full bg-white border border-line-strong rounded-[10px] animate-pop-in"
      >
        <button
          onClick={onClose}
          aria-label="Close dialog"
          className="absolute top-2.5 right-2.5 w-6 h-6 rounded-[5px] text-mute hover:bg-paper hover:text-ink flex items-center justify-center"
        >
          <X className="w-3.5 h-3.5" />
        </button>
        {children}
      </div>
    </div>,
    document.body,
  );
}

type ToastItem = { id: number; tone: "error" | "info"; text: string };
const ToastContext = createContext<(text: string, tone?: ToastItem["tone"]) => void>(() => {});

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextID = useRef(1);
  const push = useCallback((text: string, tone: ToastItem["tone"] = "info") => {
    const id = nextID.current++;
    setItems((prev) => [...prev.slice(-2), { id, tone, text }]);
    window.setTimeout(() => setItems((prev) => prev.filter((t) => t.id !== id)), 4000);
  }, []);
  return (
    <ToastContext.Provider value={push}>
      {children}
      <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 flex flex-col items-center gap-2 pointer-events-none">
        {items.map((t) => (
          <div
            key={t.id}
            role="status"
            className={`pointer-events-auto px-3 h-8 rounded-[7px] text-[12.5px] flex items-center gap-2 animate-rise-in ${
              t.tone === "error" ? "bg-[#3A1712] text-white" : "bg-ink text-white"
            }`}
          >
            {t.tone === "error" && <span className="w-1.5 h-1.5 rounded-full bg-[#F08A74]" />}
            {t.text}
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

export function SumiMark({ size = 28 }: { size?: number }) {
  return (
    <span
      style={{ width: size, height: size, fontSize: size * 0.44 }}
      className="rounded-[7px] bg-ink text-white font-semibold inline-flex items-center justify-center shrink-0 select-none"
    >
      S
    </span>
  );
}

/** Small anchored menu; closes on outside click and Escape. */
export function Menu({
  trigger,
  children,
  align = "right",
}: {
  trigger: (open: boolean, toggle: () => void) => ReactNode;
  children: (close: () => void) => ReactNode;
  align?: "left" | "right";
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
          className={`absolute z-30 top-full mt-1 ${align === "right" ? "right-0" : "left-0"} min-w-[200px] bg-white border border-line-strong rounded-[8px] p-1 animate-pop-in`}
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
  children,
  hint,
}: {
  onSelect: () => void;
  active?: boolean;
  children: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <button
      role="menuitemradio"
      aria-checked={active}
      onClick={onSelect}
      className="w-full px-2 py-1.5 rounded-[5px] text-left hover:bg-paper flex items-start gap-2"
    >
      <span className="w-3.5 pt-0.5 shrink-0">{active && <Check className="w-3.5 h-3.5" />}</span>
      <span className="min-w-0">
        <span className="block text-[12.5px] text-ink">{children}</span>
        {hint && <span className="block text-[11px] text-faint leading-snug">{hint}</span>}
      </span>
    </button>
  );
}
