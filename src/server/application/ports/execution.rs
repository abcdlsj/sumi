use super::*;

#[async_trait]
pub(in crate::server) trait ExecutionTransaction {
    async fn run(&mut self, id: RunId) -> Result<Run, ApplicationError>;
    async fn active_run_for_agent(
        &mut self,
        agent_id: MemberId,
    ) -> Result<Option<RunId>, ApplicationError>;
    async fn completed_run_for_event(
        &mut self,
        event_id: EventId,
    ) -> Result<Option<RunId>, ApplicationError>;
    async fn nonterminal_runs_for_computer(
        &mut self,
        computer_id: ComputerId,
    ) -> Result<Vec<RunId>, ApplicationError>;
    /// Marks pending Computer commands that target a Run as settled without delivering them.
    /// Called when the Run reaches a terminal state: commands queued before the terminal
    /// transition (start, attach, notice, stop) are obsolete and must not be replayed.
    async fn settle_run_commands(
        &mut self,
        run_id: RunId,
        computer_id: ComputerId,
    ) -> Result<(), ApplicationError>;
    async fn save_run(&mut self, run: Run) -> Result<(), ApplicationError>;
    /// Locks the Channel that owns `scope`, then returns up to `limit` unobserved Messages. Returns
    /// `None` when the Run holds no observed position for the scope.
    async fn unobserved_messages(
        &mut self,
        run_id: RunId,
        observer: MemberId,
        scope: ObservationScope,
        limit: u32,
    ) -> Result<Option<UnobservedMessages>, ApplicationError>;
    /// Advances the observed position after a held write and counts the hold.
    async fn record_hold(
        &mut self,
        run_id: RunId,
        scope: ObservationScope,
        observed_sequence: u64,
    ) -> Result<(), ApplicationError>;
    async fn clear_holds(
        &mut self,
        run_id: RunId,
        scope: ObservationScope,
    ) -> Result<(), ApplicationError>;
    /// Records Messages a read returned. The position advances only when the returned sequences
    /// cover every visible Message between the current position and the returned maximum.
    async fn record_read(
        &mut self,
        run_id: RunId,
        observer: MemberId,
        scope: ObservationScope,
        sequences: &[u64],
    ) -> Result<(), ApplicationError>;
    async fn dispatchable_work(
        &mut self,
        now: time::OffsetDateTime,
        limit: u32,
    ) -> Result<Vec<DispatchCandidate>, ApplicationError>;
    async fn record_dispatch_failure(
        &mut self,
        item_id: InboxItemId,
        message_id: Option<MessageId>,
        channel_id: ChannelId,
        error_code: &str,
    ) -> Result<bool, ApplicationError>;
    async fn authorize_run_capability(
        &mut self,
        proof: &RunCapabilityProof,
    ) -> Result<bool, ApplicationError>;
    async fn active_run_for_visible_agent(
        &mut self,
        agent_id: MemberId,
        viewer_id: MemberId,
    ) -> Result<Option<RunId>, ApplicationError>;
    async fn agent_provision_command_target(
        &mut self,
        computer_id: ComputerId,
        command_id: CommandId,
        sequence: u64,
    ) -> Result<Option<MemberId>, ApplicationError>;
}
