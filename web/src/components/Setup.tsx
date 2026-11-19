import { useEffect, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, CheckCircle2, Laptop, Monitor, Terminal } from "lucide-react";
import { api, ApiError, nameInput, sanitizeDisplayName, type Computer, type DriverKind } from "../api";
import { clearPendingPairing, readPendingPairing, type PendingPairing } from "../pairing";
import { Button, CopyButton, Field, Segmented, Skeleton, useAction } from "./ui";

function useSpaceBoot(spaceSlug: string, poll: boolean) {
  return useQuery({
    queryKey: ["bootstrap", spaceSlug],
    queryFn: () => api.bootstrap(spaceSlug),
    refetchInterval: poll ? 2000 : false,
  });
}

function StepHeading({ icon, title, body }: { icon: React.ReactNode; title: string; body: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <div className="w-9 h-9 rounded-[8px] border border-line bg-paper-2 text-ink-2 flex items-center justify-center shrink-0">
        {icon}
      </div>
      <div>
        <h2 className="text-[15px] font-semibold text-ink">{title}</h2>
        <p className="mt-0.5 text-[12.5px] text-mute leading-relaxed">{body}</p>
      </div>
    </div>
  );
}

function ComputerRow({ c }: { c: Computer }) {
  const online = c.connection_status === "online";
  return (
    <div className="h-11 px-3 flex items-center justify-between text-[13px] animate-rise-in">
      <span className="flex items-center gap-2.5 min-w-0">
        <Monitor className="w-4 h-4 text-mute shrink-0" />
        <span className="font-medium truncate">{c.name}</span>
        <span className="text-[11.5px] text-faint truncate">{c.hostname}</span>
      </span>
      <span className={`inline-flex items-center gap-1.5 text-[12px] ${online ? "text-work" : "text-faint"}`}>
        <span className={`w-1.5 h-1.5 rounded-full ${online ? "bg-work" : "bg-[#BDB9AE] animate-breathe"}`} />
        {online ? "Online" : "Waiting…"}
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

  const gone =
    (infoQ.error instanceof ApiError && infoQ.error.status === 404) ||
    (infoQ.data && infoQ.data.status !== "pending");

  if (infoQ.isLoading) {
    return (
      <div className="border border-line rounded-[8px] p-4 space-y-3">
        <Skeleton className="h-4 w-40" />
        <Skeleton className="h-3 w-56" />
        <Skeleton className="h-9 w-full rounded-[6px]" />
      </div>
    );
  }

  if (gone || !infoQ.data) {
    return (
      <div className="border border-line rounded-[8px] p-4 text-[12.5px] text-mute animate-fade-in">
        {infoQ.data?.status === "confirmed"
          ? "This computer is already connected."
          : "This pairing request has expired. The desktop app will issue a new one automatically."}
        {onDismiss && (
          <div className="mt-3">
            <Button
              variant="secondary"
              onClick={() => {
                clearPendingPairing();
                onDismiss();
              }}
            >
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
          await api.confirmPairing(spaceSlug, pairing.id, pairing.code, name.trim() || info.hostname);
          clearPendingPairing();
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          onConfirmed();
        });
      }}
      className="border border-ink rounded-[8px] overflow-hidden animate-rise-in"
    >
      <div className="px-4 py-3 bg-paper-2 border-b border-line flex items-center gap-3">
        <Laptop className="w-5 h-5 text-ink shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="text-[13.5px] font-semibold truncate">{info.hostname}</div>
          <div className="text-[11.5px] text-mute">{info.os === "macos" ? "macOS" : "Linux"} · Sumi desktop app</div>
        </div>
        <div className="text-right">
          <div className="text-[10.5px] uppercase tracking-wider text-faint">Code</div>
          <div className="font-mono-num text-[14px] font-semibold tracking-[0.12em]">{pairing.code}</div>
        </div>
      </div>
      <div className="p-4 space-y-3">
        <Field label="Computer name" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} />
        <p className="text-[11.5px] text-mute leading-relaxed">
          Confirm only if the code matches the one shown by the Sumi app on this machine. Agents you create here run
          on it.
        </p>
        <div className="flex items-center gap-2">
          <Button type="submit" size="md" loading={busy} className="flex-1">
            Connect this computer
          </Button>
          {onDismiss && (
            <Button
              type="button"
              variant="ghost"
              size="md"
              onClick={() => {
                clearPendingPairing();
                onDismiss();
              }}
            >
              Not now
            </Button>
          )}
        </div>
      </div>
    </form>
  );
}

export function ComputerStep({ spaceSlug, onNext }: { spaceSlug: string; onNext: () => void }) {
  const qc = useQueryClient();
  const [pairing, setPairing] = useState<PendingPairing | null>(() => readPendingPairing());
  const [compName, setCompName] = useState("my-mac");
  const [osName, setOsName] = useState<"macos" | "linux">("macos");
  const [issued, setIssued] = useState<{ id: string; token: string } | null>(null);
  const [showManual, setShowManual] = useState(false);
  const { busy, run } = useAction();
  const bootQ = useSpaceBoot(spaceSlug, true);
  const computers = bootQ.data?.computers ?? [];
  const anyOnline = computers.some((c) => c.connection_status === "online");
  const command = issued ? `sumi computer --server ${window.location.origin} --token ${issued.token}` : "";

  return (
    <div className="space-y-5">
      <StepHeading
        icon={<Monitor className="w-4.5 h-4.5" />}
        title="Connect a computer"
        body="Agents live on a computer you own. Their workspace, memory and model credentials never leave it."
      />

      {pairing ? (
        <PairingCard spaceSlug={spaceSlug} pairing={pairing} onConfirmed={() => setPairing(null)} onDismiss={() => setPairing(null)} />
      ) : !issued && computers.length > 0 && !showManual ? (
        <button type="button" onClick={() => setShowManual(true)} className="text-[12.5px] text-mute hover:text-ink underline decoration-field underline-offset-2">
          Connect another computer with a command
        </button>
      ) : !issued ? (
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            void run(async () => {
              const res = await api.pairComputer(spaceSlug, compName.trim(), compName.trim(), osName);
              setIssued({ id: res.computer.id, token: res.token });
              void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
            });
          }}
          className="space-y-3"
        >
          <div className="grid grid-cols-[1fr_auto] gap-2 items-end">
            <Field label="Computer name" value={compName} onChange={(e) => setCompName(e.target.value)} required />
            <Segmented
              value={osName}
              onChange={setOsName}
              options={[
                { value: "macos", label: "macOS" },
                { value: "linux", label: "Linux" },
              ]}
            />
          </div>
          <Button type="submit" size="md" variant={computers.length > 0 ? "secondary" : "primary"} loading={busy} className="w-full">
            <Terminal className="w-4 h-4" /> Generate connect command
          </Button>
          <p className="text-[11.5px] text-faint text-center">Using the Sumi desktop app? It connects itself — just confirm here.</p>
        </form>
      ) : (
        <div className="space-y-2 animate-rise-in">
          <div className="text-[12px] font-medium text-ink-2">Run this on {compName}:</div>
          <div className="rounded-[8px] border border-line bg-paper-2 p-3 flex items-start gap-3">
            <code className="flex-1 font-mono-num text-[11.5px] break-all text-ink-2 leading-relaxed">{command}</code>
            <CopyButton text={command} />
          </div>
          <p className="text-[11.5px] text-faint">The token is shown once. Keep it private.</p>
        </div>
      )}

      {computers.length > 0 && (
        <div className="border border-line rounded-[8px] divide-y divide-line">
          {computers.map((c) => (
            <ComputerRow key={c.id} c={c} />
          ))}
        </div>
      )}

      <div className="flex items-center justify-between pt-1">
        <button type="button" onClick={onNext} className="text-[12.5px] text-mute hover:text-ink">
          Skip for now
        </button>
        <Button size="md" disabled={!anyOnline && computers.length === 0} onClick={onNext}>
          {anyOnline ? (
            <>
              <CheckCircle2 className="w-4 h-4" /> Continue
            </>
          ) : (
            "Continue"
          )}
        </Button>
      </div>
    </div>
  );
}

const ROLE_PRESETS = [
  "Senior engineer. Reads the codebase, proposes small reviewed changes.",
  "Research assistant. Collects sources and writes concise summaries.",
  "Project coordinator. Breaks requests into Tasks and tracks them.",
];

export function AgentStep({
  spaceSlug,
  onDone,
  onSkip,
}: {
  spaceSlug: string;
  onDone: (agentMemberID: string) => void;
  onSkip: () => void;
}) {
  const qc = useQueryClient();
  const bootQ = useSpaceBoot(spaceSlug, false);
  const computers = bootQ.data?.computers ?? [];
  const [computerID, setComputerID] = useState("");
  const [name, setName] = useState("Lin");
  const [driver, setDriver] = useState<DriverKind>("builtin");
  const [role, setRole] = useState(ROLE_PRESETS[0]);
  const { busy, run } = useAction();

  const chosen = computerID || computers.find((c) => c.connection_status === "online")?.id || computers[0]?.id || "";

  return (
    <div className="space-y-5">
      <StepHeading
        icon={<Bot className="w-4.5 h-4.5" />}
        title="Create your first agent"
        body="An agent is a persistent teammate with its own role and memory. You can add more later."
      />
      {bootQ.isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-9 w-full rounded-[6px]" />
          <Skeleton className="h-20 w-full rounded-[6px]" />
        </div>
      ) : computers.length === 0 ? (
        <div className="rounded-[8px] border border-line bg-paper-2 p-4 text-[12.5px] text-mute">
          Connect a computer first. Agents need a computer to run on.
        </div>
      ) : (
        <form
          id="agent-step"
          onSubmit={(e) => {
            e.preventDefault();
            void run(async () => {
              const res = await api.createAgent(spaceSlug, {
                display_name: sanitizeDisplayName(name),
                role_text: role.trim(),
                computer_id: chosen,
                driver_kind: driver,
              });
              void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
              onDone(res.agent.id);
            });
          }}
          className="space-y-3.5"
        >
          <div className="grid grid-cols-[1fr_auto] gap-2 items-end">
            <Field
              label="Name"
              value={name}
              onChange={(e) => setName(nameInput(e.target.value))}
              hint="Letters, digits and underscores. Others @mention it by this name."
              required
            />
            <div className="pb-[22px]">
              <Segmented
                value={driver}
                onChange={setDriver}
                options={[
                  { value: "builtin", label: "Builtin" },
                  { value: "acp", label: "ACP" },
                ]}
              />
            </div>
          </div>
          {computers.length > 1 && (
            <div className="space-y-1">
              <label className="block text-[12px] font-medium text-ink-2">Runs on</label>
              <select
                value={chosen}
                onChange={(e) => setComputerID(e.target.value)}
                className="w-full h-9 px-2.5 rounded-[6px] border border-field bg-white text-[13px] outline-none focus:border-ink"
              >
                {computers.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} · {c.connection_status}
                  </option>
                ))}
              </select>
            </div>
          )}
          <div className="space-y-1">
            <label htmlFor="agent-role" className="block text-[12px] font-medium text-ink-2">
              Role
            </label>
            <textarea
              id="agent-role"
              rows={3}
              value={role}
              onChange={(e) => setRole(e.target.value)}
              className="w-full px-3 py-2 rounded-[6px] border border-field hover:border-[#BDB9AE] focus:border-ink bg-white text-[13px] outline-none resize-none transition-colors"
            />
            <div className="flex flex-wrap gap-1.5">
              {ROLE_PRESETS.map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => setRole(p)}
                  className={`h-6 px-2 rounded-[5px] border text-[11.5px] transition-colors ${
                    role === p ? "border-ink text-ink bg-paper-2" : "border-line text-mute hover:border-field hover:text-ink"
                  }`}
                >
                  {p.split(".")[0]}
                </button>
              ))}
            </div>
          </div>
        </form>
      )}
      <div className="flex items-center justify-between pt-1">
        <button type="button" onClick={onSkip} className="text-[12.5px] text-mute hover:text-ink">
          Skip for now
        </button>
        <Button form="agent-step" type="submit" size="md" loading={busy} disabled={computers.length === 0 || !name}>
          Create agent
        </Button>
      </div>
    </div>
  );
}
