import type { Agent, CreateAgentInput } from "./api/types";

/** Driver choices offered when creating an Agent; ACP providers run through the ACP Driver. */
export const DRIVER_CHOICES = [
  { value: "codex", label: "Codex (ACP)" },
  { value: "claude", label: "Claude (ACP)" },
  { value: "pi", label: "Pi (ACP)" },
  { value: "builtin", label: "Builtin" },
] as const;

export type DriverChoice = (typeof DRIVER_CHOICES)[number]["value"];

export function driverInput(choice: string): Pick<CreateAgentInput, "driver_kind" | "driver_provider"> {
  if (choice === "builtin") return { driver_kind: "builtin", driver_provider: null };
  if (choice === "codex" || choice === "claude" || choice === "pi") return { driver_kind: "acp", driver_provider: choice };
  throw new Error(`Unknown Driver choice: ${choice}`);
}

export function driverLabel(agent: Pick<Agent, "driver_kind" | "driver_provider">): string {
  if (agent.driver_kind === "builtin") return "Builtin";
  return DRIVER_CHOICES.find((choice) => choice.value === agent.driver_provider)?.label ?? "ACP";
}
