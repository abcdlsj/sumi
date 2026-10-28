import { createContext, useContext } from "react";

/** The viewing Member, so Messages can mark what is waiting for them. */
export const SelfMemberContext = createContext<string | undefined>(undefined);

export function useSelfMemberId(): string | undefined {
  return useContext(SelfMemberContext);
}
