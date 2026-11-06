import { Link, useParams } from "@tanstack/react-router";

import { SpacePage } from "../components/SpaceShell";
import { COMPANY_OFFICE_FEATURE_ID, useFeatureEnabled } from "../featureRegistry";
import { CompanyOfficeView } from "./company/OfficeView";

export function CompanyOfficePage() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/company/office" });
  const enabled = useFeatureEnabled(COMPANY_OFFICE_FEATURE_ID);
  if (!enabled) {
    return (
      <SpacePage>
        {() => (
          <div className="route-status">
            <section className="route-status-panel">
              <h1>Company office is disabled.</h1>
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
      {({ space, channels, directMessages, members, currentMember, subscribeMessages }) => (
        <CompanyOfficeView
          spaceSlug={space.slug}
          spaceId={space.id}
          channels={channels}
          directMessages={directMessages}
          members={members}
          currentMember={currentMember}
          subscribeMessages={subscribeMessages}
        />
      )}
    </SpacePage>
  );
}
