import { Outlet, useParams } from "@tanstack/react-router";

import { SpaceShell } from "./components/SpaceShell";

export function SpaceLayout() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug" });
  // A different Space is a different Shell: its stream, caches and unread marks start fresh.
  return (
    <SpaceShell key={spaceSlug} spaceSlug={spaceSlug}>
      <Outlet />
    </SpaceShell>
  );
}
