import { createContext, useContext } from "react";

import type {Channel, DirectMessage, Member, Space, User} from "../api/types";

export interface SpaceShellContext {
  space: Space;
  user: User;
  navigationOpen: boolean;
  channels: Channel[];
  directMessages: DirectMessage[];
  members: Member[];
  currentMember: Member;
  /** Listens to new Messages from the Shell's single event stream; returns the unsubscribe. */
  subscribeMessages: (listener: (event: { type: string; channelId: string }) => void) => () => void;
}

export const SpaceShellValue = createContext<SpaceShellContext | null>(null);

/** The Space the current page renders in. Only pages under the `/s/$spaceSlug` layout call it. */
export function useSpaceShell(): SpaceShellContext {
  const value = useContext(SpaceShellValue);
  if (!value) throw new Error("useSpaceShell is used outside the Space layout");
  return value;
}

