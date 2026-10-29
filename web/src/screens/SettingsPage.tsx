import { useLocation, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import type { Member, Space, User } from "../api/client";
import { SpaceShell } from "../components/SpaceShell";
import {
  type FeatureConfigField,
  type RegisteredFeature,
  REGISTERED_FEATURES,
  setFeatureEnabled,
  useFeatureEnabled,
} from "../featureRegistry";
import "./settings.css";

export function SettingsPage() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/settings" });
  const location = useLocation();
  const hash = String(location.hash).replace(/^#/, "");
  const selectedFeatureId = hash.startsWith("feature-")
    ? hash.slice("feature-".length)
    : undefined;
  return (
    <SpaceShell spaceSlug={spaceSlug} active="settings">
      {({ space, user, currentMember }) => <SettingsWorkspace selectedFeatureId={selectedFeatureId} space={space} user={user} currentMember={currentMember} />}
    </SpaceShell>
  );
}

export function SettingsWorkspace({ selectedFeatureId, space, user, currentMember }: { selectedFeatureId?: string; space?: Space; user?: User; currentMember?: Member }) {
  useEffect(() => {
    if (!selectedFeatureId) return;
    document.getElementById(`feature-${selectedFeatureId}`)?.scrollIntoView?.({ block: "nearest" });
  }, [selectedFeatureId]);
  return (
    <div className="settings-page">
      <header className="settings-header">
        <h1>Settings</h1>
        <p>Your account, this Space, and preferences for this browser.</p>
      </header>
      {user || space ? (
        <section className="settings-group" aria-labelledby="settings-account-heading">
          <div className="settings-group-heading">
            <h2 id="settings-account-heading">Account and Space</h2>
          </div>
          <dl className="settings-list settings-facts">
            {user ? <Fact label="Name" value={user.display_name} /> : null}
            {user ? <Fact label="Email" value={user.email} /> : null}
            {space ? <Fact label="Space" value={space.name} /> : null}
            {space ? <Fact label="Address" value={`/s/${space.slug}`} mono /> : null}
            {space ? (
              <div className="settings-fact">
                <dt>Accent</dt>
                <dd><span className="settings-accent" style={{ backgroundColor: space.accent }} aria-hidden="true" /> <span className="settings-mono">{space.accent.toUpperCase()}</span></dd>
              </div>
            ) : null}
            {currentMember ? <Fact label="Your access" value={currentMember.access_level.charAt(0).toUpperCase() + currentMember.access_level.slice(1)} /> : null}
          </dl>
        </section>
      ) : null}
      <section className="settings-group" aria-labelledby="settings-experimental-heading">
        <div className="settings-group-heading">
          <h2 id="settings-experimental-heading">Experimental features</h2>
          <p>Early features you can turn on. Each one adds an entry to the sidebar.</p>
        </div>
        <ul className="settings-list">
          {REGISTERED_FEATURES.map((feature) => (
            <FeatureRow
              key={feature.id}
              feature={feature}
              highlighted={feature.id === selectedFeatureId}
            />
          ))}
        </ul>
      </section>
      <section className="settings-group" aria-labelledby="settings-shortcuts-heading">
        <div className="settings-group-heading">
          <h2 id="settings-shortcuts-heading">Keyboard shortcuts</h2>
        </div>
        <dl className="settings-list settings-facts">
          {SHORTCUTS.map(([keys, action]) => (
            <div className="settings-fact" key={action}>
              <dt>{action}</dt>
              <dd>{keys.map((key) => <kbd key={key}>{key}</kbd>)}</dd>
            </div>
          ))}
        </dl>
      </section>
    </div>
  );
}

const modifier = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘" : "Ctrl";
const SHORTCUTS: Array<[string[], string]> = [
  [[modifier, "K"], "Jump to a channel, DM or Agent"],
  [[modifier, "Enter"], "Send the message"],
  [["@"], "Mention a member or Agent"],
  [["↑", "↓", "Enter"], "Pick a suggestion"],
  [["Esc"], "Close the Thread or dialog"],
];

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="settings-fact">
      <dt>{label}</dt>
      <dd className={mono ? "settings-mono" : undefined}>{value}</dd>
    </div>
  );
}

function FeatureRow({ feature, highlighted }: { feature: RegisteredFeature; highlighted: boolean }) {
  const enabled = useFeatureEnabled(feature.id);
  const labelId = `feature-${feature.id}-label`;
  return (
    <li id={`feature-${feature.id}`} className={`settings-row${highlighted ? " settings-row--highlighted" : ""}`}>
      <div className="settings-row-main">
        <div className="settings-row-copy">
          <strong id={labelId}>{feature.name}</strong>
          <p>{feature.description}</p>
        </div>
        <Switch
          labelledBy={labelId}
          checked={enabled}
          onChange={(value) => setFeatureEnabled(feature.id, value)}
        />
      </div>
      {enabled && feature.fields.length ? (
        <div className="settings-row-fields">
          {feature.fields.map((field) => (
            <FeatureField key={field.id} feature={feature} field={field} />
          ))}
        </div>
      ) : null}
    </li>
  );
}

function Switch({
  checked,
  onChange,
  labelledBy,
  label,
}: {
  checked: boolean;
  onChange: (value: boolean) => void;
  labelledBy?: string;
  label?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      className="settings-switch"
      aria-checked={checked}
      aria-labelledby={labelledBy}
      aria-label={label}
      onClick={() => onChange(!checked)}
    >
      <span aria-hidden="true" />
    </button>
  );
}

function FeatureField({
  feature,
  field,
}: {
  feature: RegisteredFeature;
  field: FeatureConfigField;
}) {
  const storageKey = `${feature.storageKey}.${field.id}`;
  const [checked, setChecked] = useState(
    () => window.localStorage?.getItem(storageKey) === "1",
  );
  if (field.kind !== "toggle") return null;
  return (
    <div className="settings-field">
      <span>{field.label}</span>
      <Switch
        label={field.label}
        checked={checked}
        onChange={(value) => {
          setChecked(value);
          window.localStorage?.setItem(storageKey, value ? "1" : "0");
        }}
      />
    </div>
  );
}
