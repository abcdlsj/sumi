use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::time::Duration;

use async_trait::async_trait;
use time::OffsetDateTime;
use uuid::Uuid;

use super::{DriverAdapter, acp_runtime::AcpRuntimeClient, contract::StructuredProviderClient};
use crate::{
    computer::{
        adapters::sqlite::SqliteAdapter,
        application::{
            ApplicationError, LocalRunState, TerminalStatus,
            ports::{
                ComputerTransaction, DriverCompletion, DriverPort, DriverTurnOutcome,
                LocalErrorCode, LocalEvent, OpenSessionRequest, ProcessEvidence, SteerOutcome,
                TransactionPort,
            },
            recovery::RecoveryService,
            run::RunService,
        },
        core::{
            input::{
                AgentInput, ContextMessageInput, DispatchedItemInput, RunContextInput, RunInput,
                WorkInput,
            },
            scheduler::{RunPriority, WorkStrength},
            session::{DriverKind, SessionFingerprint, SessionScope},
            supervisor::{LocalRun, NewRun},
        },
    },
    ids::{AgentId, ChannelId, InboxItemId, MemberId, MessageId, RunId, SpaceId, ThreadId},
};

struct FakeClient {
    prefix: &'static str,
    sessions: BTreeSet<String>,
    steer: SteerOutcome,
    turns_started: usize,
    runs: BTreeSet<RunId>,
}

impl FakeClient {
    fn new(prefix: &'static str, steer: SteerOutcome) -> Self {
        Self {
            prefix,
            sessions: BTreeSet::new(),
            steer,
            turns_started: 0,
            runs: BTreeSet::new(),
        }
    }
}

#[async_trait(?Send)]
impl StructuredProviderClient for FakeClient {
    async fn validate(
        &mut self,
        _: &crate::computer::core::home::LocalAgent,
    ) -> Result<(), ApplicationError> {
        Ok(())
    }

    async fn create_session(
        &mut self,
        _: AgentId,
        _: DriverKind,
    ) -> Result<String, ApplicationError> {
        let locator = format!("{}-{}", self.prefix, self.sessions.len() + 1);
        self.sessions.insert(locator.clone());
        Ok(locator)
    }

    async fn resume_session(
        &mut self,
        _: AgentId,
        _: DriverKind,
        locator: &str,
    ) -> Result<bool, ApplicationError> {
        Ok(self.sessions.contains(locator))
    }

    async fn start_turn(
        &mut self,
        run_id: RunId,
        _: &str,
        _: &RunInput,
    ) -> Result<(), ApplicationError> {
        self.turns_started += 1;
        self.runs.insert(run_id);
        Ok(())
    }

    async fn steer(
        &mut self,
        _: &str,
        _: u64,
        _: &DispatchedItemInput,
    ) -> Result<SteerOutcome, ApplicationError> {
        Ok(self.steer)
    }

    async fn notice(
        &mut self,
        _: &str,
        _: &crate::computer::core::input::AttentionNoticeInput,
    ) -> Result<(), ApplicationError> {
        Ok(())
    }

    async fn interrupt(&mut self, _: &str) -> Result<(), ApplicationError> {
        Ok(())
    }

    async fn restart_agent(&mut self, _: AgentId) -> Result<(), ApplicationError> {
        Ok(())
    }

    async fn delete_session(&mut self, locator: &str) -> Result<(), ApplicationError> {
        self.sessions.remove(locator);
        Ok(())
    }

    async fn process_evidence(
        &mut self,
        run_id: RunId,
    ) -> Result<ProcessEvidence, ApplicationError> {
        Ok(if self.runs.contains(&run_id) {
            ProcessEvidence::Controlled
        } else {
            ProcessEvidence::Lost
        })
    }

    async fn poll_completions(&mut self) -> Result<Vec<DriverCompletion>, ApplicationError> {
        Ok(Vec::new())
    }
}

#[tokio::test]
async fn codex_session_resumes_and_lost_locator_is_reported() {
    let codex = FakeClient::new("codex", SteerOutcome::Unsupported);
    let builtin = FakeClient::new("builtin", SteerOutcome::Accepted);
    let mut driver = DriverAdapter::new(codex, builtin);
    let request = open_request(DriverKind::Codex, None);
    let opened = driver.open_or_resume(request).await.unwrap();
    assert!(!opened.resumed);
    let resumed = driver
        .open_or_resume(open_request(
            DriverKind::Codex,
            Some(opened.locator.clone()),
        ))
        .await
        .unwrap();
    assert!(resumed.resumed);
    assert_eq!(resumed.locator, opened.locator);
    assert_eq!(
        driver
            .open_or_resume(open_request(DriverKind::Codex, Some("missing".to_owned())))
            .await,
        Err(ApplicationError::SessionLost)
    );
}

#[tokio::test]
async fn builtin_session_resumes_without_crossing_driver_boundary() {
    let codex = FakeClient::new("codex", SteerOutcome::Unsupported);
    let builtin = FakeClient::new("builtin", SteerOutcome::Accepted);
    let mut driver = DriverAdapter::new(codex, builtin);
    let opened = driver
        .open_or_resume(open_request(DriverKind::Builtin, None))
        .await
        .unwrap();
    assert!(opened.locator.starts_with("builtin-"));
    let resumed = driver
        .open_or_resume(open_request(
            DriverKind::Builtin,
            Some(opened.locator.clone()),
        ))
        .await
        .unwrap();
    assert!(resumed.resumed);
    assert_eq!(resumed.locator, opened.locator);
}

#[tokio::test]
async fn unsupported_steer_does_not_start_a_second_turn() {
    let codex = FakeClient::new("codex", SteerOutcome::Unsupported);
    let builtin = FakeClient::new("builtin", SteerOutcome::Accepted);
    let mut driver = DriverAdapter::new(codex, builtin);
    let opened = driver
        .open_or_resume(open_request(DriverKind::Codex, None))
        .await
        .unwrap();
    let mut run = test_run(DriverKind::Codex);
    driver.start_turn(&run, &opened.locator).await.unwrap();
    let item = DispatchedItemInput {
        item_id: InboxItemId::from_uuid(Uuid::now_v7()),
        source_kind: "mention".to_owned(),
        strength: WorkStrength::Hard,
        task_id: None,
        channel_id: crate::ids::ChannelId::from_uuid(Uuid::nil()),
        thread_id: run.view().focus_thread_id,
        message_id: None,
        content: Some("new input".to_owned()),
        activity_events: Vec::new(),
    };
    run.attach(1, item).unwrap();
    assert_eq!(
        driver.steer(&run, 1).await.unwrap(),
        SteerOutcome::Unsupported
    );
    assert_eq!(driver.acp.turns_started, 1);
}

#[tokio::test]
async fn waiting_for_one_completion_preserves_other_runs() {
    let codex = FakeClient::new("codex", SteerOutcome::Unsupported);
    let builtin = FakeClient::new("builtin", SteerOutcome::Accepted);
    let mut driver = DriverAdapter::new(codex, builtin);
    let target = RunId::from_uuid(Uuid::now_v7());
    let other = RunId::from_uuid(Uuid::now_v7());
    driver.completions.extend([
        DriverCompletion {
            run_id: other,
            outcome: DriverTurnOutcome::Completed,
        },
        DriverCompletion {
            run_id: target,
            outcome: DriverTurnOutcome::Interrupted,
        },
    ]);

    assert_eq!(
        driver
            .wait_for_completion(target, std::time::Duration::ZERO)
            .await
            .unwrap(),
        Some(DriverTurnOutcome::Interrupted)
    );
    assert_eq!(
        driver.poll_completions().await.unwrap(),
        vec![DriverCompletion {
            run_id: other,
            outcome: DriverTurnOutcome::Completed,
        }]
    );
}

/// One ACP agent that dies mid-turn must only cost its own Run. Before the poll isolated processes, the
/// broken pipe failed the whole poll, so another Agent's already-drained completion was dropped and the
/// same process failed every later tick as well.
#[tokio::test]
async fn a_failing_acp_process_keeps_other_agents_completions_and_loses_only_its_run() {
    use std::os::unix::fs::PermissionsExt;

    use crate::config::{AcpConfig, AcpProcessConfig};

    let directory = tempfile::tempdir().unwrap();
    let computer_home = directory.path().join("computer");
    let executable = directory.path().join("fake-acp");
    std::fs::write(&executable, FAKE_ACP_AGENT).unwrap();
    std::fs::set_permissions(&executable, std::fs::Permissions::from_mode(0o700)).unwrap();
    let config = AcpConfig {
        codex: Some(AcpProcessConfig {
            command: vec![executable.display().to_string()],
            env: BTreeMap::new(),
        }),
        ..AcpConfig::default()
    };
    let healthy_agent = AgentId::from_uuid(Uuid::now_v7());
    let failing_agent = AgentId::from_uuid(Uuid::now_v7());
    let healthy_home = fake_agent_home(&computer_home, healthy_agent, false);
    fake_agent_home(&computer_home, failing_agent, true);

    let mut driver = DriverAdapter::new(
        AcpRuntimeClient::new(computer_home, &config, [5_u8; 32]),
        FakeClient::new("builtin", SteerOutcome::Accepted),
    );
    let healthy_session = driver
        .open_or_resume(session_request(healthy_agent))
        .await
        .unwrap();
    let failing_session = driver
        .open_or_resume(session_request(failing_agent))
        .await
        .unwrap();
    let healthy_run = running_run(healthy_agent);
    let failing_run = running_run(failing_agent);
    driver
        .start_turn(&healthy_run, &healthy_session.locator)
        .await
        .unwrap();
    driver
        .start_turn(&failing_run, &failing_session.locator)
        .await
        .unwrap();

    let mut store = SqliteAdapter::open(&directory.path().join("daemon.db"))
        .await
        .unwrap();
    for run in [healthy_run.clone(), failing_run.clone()] {
        store
            .transact(async |transaction| transaction.save_run(run))
            .await
            .unwrap();
    }

    wait_for_file(&healthy_home.join("workspace/turn-finished")).await;
    wait_for_lost_process(&mut driver, &failing_run).await;

    // One poll must deliver the healthy Agent's completion and drop the dead process.
    let completions = driver
        .poll_completions()
        .await
        .expect("a dead ACP process must not fail the poll");
    assert_eq!(
        completions,
        vec![DriverCompletion {
            run_id: healthy_run.view().id,
            outcome: DriverTurnOutcome::Completed,
        }]
    );
    for completion in completions {
        RunService::finish_driver_turn(&mut store, completion.run_id, completion.outcome)
            .await
            .unwrap();
    }
    assert!(
        driver.poll_completions().await.is_ok(),
        "the dead process is dropped instead of failing every poll"
    );

    RecoveryService::fail_lost_drivers(&mut store, &mut driver)
        .await
        .unwrap();
    assert_eq!(
        stored_run_state(&mut store, failing_run.view().id).await,
        LocalRunState::Failed
    );
    assert_eq!(
        stored_run_state(&mut store, healthy_run.view().id).await,
        LocalRunState::Completed,
        "the healthy Agent's completion is applied, not lost with the dead process"
    );
    assert!(
        store
            .transact(async |transaction| transaction.pending_events())
            .await
            .unwrap()
            .iter()
            .any(|event| matches!(
                event,
                LocalEvent::RunResult {
                    run_id,
                    status: TerminalStatus::Failed,
                    error_code: Some(LocalErrorCode::DriverLost),
                    ..
                } if run_id == &failing_run.view().id
            ))
    );
}

/// One fake ACP agent for every test in this module. A marker file in the Agent Home makes the agent
/// exit without answering `session/prompt`, which is what a crashed provider looks like to the daemon.
const FAKE_ACP_AGENT: &str = r#"#!/bin/sh
[ -n "$SUMI_SOCKET" ] || exit 10
while IFS= read -r line; do
  id="${line#*\"id\":}"; id="${id%%,*}"
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{}}}' ;;
    *'"method":"session/new"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"session-'"$(basename "$HOME")"'"}}' ;;
    *'"method":"session/prompt"'*)
      if [ -e "$HOME/workspace/dies-mid-turn" ]; then
        exit 1
      fi
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"stopReason":"end_turn"}}'
      : > "$HOME/workspace/turn-finished" ;;
  esac
done
"#;

fn fake_agent_home(computer_home: &Path, agent_id: AgentId, dies_mid_turn: bool) -> PathBuf {
    let home = super::agent_home(computer_home, agent_id);
    std::fs::create_dir_all(home.join("drivers/codex")).unwrap();
    std::fs::create_dir_all(home.join("workspace")).unwrap();
    if dies_mid_turn {
        std::fs::write(home.join("workspace/dies-mid-turn"), b"").unwrap();
    }
    home
}

fn session_request(agent_id: AgentId) -> OpenSessionRequest {
    OpenSessionRequest {
        agent_id,
        scope: SessionScope::Thread(ThreadId::from_uuid(Uuid::now_v7())),
        generation: 1,
        fingerprint: fingerprint(DriverKind::Codex),
        resume_locator: None,
    }
}

fn running_run(agent_id: AgentId) -> LocalRun {
    let mut run = test_run_for(agent_id, DriverKind::Codex);
    let thread_id = run.view().focus_thread_id;
    run.begin_start().unwrap();
    run.started(SessionScope::Thread(thread_id), 1).unwrap();
    run
}

// Wait for an observed fact; elapsed time alone never decides that the scenario is ready.
async fn wait_for_file(path: &Path) {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    while !path.exists() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "the fake ACP agent never answered its prompt"
        );
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
}

async fn wait_for_lost_process(driver: &mut impl DriverPort, run: &LocalRun) {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    while driver.process_evidence(run).await.unwrap() != ProcessEvidence::Lost {
        assert!(
            tokio::time::Instant::now() < deadline,
            "the fake ACP agent never exited"
        );
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
}

async fn stored_run_state(store: &mut SqliteAdapter, run_id: RunId) -> LocalRunState {
    store
        .transact(async |transaction| transaction.run(run_id))
        .await
        .unwrap()
        .expect("the Run is stored")
        .view()
        .state
}

fn open_request(driver: DriverKind, resume_locator: Option<String>) -> OpenSessionRequest {
    OpenSessionRequest {
        agent_id: AgentId::from_uuid(Uuid::now_v7()),
        scope: SessionScope::Thread(ThreadId::from_uuid(Uuid::now_v7())),
        generation: 1,
        fingerprint: fingerprint(driver),
        resume_locator,
    }
}

fn fingerprint(driver: DriverKind) -> SessionFingerprint {
    SessionFingerprint {
        driver,
        workspace: "workspace".to_owned(),
        role_revision: 1,
        audience: "audience".to_owned(),
    }
}

fn test_run(driver: DriverKind) -> LocalRun {
    test_run_for(AgentId::from_uuid(Uuid::now_v7()), driver)
}

fn test_run_for(agent_id: AgentId, driver: DriverKind) -> LocalRun {
    let thread_id = ThreadId::from_uuid(Uuid::now_v7());
    let mut run = LocalRun::new(NewRun {
        id: RunId::from_uuid(Uuid::now_v7()),
        agent_id,
        task_id: None,
        focus_thread_id: thread_id,
        priority: RunPriority {
            explicit_human_redirect: false,
            strength: WorkStrength::Hard,
            available_at: OffsetDateTime::now_utc(),
            has_task_continuity: false,
        },
        input: RunInput {
            product_contract: "contract".to_owned(),
            agent: AgentInput {
                agent_id,
                space_id: SpaceId::from_uuid(Uuid::nil()),
                identity: "agent".to_owned(),
                role_revision: 1,
                role: "role".to_owned(),
                memory: Vec::new(),
            },
            work: WorkInput {
                task: None,
                linked_thread_ids: vec![thread_id],
                public_result_message_id: None,
            },
            context: RunContextInput {
                focus_thread_id: thread_id,
                message_snapshot_sequence: 1,
                focus_messages: vec![ContextMessageInput {
                    message_id: MessageId::from_uuid(Uuid::now_v7()),
                    author_member_id: MemberId::from_uuid(Uuid::now_v7()),
                    body: "message".to_owned(),
                }],
                channel_id: ChannelId::from_uuid(Uuid::nil()),
                channel_snapshot_sequence: 1,
                channel_activity: Vec::new(),
                dispatched_items: Vec::new(),
            },
            channel_members: Vec::new(),
        },
    })
    .unwrap();
    run.set_session_fingerprint(fingerprint(driver));
    run
}
