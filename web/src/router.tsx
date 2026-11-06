import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  redirect,
  type RouterHistory,
} from "@tanstack/react-router";

import { RouteChrome } from "./RouteChrome";
import { SpaceLayout } from "./SpaceLayout";
import { loadLastConversationRoute } from "./conversationMemory";
import { ChannelPage } from "./screens/ChannelPage";
import { DirectMessagePage } from "./screens/DirectMessagePage";
import { LoginPage } from "./screens/LoginPage";
import { RegisterPage } from "./screens/RegisterPage";
import { SpaceCreatePage } from "./screens/SpaceCreatePage";
import { NotFoundPage, RouteErrorPage } from "./components/RoutePages";
import { OnboardingSkeleton, PageSkeleton, PanelSkeleton } from "./components/Skeleton";

// Pages beyond the conversation load on first visit, so opening a Channel downloads only what it renders.
// Each one shows its own skeleton inside the Space Shell while its code downloads.
const CompanyOfficePage = lazyRouteComponent(() => import("./screens/CompanyPage"), "CompanyOfficePage");
const AgentGraphRouteContent = lazyRouteComponent(() => import("./screens/InsightsPage"), "AgentGraphRouteContent");
const InsightsPage = lazyRouteComponent(() => import("./screens/InsightsPage"), "InsightsPage");
const AgentStatisticsPage = lazyRouteComponent(() => import("./screens/AgentStatisticsPage"), "AgentStatisticsPage");
const AgentDetailPage = lazyRouteComponent(() => import("./screens/AgentDetailPage"), "AgentDetailPage");
const ComputersPage = lazyRouteComponent(() => import("./screens/ComputersPage"), "ComputersPage");
const InvitationPage = lazyRouteComponent(() => import("./screens/InvitationPage"), "InvitationPage");
const InboxPage = lazyRouteComponent(() => import("./screens/InboxPage"), "InboxPage");
const MembersPage = lazyRouteComponent(() => import("./screens/MembersPage"), "MembersPage");
const PairComputerPage = lazyRouteComponent(() => import("./screens/PairComputerPage"), "PairComputerPage");
const SettingsPage = lazyRouteComponent(() => import("./screens/SettingsPage"), "SettingsPage");
const TaskDetailPage = lazyRouteComponent(() => import("./screens/TasksPage"), "TaskDetailPage");
const TasksPage = lazyRouteComponent(() => import("./screens/TasksPage"), "TasksPage");

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
  pendingComponent: () => <PageSkeleton label="Opening Company" body="panel" />,
});
const membersRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "members",
  component: MembersPage,
  pendingComponent: () => <PageSkeleton label="Opening Members" body="list" />,
});
const insightsRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "insights",
  component: InsightsPage,
  pendingComponent: () => <PageSkeleton label="Opening Agent insights" body="panel" />,
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
  pendingComponent: () => <PanelSkeleton label="Opening Agent statistics" />,
});
const insightsGraphRoute = createRoute({
  getParentRoute: () => insightsRoute,
  path: "graph",
  component: AgentGraphRouteContent,
  pendingComponent: () => <PanelSkeleton label="Opening Agent graph" />,
});
const settingsRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "settings",
  component: SettingsPage,
  pendingComponent: () => <PageSkeleton label="Opening Settings" body="detail" />,
});
const agentDetailRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "agents/$agentId",
  component: AgentDetailPage,
  pendingComponent: () => <PageSkeleton label="Opening Agent" body="detail" />,
});
const inboxRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "inbox",
  component: InboxPage,
  pendingComponent: () => <PageSkeleton label="Opening Inbox" body="list" />,
});
const tasksRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "tasks",
  component: TasksPage,
  pendingComponent: () => <PageSkeleton label="Opening Tasks" body="list" />,
});
const taskDetailRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "tasks/$taskId",
  component: TaskDetailPage,
  pendingComponent: () => <PageSkeleton label="Opening Task" body="detail" />,
});
const computersRoute = createRoute({
  getParentRoute: () => spaceRoute,
  path: "computers",
  component: ComputersPage,
  pendingComponent: () => <PageSkeleton label="Opening Computers" body="detail" />,
});
const pairComputerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/pair-computer/$pairingId",
  validateSearch: (search: Record<string, unknown>) => ({
    code: typeof search.code === "string" ? search.code : typeof search.code === "number" ? String(search.code) : "",
  }),
  component: PairComputerPage,
  pendingComponent: () => <OnboardingSkeleton label="Opening Computer pairing" />,
});
const invitationRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/invite/$inviteToken",
  component: InvitationPage,
  pendingComponent: () => <OnboardingSkeleton label="Opening invitation" />,
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
