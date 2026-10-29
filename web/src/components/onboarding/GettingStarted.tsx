import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, LoaderCircle, X } from "lucide-react";
import { type FormEvent, useState } from "react";

import { addChannelAgents, computerInstallCommand, createAgent, type Agent, type Computer, type Member } from "../../api/client";
import { DRIVER_CHOICES, driverInput } from "../../driver";
import { DialogFrame } from "../DialogFrame";

/** Starter roles so a first Agent takes one click instead of a blank form. */
const AGENT_TEMPLATES = [
  {
    id: "engineer",
    name: "Ada",
    label: "Engineer",
    role: "Software engineer. Reads the codebase, makes focused changes, runs the tests and reports what changed and why.",
  },
  {
    id: "writer",
    name: "Iris",
    label: "Writer",
    role: "Writer. Drafts docs, release notes and product copy, keeps the tone plain and asks before publishing anything.",
  },
  {
    id: "researcher",
    name: "Nora",
    label: "Researcher",
    role: "Researcher. Investigates questions, compares options and summarises findings with sources and a recommendation.",
  },
] as const;

const DISMISS_KEY = (spaceId: string) => `sumi.setup.${spaceId}.dismissed`;

function readDismissed(spaceId: string): boolean {
  try {
    return window.localStorage?.getItem(DISMISS_KEY(spaceId)) === "1";
  } catch {
    return false;
  }
}

/**
 * The first-run guide in #general: connect a Computer, create an Agent, then
 * mention it. Each step is done in place, so a new Space never sends the
 * person off to another page to finish setup.
 */
export function GettingStarted({
  spaceId,
  channelId,
  computers,
  agents,
  channelMembers,
  agentHasSpoken,
  canPairComputer,
  canCreateAgent,
  isOwner,
  onMention,
}: {
  spaceId: string;
  channelId: string;
  computers: Computer[];
  agents: Agent[];
  channelMembers: Member[];
  agentHasSpoken: boolean;
  canPairComputer: boolean;
  canCreateAgent: boolean;
  isOwner: boolean;
  onMention: (name: string) => void;
}) {
  const queryClient = useQueryClient();
  const [dismissed, setDismissed] = useState(() => readDismissed(spaceId));
  const [agentDialogOpen, setAgentDialogOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const command = computerInstallCommand();

  const onlineComputers = computers.filter((computer) => computer.status === "online");
  const activeAgents = agents.filter((agent) => agent.desired_lifecycle !== "retired");
  const hasComputer = computers.length > 0;
  const hasOnlineComputer = onlineComputers.length > 0;
  const firstAgent = activeAgents[0];
  const hasAgent = Boolean(firstAgent);

  const mention = useMutation({
    mutationFn: async (agent: Agent) => {
      if (!channelMembers.some((member) => member.id === agent.member_id)) {
        const result = await addChannelAgents(channelId, [agent.member_id]);
        queryClient.setQueryData(["channel-members", channelId], result);
      }
      return agent;
    },
    onSuccess: (agent) => onMention(agent.name),
  });

  if (dismissed || (hasComputer && hasAgent && agentHasSpoken)) return null;

  const current = !hasOnlineComputer ? 1 : !hasAgent ? 2 : 3;

  async function copyCommand() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  }

  function dismiss() {
    try {
      window.localStorage?.setItem(DISMISS_KEY(spaceId), "1");
    } catch {
      // Dismissal still applies for this visit.
    }
    setDismissed(true);
  }

  const offlineComputer = hasComputer && !hasOnlineComputer ? computers[0] : undefined;

  return (
    <section className="getting-started" aria-labelledby="getting-started-title">
      <header className="getting-started-heading">
        <div>
          <h2 id="getting-started-title">Finish your Space setup</h2>
          <p>Three steps to your first Agent reply. Everything happens right here.</p>
        </div>
        <button className="icon-button" type="button" aria-label="Hide setup guide" title="Hide setup guide" onClick={dismiss}>
          <X />
        </button>
      </header>
      <ol className="getting-started-steps">
        <li className={stepClass(1, current)}>
          <StepMarker step={1} current={current} />
          <div className="getting-started-step-body">
            <strong>Connect a Computer</strong>
            {hasOnlineComputer ? (
              <p>{onlineComputers[0].name} is online.</p>
            ) : canPairComputer ? (
              <>
                <p>
                  Run this on the macOS or Linux machine that will host your Agents, open the link it prints,
                  then confirm.
                </p>
                <div className="pair-command">
                  <code>{command}</code>
                  <button className="compact-action" type="button" aria-label="Copy Computer command" onClick={() => void copyCommand()}>
                    {copied ? <Check /> : <Copy />}
                    {copied ? "Copied" : "Copy"}
                  </button>
                </div>
                <p className="getting-started-wait" role="status">
                  <span className="getting-started-pulse" aria-hidden="true" />
                  {offlineComputer
                    ? `${offlineComputer.name} is paired but offline. Start the command again on it.`
                    : "Waiting for your Computer to connect…"}
                </p>
              </>
            ) : (
              <p>Ask a Space Owner or Admin to pair a Computer.</p>
            )}
          </div>
        </li>
        <li className={stepClass(2, current)}>
          <StepMarker step={2} current={current} />
          <div className="getting-started-step-body">
            <strong>Create your first Agent</strong>
            {hasAgent ? (
              <p>{firstAgent.name} is ready.</p>
            ) : current === 2 && canCreateAgent ? (
              <>
                <p>Pick a starting role. You can rename it or rewrite the role later.</p>
                <button className="command-button command-button--accent" type="button" onClick={() => setAgentDialogOpen(true)}>
                  Create Agent
                </button>
              </>
            ) : (
              <p>{canCreateAgent ? "Available once a Computer is online." : "Ask a Space Owner or Admin to create one."}</p>
            )}
          </div>
        </li>
        <li className={stepClass(3, current)}>
          <StepMarker step={3} current={current} />
          <div className="getting-started-step-body">
            <strong>Give it something to do</strong>
            {current === 3 && firstAgent ? (
              <>
                <p>Mention {firstAgent.name} in a message. It replies here, and you see when it is working.</p>
                <button
                  className="command-button"
                  type="button"
                  disabled={mention.isPending}
                  onClick={() => mention.mutate(firstAgent)}
                >
                  {mention.isPending ? <LoaderCircle className="spin" aria-hidden="true" /> : null}
                  Mention {firstAgent.name}
                </button>
                {mention.error ? <p className="form-error" role="alert">{mention.error.message}</p> : null}
              </>
            ) : (
              <p>Mention the Agent with @ in #general.</p>
            )}
          </div>
        </li>
      </ol>
      {agentDialogOpen ? (
        <QuickAgentDialog
          spaceId={spaceId}
          computers={onlineComputers}
          isOwner={isOwner}
          close={() => setAgentDialogOpen(false)}
        />
      ) : null}
    </section>
  );
}

function stepClass(step: number, current: number): string {
  if (step < current) return "is-complete";
  if (step === current) return "is-current";
  return "is-upcoming";
}

function StepMarker({ step, current }: { step: number; current: number }) {
  return (
    <span className="getting-started-marker" aria-hidden="true">
      {step < current ? <Check /> : step}
    </span>
  );
}

function QuickAgentDialog({
  spaceId,
  computers,
  isOwner,
  close,
}: {
  spaceId: string;
  computers: Computer[];
  isOwner: boolean;
  close: () => void;
}) {
  const queryClient = useQueryClient();
  const [templateId, setTemplateId] = useState<string>(AGENT_TEMPLATES[0].id);
  const template = AGENT_TEMPLATES.find((candidate) => candidate.id === templateId) ?? AGENT_TEMPLATES[0];
  const [name, setName] = useState<string>(template.name);
  const [role, setRole] = useState<string>(template.role);
  const creation = useMutation({
    mutationFn: (input: Parameters<typeof createAgent>[1]) => createAgent(spaceId, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["agents", spaceId] });
      void queryClient.invalidateQueries({ queryKey: ["members", spaceId] });
      close();
    },
  });

  function pick(id: string) {
    const next = AGENT_TEMPLATES.find((candidate) => candidate.id === id);
    if (!next) return;
    setTemplateId(id);
    setName(next.name);
    setRole(next.role);
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    creation.mutate({
      computer_id: String(form.get("computer_id") ?? computers[0]?.id ?? ""),
      name: name.trim(),
      role_text: role.trim(),
      access_level: String(form.get("access_level") ?? "member") as "member" | "admin",
      ...driverInput(String(form.get("driver") ?? "codex")),
    });
  }

  return (
    <DialogFrame className="agent-dialog quick-agent-dialog" close={close} labelId="quick-agent-title">
      <header>
        <div><p className="section-kicker">First Agent</p><h2 id="quick-agent-title">Create Agent</h2></div>
        <button className="icon-button" type="button" aria-label="Close Create Agent" onClick={close}><X /></button>
      </header>
      <form className="quick-agent-form" onSubmit={submit}>
        <fieldset className="quick-agent-templates">
          <legend>Starting role</legend>
          {AGENT_TEMPLATES.map((candidate) => (
            <label key={candidate.id} className={candidate.id === templateId ? "is-selected" : undefined}>
              <input type="radio" name="template" value={candidate.id} checked={candidate.id === templateId} onChange={() => pick(candidate.id)} />
              {candidate.label}
            </label>
          ))}
        </fieldset>
        <label className="agent-field">Name<input aria-label="Agent name" required maxLength={40} value={name} onChange={(event) => setName(event.target.value)} data-dialog-initial-focus /></label>
        <label className="agent-field agent-field--wide">Role<textarea aria-label="Role" required maxLength={12000} rows={3} value={role} onChange={(event) => setRole(event.target.value)} /></label>
        <details className="quick-agent-advanced">
          <summary>Computer, driver and access</summary>
          <div>
            <label className="agent-field">Computer<select name="computer_id" defaultValue={computers[0]?.id}>{computers.map((computer) => <option key={computer.id} value={computer.id}>{computer.name}</option>)}</select></label>
            <label className="agent-field">Driver<select name="driver" defaultValue="codex">{DRIVER_CHOICES.map((choice) => <option key={choice.value} value={choice.value}>{choice.label}</option>)}</select></label>
            <label className="agent-field">Access level<select name="access_level" defaultValue="member"><option value="member">Member</option>{isOwner ? <option value="admin">Admin</option> : null}</select></label>
          </div>
        </details>
        {creation.error ? <p className="form-error" role="alert">{creation.error.message}</p> : null}
        <footer>
          <button className="command-button" type="button" onClick={close}>Cancel</button>
          <button className="command-button command-button--accent" type="submit" disabled={creation.isPending || computers.length === 0}>
            {creation.isPending ? "Creating…" : `Create ${name.trim() || "Agent"}`}
          </button>
        </footer>
      </form>
    </DialogFrame>
  );
}
