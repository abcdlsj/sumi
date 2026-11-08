use time::OffsetDateTime;

use crate::ids::{ComputerId, MemberId, SpaceId};

use super::DomainError;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server) enum DriverKind {
    Acp(AcpProvider),
    Builtin,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server) enum AcpProvider {
    Codex,
    Claude,
    Pi,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(in crate::server) struct Member {
    pub(in crate::server) id: MemberId,
    pub(in crate::server) space_id: SpaceId,
    pub(in crate::server) display_name: String,
    pub(in crate::server) access_level: AccessLevel,
    pub(in crate::server) created_at: OffsetDateTime,
}

/// Longest Display Name; the Browser create form and both creation entries share it.
pub(in crate::server) const DISPLAY_NAME_LIMIT: usize = 40;
/// Longest Agent Role; a longer text is rejected instead of truncated.
pub(in crate::server) const ROLE_TEXT_LIMIT: usize = 12_000;

/// Display name is the only member address: Unicode letters and underscores,
/// no spaces, no leading/trailing underscore, and never the reserved `all`.
pub(in crate::server) fn valid_display_name(name: &str) -> bool {
    !name.is_empty()
        && name.to_lowercase() != "all"
        && !name.starts_with('_')
        && !name.ends_with('_')
        && name
            .chars()
            .all(|character| character.is_alphabetic() || character == '_')
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server) enum AccessLevel {
    Owner,
    Admin,
    Member,
}

impl Member {
    pub(in crate::server) fn set_access_level(
        &mut self,
        actor: AccessLevel,
        requested: AccessLevel,
    ) -> Result<(), DomainError> {
        if !actor.can_grant(requested) {
            return Err(DomainError::GovernorRequired);
        }
        if self.access_level == AccessLevel::Owner {
            return Err(DomainError::GovernorRequired);
        }
        self.access_level = requested;
        Ok(())
    }
}

impl AccessLevel {
    pub(in crate::server) const fn as_str(self) -> &'static str {
        match self {
            Self::Owner => "owner",
            Self::Admin => "admin",
            Self::Member => "member",
        }
    }

    /// Parses the stored form; `None` means no known Access Level.
    pub(in crate::server) fn parse(value: &str) -> Option<Self> {
        [Self::Owner, Self::Admin, Self::Member]
            .into_iter()
            .find(|level| level.as_str() == value)
    }

    pub(in crate::server) fn can_manage_space(self) -> bool {
        matches!(self, Self::Owner | Self::Admin)
    }

    pub(in crate::server) fn can_grant(self, requested: Self) -> bool {
        match requested {
            Self::Owner => false,
            Self::Admin => self == Self::Owner,
            Self::Member => self.can_manage_space(),
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, Hash, PartialEq)]
pub(in crate::server) enum PermissionAction {
    ChannelCreate,
    ChannelInvite,
    ChannelRemove,
    AgentCreate,
}

impl PermissionAction {
    /// Parses the stored form; `None` means no known Action.
    pub(in crate::server) fn parse(value: &str) -> Option<Self> {
        [
            Self::ChannelCreate,
            Self::ChannelInvite,
            Self::ChannelRemove,
            Self::AgentCreate,
        ]
        .into_iter()
        .find(|action| action.code() == value)
    }

    pub(in crate::server) fn code(self) -> &'static str {
        match self {
            Self::ChannelCreate => "channel.create",
            Self::ChannelInvite => "channel.invite",
            Self::ChannelRemove => "channel.remove",
            Self::AgentCreate => "agent.create",
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server) enum AgentLifecycle {
    Provisioning,
    Active,
    Suspended,
    Retired,
    Error,
}

/// A validated new Agent together with its normalized Display Name, which the Member carries.
pub(in crate::server) struct NewAgent {
    pub(in crate::server) agent: Agent,
    pub(in crate::server) display_name: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(in crate::server) struct Agent {
    pub(in crate::server) member_id: MemberId,
    pub(in crate::server) space_id: SpaceId,
    pub(in crate::server) computer_id: Option<ComputerId>,
    pub(in crate::server) role_text: String,
    pub(in crate::server) role_revision: u64,
    pub(in crate::server) lifecycle: AgentLifecycle,
    pub(in crate::server) driver_kind: DriverKind,
    pub(in crate::server) retired_at: Option<OffsetDateTime>,
}

impl Agent {
    /// Validates the Agent configuration and builds the Agent. The returned Display Name and Role
    /// text are normalized, so the caller stores them instead of trimming a second time.
    pub(in crate::server) fn create(
        member_id: MemberId,
        space_id: SpaceId,
        computer_id: ComputerId,
        display_name: &str,
        role_text: &str,
        driver_kind: DriverKind,
    ) -> Result<NewAgent, DomainError> {
        let display_name = display_name.trim();
        if !valid_display_name(display_name) || display_name.chars().count() > DISPLAY_NAME_LIMIT {
            return Err(DomainError::InvalidAgentName);
        }
        let role_text = role_text.trim();
        if role_text.is_empty() || role_text.chars().count() > ROLE_TEXT_LIMIT {
            return Err(DomainError::InvalidRole);
        }
        Ok(NewAgent {
            display_name: display_name.to_owned(),
            agent: Self {
                member_id,
                space_id,
                computer_id: Some(computer_id),
                role_text: role_text.to_owned(),
                role_revision: 1,
                lifecycle: AgentLifecycle::Provisioning,
                driver_kind,
                retired_at: None,
            },
        })
    }

    pub(in crate::server) fn apply_provision_result(
        &mut self,
        computer_id: ComputerId,
        applied: bool,
    ) -> bool {
        if self.computer_id != Some(computer_id)
            || !matches!(
                self.lifecycle,
                AgentLifecycle::Provisioning | AgentLifecycle::Active | AgentLifecycle::Error
            )
        {
            return false;
        }
        self.lifecycle = if applied {
            AgentLifecycle::Active
        } else {
            AgentLifecycle::Error
        };
        true
    }

    pub(in crate::server) fn retire(&mut self, now: OffsetDateTime) -> Result<(), DomainError> {
        if self.lifecycle == AgentLifecycle::Retired {
            return Err(DomainError::AgentRetired);
        }
        self.lifecycle = AgentLifecycle::Retired;
        self.computer_id = None;
        self.retired_at = Some(now);
        Ok(())
    }

    /// Switches the Driver this Agent runs on and reports whether anything changed.
    ///
    /// The Computer closes the Agent's provider sessions when the Driver differs, so the next Run
    /// starts on the new one. A Driver cannot change while the Agent is still being set up: the
    /// provisioning command carries the Driver it was created with.
    pub(in crate::server) fn change_driver(
        &mut self,
        driver_kind: DriverKind,
    ) -> Result<bool, DomainError> {
        if self.lifecycle == AgentLifecycle::Retired {
            return Err(DomainError::AgentRetired);
        }
        if self.lifecycle == AgentLifecycle::Provisioning {
            return Err(DomainError::InvalidTransition);
        }
        if self.driver_kind == driver_kind {
            return Ok(false);
        }
        self.driver_kind = driver_kind;
        // A Driver error is what parked an Agent in Error, so a new Driver is the way out.
        if self.lifecycle == AgentLifecycle::Error {
            self.lifecycle = AgentLifecycle::Active;
        }
        Ok(true)
    }

    pub(in crate::server) fn revise_role(&mut self, role_text: &str) -> Result<(), DomainError> {
        if self.lifecycle == AgentLifecycle::Retired {
            return Err(DomainError::AgentRetired);
        }
        let role_text = role_text.trim();
        if role_text.is_empty() {
            return Err(DomainError::InvalidRole);
        }
        if role_text == self.role_text {
            return Ok(());
        }
        self.role_text = role_text.to_owned();
        self.role_revision += 1;
        Ok(())
    }

    pub(in crate::server) fn suspend(&mut self) -> Result<(), DomainError> {
        if self.lifecycle == AgentLifecycle::Retired {
            return Err(DomainError::AgentRetired);
        }
        if self.lifecycle != AgentLifecycle::Active {
            return Err(DomainError::InvalidTransition);
        }
        self.lifecycle = AgentLifecycle::Suspended;
        Ok(())
    }

    pub(in crate::server) fn resume(&mut self) -> Result<(), DomainError> {
        if self.lifecycle == AgentLifecycle::Retired {
            return Err(DomainError::AgentRetired);
        }
        if self.lifecycle != AgentLifecycle::Suspended {
            return Err(DomainError::InvalidTransition);
        }
        self.lifecycle = AgentLifecycle::Active;
        Ok(())
    }

    pub(in crate::server) fn retry_provisioning(&mut self) -> Result<(), DomainError> {
        if self.lifecycle == AgentLifecycle::Retired {
            return Err(DomainError::AgentRetired);
        }
        if self.lifecycle != AgentLifecycle::Error {
            return Err(DomainError::InvalidTransition);
        }
        self.lifecycle = AgentLifecycle::Provisioning;
        Ok(())
    }

    pub(in crate::server) fn restart(&mut self) -> Result<(), DomainError> {
        match self.lifecycle {
            AgentLifecycle::Active | AgentLifecycle::Suspended => Ok(()),
            // A parked Agent has nothing to restart until a Human says so; a restart is that word.
            AgentLifecycle::Error => {
                self.lifecycle = AgentLifecycle::Active;
                Ok(())
            }
            AgentLifecycle::Retired => Err(DomainError::AgentRetired),
            AgentLifecycle::Provisioning => Err(DomainError::InvalidTransition),
        }
    }

    /// Parks the Agent after its Driver could not run, and reports whether the state changed.
    ///
    /// Retrying the same Driver cannot help: the next attempt fails the same way, and a failed
    /// attempt is what returns the Item and hides the reason behind another "working". A Human
    /// clears this by changing the Driver or restarting the Agent.
    pub(in crate::server) fn park_on_driver_error(&mut self) -> bool {
        if self.lifecycle != AgentLifecycle::Active {
            return false;
        }
        self.lifecycle = AgentLifecycle::Error;
        true
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server) enum ComputerLifecycle {
    Online,
    Offline,
    Deleted,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(in crate::server) struct Computer {
    pub(in crate::server) id: ComputerId,
    pub(in crate::server) space_id: SpaceId,
    pub(in crate::server) lifecycle: ComputerLifecycle,
    pub(in crate::server) token_hash: Option<String>,
    pub(in crate::server) deleted_at: Option<OffsetDateTime>,
}

impl Computer {
    pub(in crate::server) fn delete(
        &mut self,
        has_assigned_agents: bool,
        now: OffsetDateTime,
    ) -> Result<(), DomainError> {
        if has_assigned_agents {
            return Err(DomainError::ComputerHasAgents);
        }
        if self.lifecycle == ComputerLifecycle::Deleted {
            return Err(DomainError::InvalidTransition);
        }
        self.lifecycle = ComputerLifecycle::Deleted;
        self.token_hash = None;
        self.deleted_at = Some(now);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn agent(lifecycle: AgentLifecycle) -> Agent {
        Agent {
            member_id: MemberId::from_uuid(uuid::Uuid::from_u128(1)),
            space_id: SpaceId::from_uuid(uuid::Uuid::from_u128(2)),
            computer_id: Some(ComputerId::from_uuid(uuid::Uuid::from_u128(3))),
            role_text: "review boundaries".into(),
            role_revision: 4,
            lifecycle,
            driver_kind: DriverKind::Acp(AcpProvider::Codex),
            retired_at: None,
        }
    }

    fn member(access_level: AccessLevel) -> Member {
        Member {
            id: MemberId::from_uuid(uuid::Uuid::from_u128(5)),
            space_id: SpaceId::from_uuid(uuid::Uuid::from_u128(2)),
            display_name: "Member".into(),
            access_level,
            created_at: OffsetDateTime::UNIX_EPOCH,
        }
    }

    #[test]
    fn creation_returns_the_normalized_display_name_and_role() {
        let created = Agent::create(
            MemberId::from_uuid(uuid::Uuid::from_u128(1)),
            SpaceId::from_uuid(uuid::Uuid::from_u128(2)),
            ComputerId::from_uuid(uuid::Uuid::from_u128(3)),
            "  Lin  ",
            "  review boundaries  ",
            DriverKind::Acp(AcpProvider::Codex),
        )
        .expect("a valid Agent");

        assert_eq!(created.display_name, "Lin");
        assert_eq!(created.agent.role_text, "review boundaries");
        assert_eq!(created.agent.lifecycle, AgentLifecycle::Provisioning);
        assert_eq!(
            Agent::create(
                MemberId::from_uuid(uuid::Uuid::from_u128(1)),
                SpaceId::from_uuid(uuid::Uuid::from_u128(2)),
                ComputerId::from_uuid(uuid::Uuid::from_u128(3)),
                "Lin Lin",
                "review",
                DriverKind::Acp(AcpProvider::Codex),
            )
            .err(),
            Some(DomainError::InvalidAgentName)
        );
    }

    #[test]
    fn revising_a_role_advances_the_revision_only_when_the_text_changes() {
        let mut subject = agent(AgentLifecycle::Active);
        subject
            .revise_role("  review boundaries  ")
            .expect("the same text is accepted");
        assert_eq!(subject.role_revision, 4);
        subject.revise_role("write tests").expect("new text");
        assert_eq!(subject.role_revision, 5);
        assert_eq!(subject.role_text, "write tests");
        assert_eq!(
            subject.revise_role("   "),
            Err(DomainError::InvalidRole),
            "空 Role 不成立"
        );
    }

    #[test]
    fn lifecycle_actions_only_apply_from_their_source_state() {
        let mut active = agent(AgentLifecycle::Active);
        active.restart().expect("active restarts");
        active.suspend().expect("active suspends");
        assert_eq!(active.lifecycle, AgentLifecycle::Suspended);
        active.restart().expect("suspended restarts");
        assert_eq!(active.suspend(), Err(DomainError::InvalidTransition));
        active.resume().expect("suspended resumes");
        assert_eq!(active.lifecycle, AgentLifecycle::Active);
        assert_eq!(active.resume(), Err(DomainError::InvalidTransition));

        let mut failed = agent(AgentLifecycle::Error);
        failed.retry_provisioning().expect("error retries");
        assert_eq!(failed.lifecycle, AgentLifecycle::Provisioning);
        assert_eq!(
            failed.retry_provisioning(),
            Err(DomainError::InvalidTransition)
        );

        assert_eq!(
            agent(AgentLifecycle::Provisioning).restart(),
            Err(DomainError::InvalidTransition)
        );
    }

    #[test]
    fn provision_result_only_updates_the_assigned_non_terminal_agent() {
        let assigned = ComputerId::from_uuid(uuid::Uuid::from_u128(20));
        let other = ComputerId::from_uuid(uuid::Uuid::from_u128(21));
        let mut provisioning = agent(AgentLifecycle::Provisioning);
        provisioning.computer_id = Some(assigned);
        assert!(provisioning.apply_provision_result(assigned, true));
        assert_eq!(provisioning.lifecycle, AgentLifecycle::Active);
        assert!(provisioning.apply_provision_result(assigned, false));
        assert_eq!(provisioning.lifecycle, AgentLifecycle::Error);
        assert!(!provisioning.apply_provision_result(other, true));
        assert_eq!(provisioning.lifecycle, AgentLifecycle::Error);

        let mut suspended = agent(AgentLifecycle::Suspended);
        suspended.computer_id = Some(assigned);
        assert!(!suspended.apply_provision_result(assigned, true));
        assert_eq!(suspended.lifecycle, AgentLifecycle::Suspended);
    }

    #[test]
    fn a_retired_agent_rejects_every_change() {
        let mut retired = agent(AgentLifecycle::Retired);
        assert_eq!(retired.revise_role("new"), Err(DomainError::AgentRetired));
        assert_eq!(retired.suspend(), Err(DomainError::AgentRetired));
        assert_eq!(retired.resume(), Err(DomainError::AgentRetired));
        assert_eq!(retired.retry_provisioning(), Err(DomainError::AgentRetired));
        assert_eq!(retired.restart(), Err(DomainError::AgentRetired));
        assert_eq!(
            retired.change_driver(DriverKind::Builtin),
            Err(DomainError::AgentRetired)
        );
    }

    #[test]
    fn a_driver_error_parks_the_agent_until_a_human_restarts_it() {
        let mut active = agent(AgentLifecycle::Active);
        assert!(active.park_on_driver_error());
        assert_eq!(active.lifecycle, AgentLifecycle::Error);
        // Parking is not a state a retry can repeat: a parked Agent stays parked.
        assert!(!active.park_on_driver_error());

        // Both Human actions clear the park, because both are a decision to try again.
        assert_eq!(active.restart(), Ok(()));
        assert_eq!(active.lifecycle, AgentLifecycle::Active);

        let mut parked = agent(AgentLifecycle::Error);
        parked.resume().expect_err("resume is not the way out");
        assert_eq!(
            parked.retry_provisioning(),
            Ok(()),
            "provisioning again installs the Driver afresh"
        );
        assert_eq!(parked.lifecycle, AgentLifecycle::Provisioning);
    }

    #[test]
    fn changing_the_driver_reports_what_changed_and_revives_a_parked_agent() {
        let mut active = agent(AgentLifecycle::Active);
        assert_eq!(
            active.change_driver(DriverKind::Acp(AcpProvider::Codex)),
            Ok(false),
            "the same Driver is not a change"
        );
        assert_eq!(active.change_driver(DriverKind::Builtin), Ok(true));
        assert_eq!(active.driver_kind, DriverKind::Builtin);
        assert_eq!(active.lifecycle, AgentLifecycle::Active);

        // A Driver error is what parks an Agent in Error, so a new Driver is the way out.
        let mut parked = agent(AgentLifecycle::Error);
        assert_eq!(
            parked.change_driver(DriverKind::Acp(AcpProvider::Claude)),
            Ok(true)
        );
        assert_eq!(parked.lifecycle, AgentLifecycle::Active);

        // Provisioning already carries a Driver; the Agent has to finish first.
        let mut provisioning = agent(AgentLifecycle::Provisioning);
        assert_eq!(
            provisioning.change_driver(DriverKind::Builtin),
            Err(DomainError::InvalidTransition)
        );
    }

    #[test]
    fn access_level_changes_respect_the_actor_and_protect_the_owner() {
        let mut target = member(AccessLevel::Member);
        assert_eq!(
            target.set_access_level(AccessLevel::Admin, AccessLevel::Admin),
            Err(DomainError::GovernorRequired)
        );
        target
            .set_access_level(AccessLevel::Owner, AccessLevel::Admin)
            .expect("owner promotes to admin");
        assert_eq!(target.access_level, AccessLevel::Admin);
        assert_eq!(
            target.set_access_level(AccessLevel::Owner, AccessLevel::Owner),
            Err(DomainError::GovernorRequired)
        );
        let mut owner = member(AccessLevel::Owner);
        assert_eq!(
            owner.set_access_level(AccessLevel::Owner, AccessLevel::Member),
            Err(DomainError::GovernorRequired)
        );
        assert_eq!(owner.access_level, AccessLevel::Owner);
    }
}
