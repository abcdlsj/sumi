import {
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  type RouterHistory,
} from "@tanstack/react-router";

import { RouteChrome } from "./RouteChrome";
import { SpaceLayout } from "./SpaceLayout";
import { loadLastConversationRoute } from "./conversationMemory";
import { ChannelPage } from "./screens/ChannelPage";
import { CompanyOfficePage } from "./screens/CompanyPage";
import { AgentGraphRouteContent, InsightsPage } from "./screens/InsightsPage";
import { AgentStatisticsPage } from "./screens/AgentStatisticsPage";
import { AgentDetailPage } from "./screens/AgentDetailPage";
import { ComputersPage } from "./screens/ComputersPage";
import { InvitationPage } from "./screens/InvitationPage";
import { InboxPage } from "./screens/InboxPage";
import { DirectMessagePage } from "./screens/DirectMessagePage";
import { LoginPage } from "./screens/LoginPage";
import { MembersPage } from "./screens/MembersPage";
import { RegisterPage } from "./screens/RegisterPage";
import { PairComputerPage } from "./screens/PairComputerPage";
import { SettingsPage } from "./screens/SettingsPage";
import { SpaceCreatePage } from "./screens/SpaceCreatePage";
import { TaskDetailPage, TasksPage } from "./screens/TasksPage";
import { NotFoundPage, RouteErrorPage } from "./components/RoutePages";

const rootRoute = createRootRoute({ component: RouteChrome });
const registerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  validateSearch: (search: Record<string, unknown>) => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  component: RegisterPage,
});
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (search: Record<string, unknown>) => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  component: LoginPage,
});
const createSpaceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/spaces/new",
  component: SpaceCreatePage,
});
/** Every page of a Space renders inside this one layout, so the Shell and its stream stay mounted. */
const spaceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/s/$spaceSlug",
  component: SpaceLayout,
});
const spaceEntryRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "/",
  beforeLoad: ({ params }) => {
    const saved = loadLastConversationRoute(params.spaceSlug);
    if (saved?.kind === "dm") {
      throw redirect({
        to: "/s/$spaceSlug/dm/$memberId",
        params: { spaceSlug: params.spaceSlug, memberId: saved.memberId },
      });
    }
    const channelSlug = saved && saved.kind === "channel" ? saved.channelSlug : "general";
    throw redirect({
      to: "/s/$spaceSlug/channels/$channelSlug",
      params: { spaceSlug: params.spaceSlug, channelSlug },
    });
  },
});
const channelRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "channels/$channelSlug",
  component: ChannelPage,
});
const companyRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "company",
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/s/$spaceSlug/company/office",
      params: { spaceSlug: params.spaceSlug },
    });
  },
});
const companyOfficeRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "company/office",
  component: CompanyOfficePage,
});
const membersRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "members",
  component: MembersPage,
});
const insightsRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "insights",
  component: InsightsPage,
});
const insightsIndexRoute = createRoute({
  getParentRoute: () => insightsRoute,
  path: "/",
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/s/$spaceSlug/insights/stats",
      params: { spaceSlug: params.spaceSlug },
    });
  },
});
const insightsStatsRoute = createRoute({
  getParentRoute: () => insightsRoute,
  path: "stats",
  component: AgentStatisticsPage,
});
const insightsGraphRoute = createRoute({
  getParentRoute: () => insightsRoute,
  path: "graph",
  component: AgentGraphRouteContent,
});
const settingsRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "settings",
  component: SettingsPage,
});
const agentDetailRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "agents/$agentId",
  component: AgentDetailPage,
});
const inboxRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "inbox",
  component: InboxPage,
});
const tasksRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "tasks",
  component: TasksPage,
});
const taskDetailRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "tasks/$taskId",
  component: TaskDetailPage,
});
const computersRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "computers",
  component: ComputersPage,
});
const pairComputerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/pair-computer/$pairingId",
  validateSearch: (search: Record<string, unknown>) => ({
    code: typeof search.code === "string" ? search.code : typeof search.code === "number" ? String(search.code) : "",
  }),
  component: PairComputerPage,
});
const invitationRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/invite/$inviteToken",
  component: InvitationPage,
});
const directMessageRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "dm/$memberId",
  component: DirectMessagePage,
});

const routeTree = rootRoute.addChildren([
  registerRoute,
  loginRoute,
  createSpaceRoute,
  spaceRoute.addChildren([
    spaceEntryRoute,
    channelRoute,
    companyRoute,
    companyOfficeRoute,
    membersRoute,
    insightsRoute.addChildren([insightsIndexRoute, insightsStatsRoute, insightsGraphRoute]),
    settingsRoute,
    agentDetailRoute,
    inboxRoute,
    tasksRoute,
    taskDetailRoute,
    computersRoute,
    directMessageRoute,
  ]),
  pairComputerRoute,
  invitationRoute,
]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    history,
    parseSearch: (searchStr) => Object.fromEntries(new URLSearchParams(searchStr)),
    defaultNotFoundComponent: NotFoundPage,
    defaultErrorComponent: RouteErrorPage,
  });
}

export const router = createAppRouter();

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
