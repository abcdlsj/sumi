import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Apple, Bot, Laptop, Monitor, Terminal } from "lucide-react";
import { api, ApiError, ownComputers, type Computer, type Member } from "../api";
import { clearPendingPairing, readPendingPairing, type PendingPairing } from "../pairing";
import { AgentForm } from "./AgentForm";
import { Button, CopyButton, Field, Segmented, Skeleton, useAction } from "./ui";

function useSpaceBoot(spaceSlug: string, poll: boolean) {
  return useQuery({
    queryKey: ["bootstrap", spaceSlug],
    queryFn: () => api.bootstrap(spaceSlug),
    refetchInterval: poll ? 2000 : false,
  });
}

export function StepHeading({ icon, title, body }: { icon: ReactNode; title: string; body: ReactNode }) {
  return (
    <div className="flex items-start gap-3.5">
      <div className="size-10 rounded-[12px] bg-accent-soft text-accent-text flex items-center justify-center shrink-0 [&>svg]:size-5">{icon}</div>
      <div className="pt-0.5">
        <h1 className="font-display text-[21px] font-semibold text-fg leading-tight">{title}</h1>
        <p className="mt-1 text-[14px] text-fg-3 leading-relaxed">{body}</p>
      </div>
    </div>
  );
}

export function ComputerRow({ c }: { c: Computer }) {
  const online = c.connection_status === "online";
  return (
    <div className="h-14 px-4 flex items-center gap-3 animate-rise-in">
      <span className="size-9 rounded-[10px] bg-subtle text-fg-2 flex items-center justify-center shrink-0">
        <Monitor className="size-4" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[14px] font-medium text-fg truncate">{c.name}</span>
        <span className="block text-[12.5px] text-fg-3 truncate">
          {c.hostname} · {c.os === "macos" ? "macOS" : "Linux"}
        </span>
      </span>
      <span className={`inline-flex items-center gap-2 text-[13px] font-medium ${online ? "text-work" : "text-fg-3"}`}>
        <span className={`size-2 rounded-full ${online ? "bg-work" : "bg-fg-4 animate-breathe"}`} />
        {online ? "Online" : "Waiting for connection"}
      </span>
    </div>
  );
}

/** Confirmation card for a pairing requested by the desktop app on this machine. */
export function PairingCard({
  spaceSlug,
  pairing,
  onConfirmed,
  onDismiss,
}: {
  spaceSlug: string;
  pairing: PendingPairing;
  onConfirmed: () => void;
  onDismiss?: () => void;
}) {
  const qc = useQueryClient();
  const { busy, run } = useAction();
  const infoQ = useQuery({
    queryKey: ["pairing", pairing.id],
    queryFn: () => api.getPairing(pairing.id, pairing.code).then((r) => r.pairing),
    retry: false,
  });
  const [name, setName] = useState("");
  useEffect(() => {
    if (infoQ.data && !name) setName(infoQ.data.hostname.replace(/\.local$/, ""));
  }, [infoQ.data]);

  const dismiss = () => {
    clearPendingPairing();
    onDismiss?.();
  };

  if (infoQ.isLoading) {
    return (
      <div className="rounded-[14px] border border-line p-5 space-y-3">
        <Skeleton className="h-5 w-44" />
        <Skeleton className="h-4 w-60" />
        <Skeleton className="h-10 w-full rounded-[9px]" />
      </div>
    );
  }

  const gone = (infoQ.error instanceof ApiError && infoQ.error.status === 404) || (infoQ.data && infoQ.data.status !== "pending");
  if (gone || !infoQ.data) {
    return (
      <div className="rounded-[14px] border border-line bg-subtle p-5 text-[14px] text-fg-2 animate-fade-in">
        {infoQ.data?.status === "confirmed"
          ? "This computer is already connected."
          : "This pairing request has expired. The desktop app issues a new one automatically."}
        {onDismiss && (
          <div className="mt-4">
            <Button variant="secondary" size="sm" onClick={dismiss}>
              Dismiss
            </Button>
          </div>
        )}
      </div>
    );
  }

  const info = infoQ.data;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void run(async () => {
          await api.confirmPairing(pairing.id, pairing.code, name.trim() || info.hostname);
          clearPendingPairing();
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          onConfirmed();
        });
      }}
      className="rounded-[14px] border border-accent shadow-[var(--ring)] bg-card overflow-hidden animate-rise-in"
    >
      <div className="px-5 py-4 flex items-center gap-3.5 border-b border-line">
        <span className="size-11 rounded-[12px] bg-accent-soft text-accent-text flex items-center justify-center shrink-0">
          <Laptop className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="text-[15px] font-semibold text-fg truncate">{info.hostname}</div>
          <div className="text-[13px] text-fg-3 flex items-center gap-1.5">
            {info.os === "macos" && <Apple className="size-3.5" />}
            {info.os === "macos" ? "macOS" : "Linux"} · Sumi desktop app
          </div>
        </div>
        <div className="text-right">
          <div className="text-[12px] text-fg-3">Code</div>
          <div className="font-mono text-[17px] font-semibold tracking-[0.18em] text-fg">{pairing.code}</div>
        </div>
      </div>
      <div className="p-5 space-y-4">
        <Field label="Computer name" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} />
        <p className="text-[13px] text-fg-3 leading-relaxed">
          Confirm only if this code matches the one in the Sumi app on your computer. It joins your account, and agents you create in any of your spaces can run on it.
        </p>
        <div className="flex items-center gap-2">
          <Button type="submit" size="lg" loading={busy} className="flex-1">
            Connect this computer
          </Button>
          {onDismiss && (
            <Button type="button" variant="ghost" size="lg" onClick={dismiss}>
              Not now
            </Button>
          )}
        </div>
      </div>
    </form>
  );
}

export function ComputerStep({ spaceSlug, onNext, nextLabel = "Continue" }: { spaceSlug: string; onNext: () => void; nextLabel?: string }) {
  const qc = useQueryClient();
  const [pairing, setPairing] = useState<PendingPairing | null>(() => readPendingPairing());
  const [compName, setCompName] = useState("my-computer");
  const [osName, setOsName] = useState<"macos" | "linux">("macos");
  const [issued, setIssued] = useState<string | null>(null);
  const [manual, setManual] = useState(false);
  const { busy, run } = useAction();
  const bootQ = useSpaceBoot(spaceSlug, true);
  const computers = bootQ.data ? ownComputers(bootQ.data) : [];
  const anyOnline = computers.some((c) => c.connection_status === "online");
  const command = issued ? `sumi computer --server ${window.location.origin} --token ${issued}` : "";

  return (
    <div className="space-y-6">
      <StepHeading
        icon={<Monitor />}
        title="Connect a computer"
        body="Agents run on a computer you own. Their workspace, memory and coding-agent logins stay on that machine."
      />

      {pairing ? (
        <PairingCard spaceSlug={spaceSlug} pairing={pairing} onConfirmed={() => setPairing(null)} onDismiss={() => setPairing(null)} />
      ) : issued ? (
        <div className="space-y-2.5 animate-rise-in">
          <div className="text-[14px] font-medium text-fg">Run this on {compName}</div>
          <div className="rounded-[12px] border border-line bg-subtle p-3.5 flex items-start gap-3">
            <Terminal className="size-4 text-fg-3 mt-0.5 shrink-0" />
            <code className="flex-1 font-mono text-[13px] break-all text-fg-2 leading-relaxed">{command}</code>
            <CopyButton text={command} />
          </div>
          <p className="text-[13px] text-fg-3">The token is shown once. Keep it private.</p>
        </div>
      ) : computers.length > 0 && !manual ? null : (
        <div className="space-y-4">
          <div className="rounded-[12px] border border-line bg-subtle px-4 py-3.5 flex items-start gap-3">
            <Laptop className="size-5 text-fg-2 shrink-0 mt-0.5" />
            <div className="text-[13.5px] text-fg-2 leading-relaxed">
              <span className="font-medium text-fg">Using the Sumi desktop app?</span> Open it on your Mac — it asks to join and you confirm it here with one click.
            </div>
          </div>
          <form
            onSubmit={(e: FormEvent) => {
              e.preventDefault();
              void run(async () => {
                const res = await api.pairComputer(compName.trim(), compName.trim(), osName);
                setIssued(res.token);
                void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
              });
            }}
            className="space-y-3"
          >
            <div className="text-[13px] font-medium text-fg-2">Or connect any machine with a command</div>
            <div className="flex items-end gap-2">
              <div className="flex-1">
                <Field aria-label="Computer name" value={compName} onChange={(e) => setCompName(e.target.value)} required />
              </div>
              <Segmented
                value={osName}
                onChange={setOsName}
                options={[
                  { value: "macos", label: "macOS" },
                  { value: "linux", label: "Linux" },
                ]}
              />
            </div>
            <Button type="submit" variant="secondary" loading={busy} icon={<Terminal />} className="w-full">
              Generate connect command
            </Button>
          </form>
        </div>
      )}

      {computers.length > 0 && (
        <div className="rounded-[12px] border border-line bg-card divide-y divide-line overflow-hidden">
          {computers.map((c) => (
            <ComputerRow key={c.id} c={c} />
          ))}
        </div>
      )}
      {computers.length > 0 && !manual && !issued && !pairing && (
        <button type="button" onClick={() => setManual(true)} className="text-[13.5px] text-accent-text hover:underline underline-offset-2">
          Connect another computer
        </button>
      )}

      <div className="flex items-center justify-between pt-1">
        <button type="button" onClick={onNext} className="text-[14px] text-fg-3 hover:text-fg">
          Skip for now
        </button>
        <Button size="lg" disabled={computers.length === 0} onClick={onNext}>
          {anyOnline || computers.length === 0 ? nextLabel : `${nextLabel} while it connects`}
        </Button>
      </div>
    </div>
  );
}

export function AgentStep({
  spaceSlug,
  onDone,
  onSkip,
}: {
  spaceSlug: string;
  onDone: (agent: Member) => void;
  onSkip: () => void;
}) {
  const qc = useQueryClient();
  const bootQ = useSpaceBoot(spaceSlug, false);
  const computers = bootQ.data ? ownComputers(bootQ.data) : [];

  return (
    <div className="space-y-6">
      <StepHeading
        icon={<Bot />}
        title="Create your first agent"
        body="An agent is a persistent teammate with its own role, memory and workspace. You can add more later."
      />
      {bootQ.isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-14 w-full rounded-[12px]" />
          <Skeleton className="h-24 w-full rounded-[12px]" />
        </div>
      ) : computers.length === 0 ? (
        <div className="rounded-[12px] border border-line bg-subtle p-4 text-[14px] text-fg-2">
          Connect a computer first. Agents need a computer to run on.
        </div>
      ) : (
        <AgentForm
          spaceSlug={spaceSlug}
          computers={computers}
          onCreated={(agent) => {
            void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
            onDone(agent);
          }}
          footer={(submit) => (
            <div className="flex items-center justify-between pt-1">
              <button type="button" onClick={onSkip} className="text-[14px] text-fg-3 hover:text-fg">
                Skip for now
              </button>
              {submit}
            </div>
          )}
        />
      )}
      {computers.length === 0 && (
        <div className="flex justify-start">
          <button type="button" onClick={onSkip} className="text-[14px] text-fg-3 hover:text-fg">
            Skip for now
          </button>
        </div>
      )}
    </div>
  );
}
