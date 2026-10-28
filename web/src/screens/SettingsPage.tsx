import { useLocation, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

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
      {() => <SettingsWorkspace selectedFeatureId={selectedFeatureId} />}
    </SpaceShell>
  );
}

export function SettingsWorkspace({ selectedFeatureId }: { selectedFeatureId?: string }) {
  useEffect(() => {
    if (!selectedFeatureId) return;
    document.getElementById(`feature-${selectedFeatureId}`)?.scrollIntoView?.({ block: "nearest" });
  }, [selectedFeatureId]);
  return (
    <div className="settings-page">
      <header className="settings-header">
        <h1>Settings</h1>
        <p>Preferences for this browser.</p>
      </header>
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
