import { useParams } from "@tanstack/react-router";

import { FeatureDisabled } from "../components/FeatureDisabled";
import { SpacePage } from "../components/SpaceShell";
import { COMPANY_OFFICE_FEATURE_ID, useFeatureEnabled } from "../featureRegistry";
import { CompanyOfficeView } from "./company/OfficeView";

export function CompanyOfficePage() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/company/office" });
  const enabled = useFeatureEnabled(COMPANY_OFFICE_FEATURE_ID);
  if (!enabled) {
    return (
      <SpacePage>
        {() => <FeatureDisabled featureName="Company office" spaceSlug={spaceSlug} />}
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
