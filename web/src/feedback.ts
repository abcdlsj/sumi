// One place for short-lived confirmations: a mutation names its success and
// error lines in `meta`, and the Toaster shows them. Pages keep inline errors
// where a form needs them; toasts cover actions with nowhere else to answer.
import { MutationCache } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";

export interface Toast {
  id: number;
  tone: "success" | "error";
  text: string;
}

type Line = string | ((data: unknown, variables: unknown) => string | undefined);

declare module "@tanstack/react-query" {
  interface Register {
    mutationMeta: {
      /** Shown when the mutation succeeds. */
      success?: Line;
      /** Shown, with the Server's reason, when the mutation fails. */
      error?: string;
    };
  }
}

let toasts: readonly Toast[] = [];
let nextId = 1;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((listener) => listener());

export function notify(tone: Toast["tone"], text: string) {
  const id = nextId++;
  // The same line twice in a row replaces the first instead of stacking.
  toasts = [...toasts.filter((toast) => toast.text !== text).slice(-2), { id, tone, text }];
  emit();
  window.setTimeout(() => dismiss(id), tone === "error" ? 7000 : 3500);
}

export function dismiss(id: number) {
  toasts = toasts.filter((toast) => toast.id !== id);
  emit();
}

export function useToasts(): readonly Toast[] {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => toasts,
  );
}

export function feedbackMutationCache(): MutationCache {
  return new MutationCache({
    onSuccess: (data, variables, _context, mutation) => {
      const line = mutation.meta?.success;
      const text = typeof line === "function" ? line(data, variables) : line;
      if (text) notify("success", text);
    },
    onError: (error, _variables, _context, mutation) => {
      const line = mutation.meta?.error;
      if (!line) return;
      const reason = error instanceof Error && error.message ? error.message : undefined;
      notify("error", reason ? `${line} ${reason}` : line);
    },
  });
}
