import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type InputHTMLAttributes,
  type ReactNode,
  type RefObject,
  type TextareaHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import { AlertCircle, Check, CheckCircle2, ChevronDown, Copy, Loader2, Search, X } from "lucide-react";
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

export interface SelectOption<T extends string = string> {
  value: T;
  label: string;
  description?: string;
  icon?: ReactNode;
  trailing?: ReactNode;
  disabled?: boolean;
}

const FOCUSABLE_SELECTOR =
  'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])';

const modalStack: symbol[] = [];

/** Computes viewport-safe fixed coordinates for a portaled popover, flipping upward when near the bottom edge. */
function useFloatingPosition(
  open: boolean,
  triggerRef: RefObject<HTMLElement | null>,
  floatingRef: RefObject<HTMLElement | null>,
  opts: { align?: "left" | "right"; minWidth?: number; width?: number; maxHeight?: number } = {},
) {
  const [style, setStyle] = useState<CSSProperties>({ position: "fixed", visibility: "hidden", top: 0, left: 0 });
  const { align = "left", minWidth, width, maxHeight = 300 } = opts;

  const update = useCallback(() => {
    const tr = triggerRef.current;
    if (!tr) return;
    const rect = tr.getBoundingClientRect();
    const vw = window.innerWidth || 1024;
    const vh = window.innerHeight || 768;
    const pad = 8;
    const gap = 6;
    const flRect = floatingRef.current?.getBoundingClientRect();
    const resolvedWidth = width ?? Math.min(320, Math.max(minWidth ?? rect.width, flRect?.width ?? rect.width));
    const estHeight = Math.min(maxHeight, flRect?.height || 240);

    const spaceBelow = vh - rect.bottom - pad - gap;
    const spaceAbove = rect.top - pad - gap;
    const placeAbove = spaceBelow < estHeight && spaceAbove > spaceBelow;

    let left = align === "right" ? rect.right - resolvedWidth : rect.left;
    left = Math.max(pad, Math.min(left, vw - resolvedWidth - pad));

    const next: CSSProperties = {
      position: "fixed",
      left,
      minWidth: width ?? Math.max(rect.width, minWidth ?? 0),
      width: width,
      maxWidth: `min(320px, calc(100vw - ${pad * 2}px))`,
      visibility: "visible",
    };
    if (placeAbove) {
      next.bottom = Math.max(pad, vh - rect.top + gap);
    } else {
      next.top = Math.min(vh - pad - 48, rect.bottom + gap);
    }
    setStyle(next);
  }, [triggerRef, floatingRef, align, minWidth, width, maxHeight]);

  useLayoutEffect(() => {
    if (!open) return;
    update();
    const raf = window.requestAnimationFrame(update);
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.cancelAnimationFrame(raf);
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open, update]);

  return style;
}

/** Custom accessible portal-backed dropdown select; replaces native <select> across the UI. */
export function Select<T extends string = string>({
  label,
  hint,
  optional,
  value,
  onChange,
  options,
  placeholder = "Select…",
  searchable = false,
  searchPlaceholder = "Search…",
  size = "md",
  disabled = false,
  align = "left",
  ariaLabel,
  className = "",
  menuWidth,
}: {
  label?: string;
  hint?: ReactNode;
  optional?: boolean;
  value: T;
  onChange: (value: T) => void;
  options: SelectOption<T>[];
  placeholder?: string;
  searchable?: boolean;
  searchPlaceholder?: string;
  size?: "sm" | "md";
  disabled?: boolean;
  align?: "left" | "right";
  ariaLabel?: string;
  className?: string;
  menuWidth?: number;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIdx, setActiveIdx] = useState(0);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const searchRef = useRef<HTMLInputElement | null>(null);
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const selected = useMemo(() => options.find((o) => o.value === value), [options, value]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((o) => o.label.toLowerCase().includes(q) || (o.description ?? "").toLowerCase().includes(q));
  }, [options, query]);

  const floatingStyle = useFloatingPosition(open, triggerRef, menuRef, { align, width: menuWidth, maxHeight: 300 });

  useEffect(() => {
    if (!open) {
      setQuery("");
      return;
    }
    const selIdx = filtered.findIndex((o) => o.value === value && !o.disabled);
    setActiveIdx(selIdx >= 0 ? selIdx : 0);
    if (searchable) {
      window.setTimeout(() => searchRef.current?.focus(), 0);
    }
  }, [open, searchable]);

  useEffect(() => {
    setActiveIdx((idx) => (filtered.length === 0 ? 0 : Math.min(idx, filtered.length - 1)));
  }, [filtered.length]);

  useEffect(() => {
    if (!open) return;
    optionRefs.current[activeIdx]?.scrollIntoView?.({ block: "nearest" });
  }, [open, activeIdx]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (triggerRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopImmediatePropagation();
        setOpen(false);
        triggerRef.current?.focus();
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveIdx((i) => (filtered.length ? (i + 1) % filtered.length : 0));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveIdx((i) => (filtered.length ? (i - 1 + filtered.length) % filtered.length : 0));
      } else if (e.key === "Home") {
        e.preventDefault();
        setActiveIdx(0);
      } else if (e.key === "End") {
        e.preventDefault();
        setActiveIdx(Math.max(0, filtered.length - 1));
      } else if (e.key === "Enter" && filtered[activeIdx] && !filtered[activeIdx].disabled) {
        e.preventDefault();
        onChange(filtered[activeIdx].value);
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open, filtered, activeIdx, onChange]);

  const heightCls = size === "sm" ? "h-8 px-2.5 text-[13.5px] rounded-[8px]" : "h-10 px-3 text-[14px] rounded-[9px]";

  const trigger = (
    <div className={`relative ${className}`}>
      <button
        ref={triggerRef}
        id={id}
        type="button"
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => setOpen((v) => !v)}
        className={`w-full ${heightCls} border bg-card flex items-center gap-2 text-left transition-[border-color,box-shadow,background-color] duration-150 disabled:opacity-55 ${
          open ? "border-accent shadow-[var(--ring)]" : "border-line-strong hover:border-fg-4"
        }`}
      >
        {selected?.icon && <span className="shrink-0 flex items-center [&>svg]:size-4">{selected.icon}</span>}
        <span className={`min-w-0 flex-1 truncate ${selected ? "text-fg font-medium" : "text-fg-4"}`}>
          {selected ? selected.label : placeholder}
        </span>
        <ChevronDown className={`size-3.5 text-fg-3 shrink-0 transition-transform duration-150 ${open ? "rotate-180" : ""}`} />
      </button>
      {open &&
        createPortal(
          <div
            ref={menuRef}
            role="listbox"
            aria-label={ariaLabel ?? label ?? "Options"}
            style={floatingStyle}
            className="z-50 bg-card border border-line rounded-[12px] shadow-lg p-1.5 animate-pop-in"
          >
            {searchable && (
              <div className="relative px-1 pb-1.5">
                <Search className="size-3.5 text-fg-4 absolute left-3 top-[9px] pointer-events-none" />
                <input
                  ref={searchRef}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={searchPlaceholder}
                  className="w-full h-8 pl-7 pr-2.5 rounded-[7px] bg-subtle border border-line text-[13px] text-fg outline-none placeholder:text-fg-4 focus:border-accent"
                />
              </div>
            )}
            <div className="max-h-[240px] overflow-y-auto scroll-thin space-y-0.5">
              {filtered.length === 0 ? (
                <div className="px-2.5 py-3 text-center text-[13px] text-fg-3">No matches</div>
              ) : (
                filtered.map((opt, idx) => {
                  const isSelected = opt.value === value;
                  const isActive = idx === activeIdx;
                  return (
                    <button
                      key={opt.value || "__empty__"}
                      ref={(el) => {
                        optionRefs.current[idx] = el;
                      }}
                      type="button"
                      role="option"
                      aria-selected={isSelected}
                      disabled={opt.disabled}
                      onMouseEnter={() => setActiveIdx(idx)}
                      onClick={() => {
                        onChange(opt.value);
                        setOpen(false);
                        triggerRef.current?.focus();
                      }}
                      className={`w-full px-2.5 py-1.5 rounded-[8px] flex items-center gap-2 text-left transition-colors disabled:opacity-45 disabled:pointer-events-none ${
                        isActive ? "bg-hover" : "hover:bg-hover"
                      }`}
                    >
                      {opt.icon && <span className="shrink-0 flex items-center [&>svg]:size-4">{opt.icon}</span>}
                      <span className="min-w-0 flex-1">
                        <span className={`block truncate text-[13.5px] leading-5 ${isSelected ? "font-semibold text-fg" : "text-fg"}`}>{opt.label}</span>
                        {opt.description && <span className="block truncate text-[12px] text-fg-3 leading-tight">{opt.description}</span>}
                      </span>
                      {opt.trailing && <span className="shrink-0 text-[11.5px] text-fg-4">{opt.trailing}</span>}
                      <span className="size-4 shrink-0 flex items-center justify-center text-accent">{isSelected && <Check className="size-3.5" />}</span>
                    </button>
                  );
                })
              )}
            </div>
          </div>,
          document.body,
        )}
    </div>
  );

  if (!label && !hint) return trigger;
  return (
    <FieldShell id={id} label={label} hint={hint} optional={optional}>
      {trigger}
    </FieldShell>
  );
}

/** Custom searchable multi-select checklist for picking members/items inside dialogs. */
export function MultiSelectList<T extends string = string>({
  label,
  hint,
  optional,
  values,
  onChange,
  options,
  emptyText = "No items available.",
  searchPlaceholder = "Filter…",
}: {
  label?: string;
  hint?: ReactNode;
  optional?: boolean;
  values: T[];
  onChange: (next: T[]) => void;
  options: SelectOption<T>[];
  emptyText?: string;
  searchPlaceholder?: string;
}) {
  const id = useId();
  const [q, setQ] = useState("");
  const selected = useMemo(() => new Set(values), [values]);
  const filtered = useMemo(() => {
    const term = q.trim().toLowerCase();
    if (!term) return options;
    return options.filter((o) => o.label.toLowerCase().includes(term) || (o.description ?? "").toLowerCase().includes(term));
  }, [options, q]);

  function toggle(v: T) {
    if (selected.has(v)) onChange(values.filter((x) => x !== v));
    else onChange([...values, v]);
  }

  return (
    <FieldShell id={id} label={label} hint={hint} optional={optional}>
      <div className="rounded-[10px] border border-line-strong bg-card overflow-hidden">
        {options.length > 5 && (
          <div className="relative px-2.5 py-2 border-b border-line">
            <Search className="size-3.5 text-fg-4 absolute left-4 top-1/2 -translate-y-1/2 pointer-events-none" />
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={searchPlaceholder}
              className="w-full h-7 pl-6 pr-2 rounded-[6px] bg-subtle text-[13px] text-fg outline-none placeholder:text-fg-4"
            />
          </div>
        )}
        <div className="max-h-[180px] overflow-y-auto scroll-thin p-1 space-y-0.5">
          {filtered.length === 0 ? (
            <div className="px-3 py-4 text-center text-[13px] text-fg-3">{options.length === 0 ? emptyText : "No matches"}</div>
          ) : (
            filtered.map((opt) => {
              const checked = selected.has(opt.value);
              return (
                <button
                  key={opt.value}
                  type="button"
                  role="checkbox"
                  aria-checked={checked}
                  disabled={opt.disabled}
                  onClick={() => toggle(opt.value)}
                  className={`w-full px-2.5 py-1.5 rounded-[7px] flex items-center gap-2.5 text-left transition-colors ${
                    checked ? "bg-accent-soft/70" : "hover:bg-subtle"
                  }`}
                >
                  <span
                    aria-hidden
                    className={`size-4 rounded-[5px] border flex items-center justify-center shrink-0 transition-colors ${
                      checked ? "bg-accent border-accent text-accent-fg" : "border-line-strong bg-card"
                    }`}
                  >
                    {checked && <Check className="size-3 stroke-[2.5]" />}
                  </span>
                  {opt.icon && <span className="shrink-0 flex items-center">{opt.icon}</span>}
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[13.5px] font-medium text-fg leading-5">{opt.label}</span>
                    {opt.description && <span className="block truncate text-[11.5px] text-fg-3 leading-tight">{opt.description}</span>}
                  </span>
                  {opt.trailing && <span className="shrink-0 text-[11.5px] text-fg-4">{opt.trailing}</span>}
                </button>
              );
            })
          )}
        </div>
      </div>
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
      <div className="font-display text-[19px] font-semibold text-fg">{title}</div>
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
  tone?: "neutral" | "accent" | "warm" | "work" | "online" | "busy" | "danger";
  children: ReactNode;
  className?: string;
}) {
  const tones = {
    neutral: "bg-subtle text-fg-2 border-line",
    accent: "bg-accent-soft text-accent-text border-transparent",
    warm: "bg-warm-soft text-warm border-transparent",
    work: "bg-work-soft text-work border-transparent",
    online: "bg-online-soft text-online border-transparent",
    busy: "bg-busy-soft text-busy border-transparent",
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
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    const token = Symbol("modal");
    modalStack.push(token);
    const prevFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;

    const onKey = (e: KeyboardEvent) => {
      if (modalStack[modalStack.length - 1] !== token) return;
      if (e.key === "Escape") {
        e.preventDefault();
        onCloseRef.current();
        return;
      }
      if (e.key === "Tab" && dialogRef.current) {
        const focusables = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
        if (focusables.length === 0) return;
        const first = focusables[0];
        const last = focusables[focusables.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      const idx = modalStack.indexOf(token);
      if (idx >= 0) modalStack.splice(idx, 1);
      if (prevFocus && document.contains(prevFocus)) prevFocus.focus();
    };
  }, [open]);

  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-40 flex items-start justify-center pt-[10vh] px-4">
      <div className="absolute inset-0 bg-black/30 dark:bg-black/55 backdrop-blur-[2px] animate-fade-in" onClick={onClose} />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        style={{ width }}
        className="relative max-w-full max-h-[80vh] flex flex-col bg-panel border border-line rounded-[14px] shadow-lg animate-pop-in"
      >
        {title ? (
          <div className="px-6 pt-5 pb-1 flex items-start justify-between gap-4">
            <div>
              <h2 className="font-display text-[20px] font-semibold text-fg leading-tight">{title}</h2>
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

/** Portal-backed anchored menu; auto-flips near viewport edges and closes on outside click or Escape. */
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
  const triggerWrapRef = useRef<HTMLDivElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const floatingStyle = useFloatingPosition(open, triggerWrapRef, menuRef, { align, width, maxHeight: 360 });

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (triggerWrapRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopImmediatePropagation();
        setOpen(false);
      }
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open]);

  return (
    <div ref={triggerWrapRef} className="relative">
      {trigger(open, () => setOpen((v) => !v))}
      {open &&
        createPortal(
          <div
            ref={menuRef}
            role="menu"
            style={floatingStyle}
            className="z-50 bg-card border border-line rounded-[12px] shadow-lg p-1.5 animate-pop-in max-h-[360px] overflow-y-auto scroll-thin"
          >
            {children(() => setOpen(false))}
          </div>,
          document.body,
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
      style={{ width: size, height: size, borderRadius: size * 0.3, background: "linear-gradient(135deg, var(--accent), var(--accent-hover))" }}
      className="text-accent-fg inline-flex items-center justify-center shrink-0 select-none shadow-sm"
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
  in_review: { label: "In review", color: "var(--review)" },
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
