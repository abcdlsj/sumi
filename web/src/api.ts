export type MemberKind = "human" | "agent";
export type AccessLevel = "owner" | "admin" | "member";
export type Permission = "agent.create" | "channel.create" | "channel.manage_members";
export type DriverKind = "acp" | "builtin";
export type RuntimeState = "idle" | "working" | "offline" | "error";
export type ConversationKind = "public" | "private" | "dm" | "thread" | "task";
export type TaskStatus = "todo" | "in_progress" | "in_review" | "done" | "closed";
export type NotifyLevel = "all" | "mentions" | "mute";

export interface User {
  id: string;
  email: string;
  display_name: string;
  created_at: string;
}

export interface Space {
  id: string;
  slug: string;
  name: string;
  owner_member_id: string;
  event_seq: number;
  created_at: string;
}

export type ACPProvider = "pi" | "codex" | "claude";

/** Browser-facing driver config; the API key is never returned, only whether one is set. */
export interface DriverConfigView {
  base_url?: string;
  model?: string;
  api_key_set?: boolean;
  provider?: ACPProvider;
  command?: string[];
}

export type DriverConfigInput =
  | { base_url: string; model: string; api_key?: string }
  | { provider: ACPProvider; command?: string[] };

export interface AgentInfo {
  computer_id?: string;
  role_text: string;
  driver_kind: DriverKind;
  driver_config?: DriverConfigView;
  runtime_state: RuntimeState;
  focus_conversation_id?: string;
  last_error_code?: string;
  last_error_detail?: string;
  runtime_state_updated_at?: string;
}

export interface Member {
  id: string;
  space_id: string;
  kind: MemberKind;
  display_name: string;
  access_level: AccessLevel;
  permissions?: Permission[];
  agent?: AgentInfo;
  created_at: string;
}

export interface Computer {
  id: string;
  space_id: string;
  name: string;
  hostname: string;
  os: string;
  connection_status: "online" | "offline";
  daemon_version?: string;
  acp_providers?: ACPProvider[];
  last_seen_at?: string;
  created_at: string;
}

export interface Conversation {
  id: string;
  space_id: string;
  kind: ConversationKind;
  slug?: string;
  topic?: string;
  parent_conversation_id?: string;
  root_message_id?: string;
  task_id?: string;
  next_seq: number;
  read_seq: number;
  unread_count: number;
  mention_count: number;
  notify_level?: NotifyLevel;
  following: boolean;
  peer_member_id?: string;
  peer_display_name?: string;
  peer_kind?: MemberKind;
  peer_runtime_state?: RuntimeState;
}

export interface Task {
  id: string;
  space_id: string;
  number: number;
  channel_id: string;
  channel_slug?: string;
  source_message_id?: string;
  conversation_id?: string;
  title: string;
  description: string;
  status: TaskStatus;
  creator_member_id: string;
  assignee_member_id?: string;
  assignee_display_name?: string;
  assignee_kind?: MemberKind;
  revision: number;
  message_count: number;
  created_at: string;
  updated_at: string;
}

export interface Attachment {
  id: string;
  space_id: string;
  uploader_member_id: string;
  name: string;
  media_type: string;
  size_bytes: number;
  message_id?: string;
  created_at: string;
}

export interface Message {
  id: string;
  space_id: string;
  conversation_id: string;
  seq: number;
  author_member_id: string;
  author_display_name?: string;
  author_kind?: MemberKind;
  kind: "text" | "action" | "system";
  body_markdown: string;
  action_kind?: string;
  payload_json?: string;
  mention_all?: boolean;
  reply_count?: number;
  thread_conversation_id?: string;
  last_reply_at?: string;
  attached_tasks?: Task[];
  attachments?: Attachment[];
  created_at: string;
}

export interface InboxItem {
  conversation_id: string;
  kind: ConversationKind;
  title: string;
  parent_slug?: string;
  task_number?: number;
  task_id?: string;
  unread_count: number;
  mention_count: number;
  needs_attention: boolean;
  reason: string;
  latest_seq: number;
  latest_author_name?: string;
  latest_preview?: string;
  latest_at: string;
  following: boolean;
  notify_level: NotifyLevel;
}

export interface PairingInfo {
  id: string;
  hostname: string;
  os: string;
  status: "pending" | "confirmed" | "expired";
  expires_at: string;
}

export interface BootstrapData {
  space: Space;
  current_member: Member;
  members: Member[];
  conversations: Conversation[];
  inbox: InboxItem[];
  tasks: Task[];
  computers: Computer[];
}

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, {
    credentials: "include",
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const err = data?.error;
    throw new ApiError(resp.status, err?.code ?? "internal", err?.message ?? `HTTP ${resp.status}`);
  }
  return data as T;
}

export const api = {
  me: () => request<{ user: User; spaces: Space[] }>("/api/v1/auth/me"),
  register: (email: string, password: string, display_name: string) =>
    request<{ user: User }>("/api/v1/auth/register", {
      method: "POST",
      body: JSON.stringify({ email, password, display_name }),
    }),
  login: (email: string, password: string) =>
    request<{ user: User }>("/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    }),
  logout: () => request<{ ok: boolean }>("/api/v1/auth/logout", { method: "POST" }),
  createSpace: (slug: string, name: string) =>
    request<{ space: Space; member: Member }>("/api/v1/spaces", {
      method: "POST",
      body: JSON.stringify({ slug, name }),
    }),
  bootstrap: (spaceSlug: string) => request<BootstrapData>(`/api/v1/spaces/${spaceSlug}/bootstrap`),
  listMessages: (spaceSlug: string, convID: string, markRead = true) =>
    request<{ messages: Message[]; read_seq: number }>(
      `/api/v1/spaces/${spaceSlug}/conversations/${convID}/messages?limit=100&mark_read=${markRead}`,
    ),
  sendMessage: (
    spaceSlug: string,
    convID: string,
    body_markdown: string,
    reply_to_message_id?: string,
    attachment_ids?: string[],
  ) =>
    request<{ message: Message }>(`/api/v1/spaces/${spaceSlug}/conversations/${convID}/messages`, {
      method: "POST",
      body: JSON.stringify({ body_markdown, reply_to_message_id, attachment_ids }),
    }),
  uploadAttachment: async (spaceSlug: string, file: File): Promise<Attachment> => {
    const form = new FormData();
    form.append("file", file, file.name);
    const resp = await fetch(`/api/v1/spaces/${spaceSlug}/attachments`, {
      method: "POST",
      credentials: "include",
      body: form,
    });
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) {
      const err = data?.error;
      throw new ApiError(resp.status, err?.code ?? "internal", err?.message ?? `HTTP ${resp.status}`);
    }
    return data.attachment as Attachment;
  },
  attachmentURL: (spaceSlug: string, attachmentID: string) =>
    `/api/v1/spaces/${spaceSlug}/attachments/${attachmentID}`,
  markDone: (spaceSlug: string, convID: string) =>
    request<{ ok: boolean }>(`/api/v1/spaces/${spaceSlug}/conversations/${convID}/done`, { method: "POST" }),
  setNotifyLevel: (spaceSlug: string, convID: string, level: NotifyLevel) =>
    request<{ ok: boolean }>(`/api/v1/spaces/${spaceSlug}/conversations/${convID}/notify`, {
      method: "PUT",
      body: JSON.stringify({ level }),
    }),
  setFollowing: (spaceSlug: string, convID: string, following: boolean) =>
    request<{ ok: boolean }>(`/api/v1/spaces/${spaceSlug}/conversations/${convID}/follow`, {
      method: "PUT",
      body: JSON.stringify({ following }),
    }),
  listInbox: (spaceSlug: string, filter: "needs_me" | "unread" | "all") =>
    request<{ items: InboxItem[] }>(`/api/v1/spaces/${spaceSlug}/inbox?filter=${filter}`),
  createChannel: (spaceSlug: string, slug: string, kind: "public" | "private", topic: string) =>
    request<{ conversation: Conversation }>(`/api/v1/spaces/${spaceSlug}/channels`, {
      method: "POST",
      body: JSON.stringify({ slug, kind, topic }),
    }),
  createDM: (spaceSlug: string, peer_member_id: string) =>
    request<{ conversation: Conversation }>(`/api/v1/spaces/${spaceSlug}/dm`, {
      method: "POST",
      body: JSON.stringify({ peer_member_id }),
    }),
  listTasks: (spaceSlug: string, status: string, assignee = "") =>
    request<{ tasks: Task[] }>(`/api/v1/spaces/${spaceSlug}/tasks?status=${status}&assignee=${assignee}`),
  getTask: (spaceSlug: string, taskID: string) =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks/${taskID}`),
  createTask: (
    spaceSlug: string,
    payload: { channel_id: string; source_message_id?: string; title: string; description?: string; assignee_member_id?: string },
  ) =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks`, {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  ensureTaskConv: (spaceSlug: string, taskID: string) =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks/${taskID}/conversation`, { method: "POST" }),
  claimTask: (spaceSlug: string, taskID: string) =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks/${taskID}/claim`, { method: "POST" }),
  unclaimTask: (spaceSlug: string, taskID: string) =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks/${taskID}/unclaim`, { method: "POST" }),
  assignTask: (spaceSlug: string, taskID: string, assignee_member_id: string, reason = "") =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks/${taskID}/assign`, {
      method: "POST",
      body: JSON.stringify({ assignee_member_id, reason }),
    }),
  updateTaskStatus: (spaceSlug: string, taskID: string, status: TaskStatus) =>
    request<{ task: Task }>(`/api/v1/spaces/${spaceSlug}/tasks/${taskID}/status`, {
      method: "PUT",
      body: JSON.stringify({ status }),
    }),
  pairComputer: (spaceSlug: string, name: string, hostname: string, os: "macos" | "linux") =>
    request<{ computer: Computer; token: string }>(`/api/v1/spaces/${spaceSlug}/computers/pair`, {
      method: "POST",
      body: JSON.stringify({ name, hostname, os }),
    }),
  getPairing: (pairingID: string, code: string) =>
    request<{ pairing: PairingInfo }>(
      `/api/v1/pairings/${encodeURIComponent(pairingID)}?code=${encodeURIComponent(code)}`,
    ),
  confirmPairing: (spaceSlug: string, pairingID: string, code: string, name: string) =>
    request<{ computer: Computer }>(
      `/api/v1/spaces/${spaceSlug}/computers/pairings/${encodeURIComponent(pairingID)}/confirm`,
      { method: "POST", body: JSON.stringify({ code, name }) },
    ),
  createAgent: (
    spaceSlug: string,
    payload: {
      display_name: string;
      role_text: string;
      computer_id: string;
      driver_kind: DriverKind;
      driver_config: DriverConfigInput;
    },
  ) =>
    request<{ agent: Member }>(`/api/v1/spaces/${spaceSlug}/agents`, {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  updateAgentDriver: (spaceSlug: string, agentID: string, driver_kind: DriverKind, driver_config: DriverConfigInput) =>
    request<{ agent: Member }>(`/api/v1/spaces/${spaceSlug}/agents/${agentID}/driver`, {
      method: "PUT",
      body: JSON.stringify({ driver_kind, driver_config }),
    }),
  controlAgent: (spaceSlug: string, agentID: string, action: "restart" | "reset_session") =>
    request<{ ok: boolean }>(`/api/v1/spaces/${spaceSlug}/agents/${agentID}/control`, {
      method: "POST",
      body: JSON.stringify({ action }),
    }),
  setPermission: (spaceSlug: string, memberID: string, perm: Permission, granted: boolean) =>
    request<{ ok: boolean }>(`/api/v1/spaces/${spaceSlug}/members/${memberID}/permissions/${perm}`, {
      method: "PUT",
      body: JSON.stringify({ granted }),
    }),
};

export function formatClock(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
}

export function formatDay(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === yesterday.toDateString()) return "Yesterday";
  return d.toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric" });
}

/** Display names are letters, digits and underscores only. */
export function sanitizeDisplayName(raw: string): string {
  return raw.replace(/[^A-Za-z0-9_]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 32);
}

/** Live-typing variant: keeps a trailing hyphen so users can type "design-review". */
export function slugInput(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+/, "")
    .slice(0, 32);
}

export function nameInput(raw: string): string {
  return raw.replace(/[^A-Za-z0-9_]+/g, "").slice(0, 32);
}

export function slugify(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}
