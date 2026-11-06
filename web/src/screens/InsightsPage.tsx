import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import { getSpaceBySlug } from "../api/client";
import { InsightsTabs } from "../components/InsightsNavigation";
import { SpacePage } from "../components/SpaceShell";
import { AGENT_INSIGHTS_FEATURE_ID, useFeatureEnabled } from "../featureRegistry";
import { AgentGraphWorkspace } from "./AgentGraphPage";
import { PanelSkeleton } from "../components/Skeleton";

export function InsightsPage() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/insights" });
  const enabled = useFeatureEnabled(AGENT_INSIGHTS_FEATURE_ID);
  if (!enabled) {
    return (
      <SpacePage>
        {() => (
          <div className="route-status">
            <section className="route-status-panel">
              <h1>Agent insights is disabled.</h1>
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
        )}
      </SpacePage>
    );
  }
  return (
    <SpacePage>
      {() => (
        <div className="insights-page">
          <InsightsTabs spaceSlug={spaceSlug} />
          <Outlet />
        </div>
      )}
    </SpacePage>
  );
}

export function AgentGraphRouteContent() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/insights/graph" });
  const space = useQuery({
    queryKey: ["space", spaceSlug],
    queryFn: () => getSpaceBySlug(spaceSlug),
  });
  if (space.isPending) return <PanelSkeleton label="Opening Agent graph" />;
  if (!space.data) {
    return (
      <div className="route-status route-status--error">
        <p>Space is unavailable.</p>
      </div>
    );
  }
  return <AgentGraphWorkspace spaceId={space.data.id} />;
}
