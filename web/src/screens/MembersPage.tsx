import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  Check,
  Copy,
  LoaderCircle,
  MailPlus,
  MessageCircle,
  Plus,
  UserPlus,
  Users,
  Settings2,
  X,
} from "lucide-react";
import { type FormEvent, useState } from "react";

import {
  createInvitation,
  createDirectMessage,
  listAgents,
  listMembers,
  updateMember,
  type Agent,
  type Member,
  type Space,
} from "../api/client";
import { activityLabel, agentPresence } from "../agentActivity";
import { PresenceIdentity, SpaceShell } from "../components/SpaceShell";
import { SumiSelect } from "../components/SumiSelect";
import { ListSkeleton } from "../components/Skeleton";

export function MembersPage() {
  const { spaceSlug } = useParams({ from: "/s/$spaceSlug/members" });

  return (
    <SpaceShell spaceSlug={spaceSlug} active="members">
      {({ space }) => (
        <MembersWorkspace space={space} />
      )}
    </SpaceShell>
  );
}

function MembersWorkspace({ space }: { space: Space }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [inviteOpen, setInviteOpen] = useState(false);
  const [inviteLink, setInviteLink] = useState<string>();
  const [copied, setCopied] = useState(false);
  const [kindFilter, setKindFilter] = useState<"all" | "human" | "agent">("all");
  const members = useQuery({
    queryKey: ["members", space.id],
    queryFn: () => listMembers(space.id),
  });
  const agents = useQuery({
    queryKey: ["agents", space.id],
    queryFn: () => listAgents(space.id),
  });
  const activityByMemberId = new Map(
    (agents.data ?? []).map((agent) => [agent.member_id, agentPresence(agent)] as const),
  );
  const agentByMemberId = new Map((agents.data ?? []).map((agent) => [agent.member_id, agent] as const));
  const currentMember = members.data?.find((member) => member.id === space.current_member_id);
  const canInvite = currentMember?.access_level === "owner" || currentMember?.access_level === "admin";
  const canCreateAgent = canInvite || currentMember?.permissions.includes("agent.create");
  const invitation = useMutation({
    mutationFn: (email: string) => createInvitation(space.id, { email }),
    onSuccess: (created) => {
      setInviteLink(
        created.token ? `${window.location.origin}/invite/${created.token}` : undefined,
      );
      setCopied(false);
    },
  });
  const memberUpdate = useMutation({
    meta: { success: "Access updated." },
    mutationFn: ({ memberId, input }: { memberId: string; input: Parameters<typeof updateMember>[2] }) =>
      updateMember(space.id, memberId, input),
    onSuccess: (updated) => {
      queryClient.setQueryData<Member[]>(["members", space.id], (current) =>
        current?.map((member) => (member.id === updated.id ? updated : member)),
      );
    },
  });
  const directMessage = useMutation({
    mutationFn: (memberId: string) => createDirectMessage(space.id, memberId),
    meta: { error: "Could not open the DM." },
    onSuccess: (dm) => {
      void queryClient.invalidateQueries({ queryKey: ["direct-messages", space.id] });
      void navigate({
        to: "/s/$spaceSlug/dm/$memberId",
        params: { spaceSlug: space.slug, memberId: dm.other_member.id },
      });
    },
  });
  const visibleMembers = members.data?.filter((member) => kindFilter === "all" || member.kind === kindFilter) ?? [];
  const memberGroups = (['agent', 'human'] as const)
    .map((kind) => ({ kind, members: visibleMembers.filter((member) => member.kind === kind) }))
    .filter((group) => group.members.length > 0);

  function submitInvitation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    invitation.mutate(String(form.get("email") ?? ""));
  }

  async function copyInviteLink() {
    if (!inviteLink) return;
    await navigator.clipboard.writeText(inviteLink);
    setCopied(true);
  }

  return (
    <section className="members-workspace" aria-labelledby="members-heading">
      <header className="members-header">
        <div className="members-title">
          <h1 id="members-heading">Members</h1>
        </div>
        {canCreateAgent ? (
          <Link
            className="command-button command-button--accent compact-header-action"
            to="/s/$spaceSlug/computers"
            params={{ spaceSlug: space.slug }}
            hash="create-agent"
            aria-label="Create Agent"
            title="Create Agent"
          >
            <Plus aria-hidden="true" />
            <span>Create agent</span>
          </Link>
        ) : null}
        {canInvite ? (
          <button
            className="command-button compact-header-action"
            type="button"
            aria-label={inviteOpen ? "Close invitation form" : "Invite Human"}
            title={inviteOpen ? "Close invitation form" : "Invite Human"}
            onClick={() => {
              setInviteOpen((open) => !open);
              setInviteLink(undefined);
              invitation.reset();
            }}
          >
            {inviteOpen ? <X aria-hidden="true" /> : <UserPlus aria-hidden="true" />}
            <span>{inviteOpen ? "Close" : "Invite"}</span>
          </button>
        ) : null}
      </header>

      <div className="member-filter" role="group" aria-label="Filter Members by kind">
        {(["all", "human", "agent"] as const).map((kind) => (
          <button
            key={kind}
            type="button"
            className={kindFilter === kind ? "member-filter--active" : undefined}
            aria-pressed={kindFilter === kind}
            onClick={() => setKindFilter(kind)}
          >
            {kind === "all" ? "All" : kind === "human" ? "Human" : "Agent"}
            <span>
              {kind === "all"
                ? members.data?.length ?? 0
                : members.data?.filter((member) => member.kind === kind).length ?? 0}
            </span>
          </button>
        ))}
      </div>


      {inviteOpen ? (
        <section className="invite-band" aria-labelledby="invite-heading">
          <div className="invite-band-title">
            <MailPlus aria-hidden="true" />
            <div>
              <h2 id="invite-heading">Invite a Human</h2>
              <p>Bound to one email and valid for seven days.</p>
            </div>
          </div>
          {inviteLink ? (
            <div className="invite-result">
              <label htmlFor="invite-link">Invitation link</label>
              <div className="copy-field">
                <input id="invite-link" value={inviteLink} readOnly />
                <button
                  className="icon-button"
                  type="button"
                  aria-label="Copy invitation link"
                  title="Copy invitation link"
                  onClick={() => void copyInviteLink()}
                >
                  {copied ? <Check /> : <Copy />}
                </button>
              </div>
            </div>
          ) : (
            <form className="invite-form" onSubmit={submitInvitation}>
              <label htmlFor="invite-email">Email</label>
              <input id="invite-email" name="email" type="email" autoComplete="email" required />
              <button className="command-button command-button--accent" type="submit" disabled={invitation.isPending}>
                {invitation.isPending ? <LoaderCircle className="spin" /> : <UserPlus />}
                Create invitation
              </button>
            </form>
          )}
          {invitation.error ? (
            <p className="form-error invite-error" role="alert">
              {invitation.error.message}
            </p>
          ) : null}
        </section>
      ) : null}

      <div className="member-list" aria-live="polite">
        {members.isPending ? <ListSkeleton label="Loading Members" rows={5} /> : null}
        {members.error ? (
          <div className="members-status members-status--error">{members.error.message}</div>
        ) : null}
        {memberGroups.map((group) => <section className="member-group" key={group.kind} aria-labelledby={`member-group-${group.kind}`}>
          <header className="member-group-heading">
            <h2 id={`member-group-${group.kind}`}>{group.kind === "agent" ? "Agents" : "Humans"}</h2>
            <span>{group.members.length} {group.kind === "agent" ? "Agents" : "Humans"}</span>
          </header>
          {group.members.map((member) => {
          const currentAccess = currentMember?.access_level;
          const ownerCanSetAccess = currentAccess === "owner" && member.access_level !== "owner";
          return (
            <article className="member-row" key={member.id}>
              <div className="member-identity">
                <PresenceIdentity name={member.display_name} kind={member.kind} seed={member.id} activityStatus={activityByMemberId.get(member.id)} />
                <div>
                  <strong title={member.display_name}>{member.display_name}</strong>
                  <span className="member-status">
                    {member.kind === "agent" ? (
                      <>
                        <i className={`member-status-dot member-status-dot--${activityByMemberId.get(member.id) ?? "waiting"}`} aria-hidden="true" />
                        {activityLabel(activityByMemberId.get(member.id))}
                        {agentDetail(agentByMemberId.get(member.id)) ? <span className="member-status-detail">· {agentDetail(agentByMemberId.get(member.id))}</span> : null}
                      </>
                    ) : member.id === currentMember?.id ? "You" : "Space member"}
                  </span>
                </div>
              </div>
              <div className="member-controls">
                <div className="access-value">
                  {ownerCanSetAccess ? (
                    <SumiSelect
                      value={member.access_level}
                      onChange={(next) =>
                        memberUpdate.mutate({
                          memberId: member.id,
                          input: { access_level: next as "member" | "admin" },
                        })
                      }
                      options={[
                        { value: "member", label: "Member" },
                        { value: "admin", label: "Admin" },
                      ]}
                      ariaLabel={`Access level for ${member.display_name}`}
                      disabled={memberUpdate.isPending}
                    />
                  ) : (
                    <strong>{capitalize(member.access_level)}</strong>
                  )}
                </div>
              </div>
              <div className="member-actions">
                {member.kind === "agent" ? (
                  <Link
                    className="member-message-button"
                    to="/s/$spaceSlug/agents/$agentId"
                    params={{ spaceSlug: space.slug, agentId: member.id }}
                    aria-label={`Manage ${member.display_name}`}
                    title={`Manage ${member.display_name}`}
                  >
                    <Settings2 aria-hidden="true" />
                  </Link>
                ) : null}
                {member.id !== currentMember?.id ? (
                  <button
                    className="member-message-button"
                    type="button"
                    aria-label={`Message ${member.display_name}`}
                    title={`Message ${member.display_name}`}
                    disabled={directMessage.isPending}
                    onClick={() => directMessage.mutate(member.id)}
                  >
                    <MessageCircle aria-hidden="true" />
                  </button>
                ) : null}
              </div>
            </article>
          );
          })}
        </section>)}
      </div>
      {memberUpdate.error ? (
        <p className="member-update-error form-error" role="alert">
          {memberUpdate.error.message}
        </p>
      ) : null}
      {!members.isPending && visibleMembers.length === 0 ? (
        <div className="empty-members">
          <Users aria-hidden="true" />
          <div>
            <strong>{members.data?.length ? "No matching members" : "No members yet"}</strong>
            <p>Invite a Human or create an Agent to start collaborating.</p>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

/** What an Agent is doing now, or else what it is for. */
function agentDetail(agent: Agent | undefined): string | undefined {
  if (!agent) return undefined;
  if (agent.activity?.label && ["working", "dispatched"].includes(agent.activity_status)) return agent.activity.label;
  return (agent.role_text ?? "").split("\n")[0]?.trim() || undefined;
}
