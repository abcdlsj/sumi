import { Link } from "@tanstack/react-router";

/** One disabled-experiment page: what is off, why, and the way to turn it on. */
export function FeatureDisabled({ featureName, spaceSlug }: { featureName: string; spaceSlug: string }) {
  return (
    <div className="route-status">
      <section className="route-status-panel">
        <h1>{featureName} is disabled.</h1>
        <p>This experimental feature is off. Turn it on in Settings to add it to the sidebar.</p>
        <Link
          className="command-button command-button--accent"
          to="/s/$spaceSlug/settings"
          params={{ spaceSlug }}
        >
          Open Settings
        </Link>
      </section>
    </div>
  );
}
