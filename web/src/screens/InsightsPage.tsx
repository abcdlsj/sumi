import { Outlet, useParams } from "@tanstack/react-router";

import { FeatureDisabled } from "../components/FeatureDisabled";
import { InsightsTabs } from "../components/InsightsNavigation";
import { SpacePage } from "../components/SpaceShell";
import { useSpaceShell } from "../components/spaceShellContext";
import { AGENT_INSIGHTS_FEATURE_ID, useFeatureEnabled } from "../featureRegistry";
import { AgentGraphWorkspace } from "./AgentGraphPage";

export function InsightsPage() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/insights" });
  const enabled = useFeatureEnabled(AGENT_INSIGHTS_FEATURE_ID);
  if (!enabled) {
    return (
      <SpacePage>
        {() => <FeatureDisabled featureName="Agent insights" spaceSlug={spaceSlug} />}
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
  return <AgentGraphWorkspace spaceId={useSpaceShell().space.id} />;
}
