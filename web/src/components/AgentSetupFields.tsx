import { useState } from "react";

import type { Computer } from "../api/types";
import { DRIVER_CHOICES, type DriverChoice } from "../driver";
import { SumiSelect } from "./SumiSelect";

/** Computer, Driver and Access for a new Agent.
 *
 * The values live in hidden inputs so the dialog's form still submits them by name, and every
 * control is a SumiSelect: DESIGN forbids the browser's native dropdowns.
 */
export function AgentSetupFields({
  computers,
  isOwner,
  defaultComputerId,
  collapsible = false,
}: {
  computers: Computer[];
  isOwner: boolean;
  defaultComputerId?: string;
  /** Hide the fields behind a summary, as the onboarding dialog does. */
  collapsible?: boolean;
}) {
  const [selectedComputerId, setSelectedComputerId] = useState(defaultComputerId ?? "");
  const [driver, setDriver] = useState<DriverChoice>("codex");
  const [accessLevel, setAccessLevel] = useState<"member" | "admin">("member");

  // Computers can come online while the dialog is open, so the effective id follows the current list:
  // the chosen Computer while it is still there, otherwise the first one available now.
  const computerId = computers.some((computer) => computer.id === selectedComputerId)
    ? selectedComputerId
    : (computers[0]?.id ?? "");

  const fields = (
    <div className="agent-setup-fields">
      <input type="hidden" name="computer_id" value={computerId} />
      <input type="hidden" name="driver" value={driver} />
      <input type="hidden" name="access_level" value={accessLevel} />
      <label className="agent-field agent-field--wide">
        Computer
        <SumiSelect
          ariaLabel="Computer"
          value={computerId}
          onChange={setSelectedComputerId}
          options={computers.map((computer) => ({
            value: computer.id,
            label: `${computer.name} · ${computer.hostname}`,
          }))}
        />
      </label>
      <label className="agent-field">
        Driver
        <SumiSelect
          ariaLabel="Driver"
          value={driver}
          onChange={(value) => setDriver(value as DriverChoice)}
          options={DRIVER_CHOICES.map((choice) => ({ value: choice.value, label: choice.label }))}
        />
      </label>
      <label className="agent-field">
        Access level
        <SumiSelect
          ariaLabel="Access level"
          value={accessLevel}
          onChange={(value) => setAccessLevel(value as "member" | "admin")}
          options={[
            { value: "member", label: "Member" },
            ...(isOwner ? [{ value: "admin", label: "Admin" }] : []),
          ]}
        />
      </label>
    </div>
  );

  if (!collapsible) return fields;
  return (
    <details className="quick-agent-advanced" open={false}>
      <summary>Computer, driver and access</summary>
      {fields}
    </details>
  );
}
