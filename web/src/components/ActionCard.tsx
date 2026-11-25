import { Bot, CheckCircle2, Sparkles, X } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import {
  api,
  type ActionCardPayload,
  type AgentCreateCardParams,
  type AgentCreateCardResult,
  type AgentCreateProposal,
  type Message,
} from "../api";
import { Badge, Button, useAction } from "./ui";

/** Parses a generic action_card message payload; returns null for malformed or non-card payloads. */
export function parseActionCard(raw?: string): ActionCardPayload | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Partial<ActionCardPayload>;
    if (!parsed || typeof parsed !== "object" || typeof parsed.card_type !== "string" || typeof parsed.status !== "string") {
      return null;
    }
    return {
      version: typeof parsed.version === "number" ? parsed.version : 1,
      card_type: parsed.card_type,
      status: parsed.status,
      title: typeof parsed.title === "string" && parsed.title ? parsed.title : parsed.card_type,
      description: typeof parsed.description === "string" ? parsed.description : undefined,
      params: parsed.params,
      result: parsed.result,
      executed_by_member_id: parsed.executed_by_member_id,
      executed_by_name: parsed.executed_by_name,
      executed_at: parsed.executed_at,
    };
  } catch {
    return null;
  }
}

export function isActionCardMessage(m: Pick<Message, "kind" | "action_kind">): boolean {
  return m.kind === "action" && m.action_kind === "action_card";
}

function driverSummary(params: AgentCreateCardParams): string {
  if (params.driver_kind === "acp") {
    const p = params.driver_config?.provider;
    return p ? `ACP · ${p}` : "ACP";
  }
  if (params.driver_kind === "builtin") {
    return params.driver_config?.model ? `Model API · ${params.driver_config.model}` : "Model API";
  }
  return "";
}

/**
 * Lightweight interactive Action Card rendered inline in a conversation.
 * Designed around a generic payload envelope so future card types plug into the same frame.
 */
export function ActionCard({
  spaceSlug,
  message,
  canGovern,
  onReviewAgentProposal,
  onInspectAgent,
}: {
  spaceSlug: string;
  message: Message;
  canGovern: boolean;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
  onInspectAgent?: (memberID: string) => void;
}) {
  const qc = useQueryClient();
  const dismissAct = useAction();
  const card = parseActionCard(message.payload_json);
  if (!card) return null;

  const isPending = card.status === "pending";
  const isExecuted = card.status === "executed";
  const isDismissed = card.status === "dismissed";

  async function handleDismiss() {
    const ok = await dismissAct.run(() => api.dismissActionCard(spaceSlug, message.id));
    if (ok) {
      void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, message.conversation_id] });
    }
  }

  if (card.card_type === "agent.create") {
    const params = (card.params ?? {}) as AgentCreateCardParams;
    const result = (card.result ?? {}) as Partial<AgentCreateCardResult>;
    const drv = driverSummary(params);
    return (
      <div
        data-testid="action-card"
        data-card-type={card.card_type}
        data-card-status={card.status}
        className="mt-1.5 rounded-[6px] border border-line-strong bg-card max-w-[440px] overflow-hidden"
      >
        <div className="px-3.5 py-2.5 border-b border-line flex items-center justify-between gap-2 bg-subtle/35">
          <div className="flex items-center gap-2 min-w-0">
            <span className="size-6 rounded-[5px] bg-subtle border border-line text-fg-2 flex items-center justify-center shrink-0">
              <Bot className="size-3.5" />
            </span>
            <span className="text-[13.5px] font-semibold text-fg truncate">{card.title || "Create agent"}</span>
          </div>
          {isPending && <Badge tone="warm">Pending</Badge>}
          {isExecuted && <Badge tone="online">Created</Badge>}
          {isDismissed && <Badge tone="neutral">Dismissed</Badge>}
        </div>

        <div className="px-3.5 py-3 space-y-1.5 text-[13px]">
          <div className="flex items-baseline gap-2">
            <span className="text-fg-3 w-12 shrink-0">Name</span>
            <span className="font-medium text-fg truncate">{params.display_name || "Auto-assigned"}</span>
          </div>
          {params.role_text && (
            <div className="flex items-baseline gap-2">
              <span className="text-fg-3 w-12 shrink-0">Role</span>
              <span className="text-fg-2 line-clamp-2 leading-snug">{params.role_text}</span>
            </div>
          )}
          {drv && (
            <div className="flex items-baseline gap-2">
              <span className="text-fg-3 w-12 shrink-0">Driver</span>
              <span className="font-mono text-[12px] text-fg-2 truncate">{drv}</span>
            </div>
          )}
        </div>

        <div className="px-3.5 py-2.5 border-t border-line bg-subtle/20 flex items-center justify-between gap-2">
          {isPending ? (
            canGovern && onReviewAgentProposal ? (
              <>
                <Button
                  type="button"
                  size="sm"
                  onClick={() =>
                    onReviewAgentProposal({
                      message_id: message.id,
                      conversation_id: message.conversation_id,
                      proposed_by_name: message.author_display_name,
                      params,
                    })
                  }
                >
                  Review &amp; create
                </Button>
                <Button type="button" size="sm" variant="ghost" loading={dismissAct.busy} icon={<X />} onClick={() => void handleDismiss()}>
                  Dismiss
                </Button>
              </>
            ) : (
              <span className="text-[12px] text-fg-3">Requires an admin or owner to confirm.</span>
            )
          ) : isExecuted ? (
            <div className="flex items-center gap-1.5 text-[12.5px] text-fg-2 min-w-0">
              <CheckCircle2 className="size-3.5 text-online shrink-0" />
              <span className="truncate">
                Created{" "}
                {result.agent_id && onInspectAgent ? (
                  <button
                    type="button"
                    onClick={() => onInspectAgent(result.agent_id!)}
                    className="font-semibold text-fg hover:underline underline-offset-2"
                  >
                    @{result.display_name || params.display_name || "agent"}
                  </button>
                ) : (
                  <span className="font-semibold text-fg">@{result.display_name || params.display_name || "agent"}</span>
                )}
                {card.executed_by_name ? <span className="text-fg-3"> · confirmed by {card.executed_by_name}</span> : null}
              </span>
            </div>
          ) : (
            <span className="text-[12.5px] text-fg-3">
              Dismissed{card.executed_by_name ? ` by ${card.executed_by_name}` : ""}
            </span>
          )}
        </div>
      </div>
    );
  }

  // Generic fallback for future Action Card types.
  return (
    <div
      data-testid="action-card"
      data-card-type={card.card_type}
      data-card-status={card.status}
      className="mt-1.5 rounded-[8px] border border-line bg-card max-w-[440px] px-3.5 py-2.5 flex items-center justify-between gap-2"
    >
      <div className="flex items-center gap-2 min-w-0">
        <Sparkles className="size-3.5 text-fg-3 shrink-0" />
        <div className="min-w-0">
          <div className="text-[13.5px] font-medium text-fg truncate">{card.title}</div>
          {card.description && <div className="text-[12.5px] text-fg-3 truncate">{card.description}</div>}
        </div>
      </div>
      <Badge tone={isPending ? "warm" : isExecuted ? "online" : "neutral"}>{card.status}</Badge>
    </div>
  );
}
