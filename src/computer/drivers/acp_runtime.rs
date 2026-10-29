use std::{
    collections::{BTreeMap, BTreeSet, VecDeque},
    ffi::OsString,
    path::{Path, PathBuf},
    process::Stdio,
    time::Duration,
};

use agent_client_protocol_schema::{
    ProtocolVersion,
    v1::{
        AgentCapabilities, CancelNotification, ClientCapabilities, CloseSessionRequest,
        ContentBlock, Implementation, InitializeRequest, InitializeResponse, LoadSessionRequest,
        LoadSessionResponse, NewSessionRequest, NewSessionResponse, PermissionOptionKind,
        PromptRequest, PromptResponse, RequestPermissionOutcome, RequestPermissionRequest,
        RequestPermissionResponse, ResumeSessionRequest, ResumeSessionResponse,
        SelectedPermissionOutcome, SessionId, SessionModeState, SetSessionModeRequest, StopReason,
    },
};
use async_trait::async_trait;
use serde::{Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader, Lines},
    process::{Child, ChildStdin, ChildStdout, Command},
};

use crate::{
    computer::{
        application::{
            ApplicationError,
            ports::{DriverCompletion, DriverTurnOutcome, ProcessEvidence, SteerOutcome},
        },
        core::{
            home::LocalAgent,
            input::{DispatchedItemInput, RunInput},
            session::DriverKind,
        },
    },
    config::daemon_socket_path,
    ids::{AgentId, RunId},
};

use super::{agent_home, contract::StructuredProviderClient, prompt};
use crate::computer::application::capability::CapabilityService;

const RPC_TIMEOUT: Duration = Duration::from_secs(30);
// The first start may download the pinned adapter package through npx.
const STARTUP_TIMEOUT: Duration = Duration::from_secs(180);
const MAX_TURN_ATTEMPTS: u8 = 3;

#[derive(Clone, Copy, Debug, Eq, Ord, PartialEq, PartialOrd)]
enum Provider {
    Codex,
    Claude,
    Pi,
}

impl Provider {
    const ALL: [Self; 3] = [Self::Codex, Self::Claude, Self::Pi];

    fn of(driver: DriverKind) -> Result<Self, ApplicationError> {
        match driver {
            DriverKind::Codex => Ok(Self::Codex),
            DriverKind::Claude => Ok(Self::Claude),
            DriverKind::Pi => Ok(Self::Pi),
            DriverKind::Builtin => Err(ApplicationError::DriverUnavailable),
        }
    }

    fn default_command(self) -> &'static [&'static str] {
        match self {
            Self::Codex => &["npx", "-y", "@zed-industries/codex-acp@0.16.0"],
            Self::Claude => &["npx", "-y", "@zed-industries/claude-code-acp@0.16.2"],
            Self::Pi => &["npx", "-y", "pi-acp@0.0.34"],
        }
    }

    fn command_variable(self) -> &'static str {
        match self {
            Self::Codex => "SUMI_ACP_CODEX_COMMAND",
            Self::Claude => "SUMI_ACP_CLAUDE_COMMAND",
            Self::Pi => "SUMI_ACP_PI_COMMAND",
        }
    }

    fn home(self) -> &'static str {
        match self {
            Self::Codex => "drivers/codex",
            Self::Claude => "drivers/claude",
            Self::Pi => "drivers/pi",
        }
    }

    fn home_variable(self) -> &'static str {
        match self {
            Self::Codex => "CODEX_HOME",
            Self::Claude => "CLAUDE_CONFIG_DIR",
            Self::Pi => "PI_CODING_AGENT_DIR",
        }
    }

    /// Daemon variables passed through unchanged; everything else is cleared.
    fn inherited_variables(self) -> &'static [&'static str] {
        match self {
            Self::Codex => &[],
            Self::Claude => &[
                "ANTHROPIC_API_KEY",
                "ANTHROPIC_AUTH_TOKEN",
                "ANTHROPIC_BASE_URL",
                "CLAUDE_CODE_OAUTH_TOKEN",
            ],
            Self::Pi => &[
                "ANTHROPIC_API_KEY",
                "OPENAI_API_KEY",
                "GEMINI_API_KEY",
                "OPENROUTER_API_KEY",
            ],
        }
    }

    fn full_access_mode(self) -> Option<&'static str> {
        match self {
            Self::Codex => Some("full-access"),
            Self::Claude => Some("bypassPermissions"),
            Self::Pi => None,
        }
    }
}

pub(super) struct AcpRuntimeClient {
    computer_home: PathBuf,
    commands: BTreeMap<Provider, Vec<OsString>>,
    driver_secret: [u8; 32],
    processes: BTreeMap<AgentId, AcpProcess>,
    locator_owners: BTreeMap<String, AgentId>,
    run_owners: BTreeMap<RunId, AgentId>,
    run_inputs: BTreeMap<RunId, AcpRunInput>,
}

#[derive(Clone)]
struct AcpRunInput {
    locator: String,
    input: RunInput,
    attempts: u8,
}

impl AcpRuntimeClient {
    pub(super) fn new(computer_home: PathBuf, driver_secret: [u8; 32]) -> Self {
        let commands = Provider::ALL
            .into_iter()
            .map(|provider| {
                let command = std::env::var(provider.command_variable())
                    .ok()
                    .map(|value| {
                        value
                            .split_whitespace()
                            .map(OsString::from)
                            .collect::<Vec<_>>()
                    })
                    .filter(|command| !command.is_empty())
                    .unwrap_or_else(|| {
                        provider
                            .default_command()
                            .iter()
                            .map(OsString::from)
                            .collect()
                    });
                (provider, command)
            })
            .collect();
        Self::with_commands(computer_home, commands, driver_secret)
    }

    fn with_commands(
        computer_home: PathBuf,
        commands: BTreeMap<Provider, Vec<OsString>>,
        driver_secret: [u8; 32],
    ) -> Self {
        Self {
            computer_home,
            commands,
            driver_secret,
            processes: BTreeMap::new(),
            locator_owners: BTreeMap::new(),
            run_owners: BTreeMap::new(),
            run_inputs: BTreeMap::new(),
        }
    }

    fn agent_home(&self, agent_id: AgentId) -> PathBuf {
        agent_home(&self.computer_home, agent_id)
    }

    fn command(&self, provider: Provider) -> Result<&[OsString], ApplicationError> {
        self.commands
            .get(&provider)
            .map(Vec::as_slice)
            .filter(|command| !command.is_empty())
            .ok_or(ApplicationError::DriverUnavailable)
    }

    fn running(&mut self, agent_id: AgentId) -> Result<&mut AcpProcess, ApplicationError> {
        let process = self
            .processes
            .get_mut(&agent_id)
            .ok_or(ApplicationError::DriverUnavailable)?;
        if process.is_running() {
            Ok(process)
        } else {
            Err(ApplicationError::DriverUnavailable)
        }
    }

    fn owner_for_locator(&self, locator: &str) -> Result<AgentId, ApplicationError> {
        self.locator_owners
            .get(locator)
            .copied()
            .ok_or(ApplicationError::SessionLost)
    }

    /// Starts or reuses the ACP agent for one Agent. The process stays alive across Runs because
    /// its environment carries the Agent's stable Driver token; it is replaced only when it is
    /// gone or the Agent switched provider.
    async fn ensure_process(
        &mut self,
        agent_id: AgentId,
        provider: Provider,
    ) -> Result<&mut AcpProcess, ApplicationError> {
        let reusable = self
            .processes
            .get_mut(&agent_id)
            .is_some_and(|process| process.provider == provider && process.is_running());
        if !reusable {
            if let Some(mut stale) = self.processes.remove(&agent_id) {
                let _ = stale.child.kill().await;
                let _ = stale.child.wait().await;
            }
            self.locator_owners.retain(|_, owner| *owner != agent_id);
            let socket_path = daemon_socket_path(&self.computer_home);
            let driver_token = CapabilityService::driver_token(&self.driver_secret, agent_id);
            let process = AcpProcess::spawn(
                provider,
                self.command(provider)?,
                self.agent_home(agent_id),
                &socket_path,
                &driver_token,
            )
            .await?;
            self.processes.insert(agent_id, process);
        }
        self.running(agent_id)
    }

    async fn start_turn_once(
        &mut self,
        run_id: RunId,
        locator: &str,
        input: &RunInput,
    ) -> Result<(), ApplicationError> {
        let agent_id = input.agent.agent_id;
        if self.owner_for_locator(locator)? != agent_id {
            return Err(ApplicationError::SessionLost);
        }
        let encoded =
            serde_json::to_string(&input.model_view()).map_err(|_| ApplicationError::Internal)?;
        let request = PromptRequest::new(
            SessionId::new(locator),
            vec![ContentBlock::from(prompt::acp_turn_instruction(&encoded))],
        );
        self.running(agent_id)?
            .start_prompt(locator, run_id, &request)
            .await?;
        self.run_owners.insert(run_id, agent_id);
        Ok(())
    }
}

struct AcpProcess {
    provider: Provider,
    child: Child,
    stdin: ChildStdin,
    stdout: Lines<BufReader<ChildStdout>>,
    workspace: PathBuf,
    capabilities: AgentCapabilities,
    next_request_id: u64,
    /// Sessions created or restored by this process instance.
    sessions: BTreeSet<String>,
    /// Outstanding `session/prompt` request ids; their responses end the turn.
    prompts: BTreeMap<u64, RunId>,
    active_runs: BTreeMap<String, RunId>,
    failure_reasons: BTreeMap<RunId, String>,
    completions: VecDeque<DriverCompletion>,
}

impl AcpProcess {
    async fn spawn(
        provider: Provider,
        command: &[OsString],
        agent_home: PathBuf,
        socket: &Path,
        driver_token: &str,
    ) -> Result<Self, ApplicationError> {
        let provider_home = agent_home.join(provider.home());
        let workspace = agent_home.join("workspace");
        if !provider_home.is_dir() || !workspace.is_dir() {
            return Err(ApplicationError::DriverUnavailable);
        }
        let (program, arguments) = command
            .split_first()
            .ok_or(ApplicationError::DriverUnavailable)?;
        let mut process = Command::new(program);
        process
            .args(arguments)
            .current_dir(&workspace)
            .env_clear()
            .env("HOME", &agent_home)
            .env(provider.home_variable(), &provider_home)
            .env("SUMI_SOCKET", socket)
            .env("SUMI_DRIVER_TOKEN", driver_token)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        if provider == Provider::Codex
            && let Some((key, value)) = codex_provider_environment(&provider_home).await?
        {
            process.env(key, value);
        }
        for variable in provider.inherited_variables() {
            if let Some(value) = std::env::var_os(variable).filter(|value| !value.is_empty()) {
                process.env(variable, value);
            }
        }
        process.env("PATH", executable_path()?);
        if let Some(language) = std::env::var_os("LANG") {
            process.env("LANG", language);
        }
        let mut child = process
            .spawn()
            .map_err(|_| ApplicationError::DriverUnavailable)?;
        let stdin = child.stdin.take().ok_or(ApplicationError::Internal)?;
        let stdout = child.stdout.take().ok_or(ApplicationError::Internal)?;
        let mut process = Self {
            provider,
            child,
            stdin,
            stdout: BufReader::new(stdout).lines(),
            workspace,
            capabilities: AgentCapabilities::default(),
            next_request_id: 1,
            sessions: BTreeSet::new(),
            prompts: BTreeMap::new(),
            active_runs: BTreeMap::new(),
            failure_reasons: BTreeMap::new(),
            completions: VecDeque::new(),
        };
        let initialized: InitializeResponse = process
            .request(
                "initialize",
                &InitializeRequest::new(ProtocolVersion::V1)
                    .client_capabilities(ClientCapabilities::default())
                    .client_info(
                        Implementation::new("sumi", env!("CARGO_PKG_VERSION"))
                            .title("Sumi Computer".to_owned()),
                    ),
                STARTUP_TIMEOUT,
            )
            .await?;
        if initialized.protocol_version != ProtocolVersion::V1 {
            return Err(ApplicationError::DriverUnavailable);
        }
        process.capabilities = initialized.agent_capabilities;
        Ok(process)
    }

    fn session_request_timeout(&self) -> Duration {
        // Adapters may start the provider runtime lazily on the first session request.
        if self.sessions.is_empty() {
            STARTUP_TIMEOUT
        } else {
            RPC_TIMEOUT
        }
    }

    async fn create_session(&mut self) -> Result<String, ApplicationError> {
        let created: NewSessionResponse = self
            .request(
                "session/new",
                &NewSessionRequest::new(&self.workspace),
                self.session_request_timeout(),
            )
            .await?;
        let locator = created.session_id.0.to_string();
        self.grant_full_access(&locator, created.modes).await?;
        self.sessions.insert(locator.clone());
        Ok(locator)
    }

    /// Returns `false` when the agent cannot restore the session.
    async fn restore_session(&mut self, locator: &str) -> Result<bool, ApplicationError> {
        if self.sessions.contains(locator) {
            return Ok(true);
        }
        let session_id = SessionId::new(locator);
        let timeout = self.session_request_timeout();
        let modes = if self.capabilities.session_capabilities.resume.is_some() {
            self.request::<ResumeSessionResponse>(
                "session/resume",
                &ResumeSessionRequest::new(session_id, &self.workspace),
                timeout,
            )
            .await
            .map(|response| response.modes)
        } else if self.capabilities.load_session {
            // Replayed `session/update` notifications are discarded by `dispatch`.
            self.request::<LoadSessionResponse>(
                "session/load",
                &LoadSessionRequest::new(session_id, &self.workspace),
                timeout,
            )
            .await
            .map(|response| response.modes)
        } else {
            return Ok(false);
        };
        let modes = match modes {
            Ok(modes) => modes,
            Err(RpcFailure::Remote) => return Ok(false),
            Err(RpcFailure::Transport) => return Err(ApplicationError::DriverUnavailable),
        };
        self.grant_full_access(locator, modes).await?;
        self.sessions.insert(locator.to_owned());
        Ok(true)
    }

    async fn grant_full_access(
        &mut self,
        locator: &str,
        modes: Option<SessionModeState>,
    ) -> Result<(), ApplicationError> {
        let Some(target) = self.provider.full_access_mode() else {
            return Ok(());
        };
        let Some(modes) = modes else {
            return Ok(());
        };
        if modes.current_mode_id.0.as_ref() == target
            || !modes
                .available_modes
                .iter()
                .any(|mode| mode.id.0.as_ref() == target)
        {
            return Ok(());
        }
        self.request::<Value>(
            "session/set_mode",
            &SetSessionModeRequest::new(SessionId::new(locator), target),
            RPC_TIMEOUT,
        )
        .await?;
        Ok(())
    }

    async fn close_session(&mut self, locator: &str) -> Result<(), ApplicationError> {
        self.sessions.remove(locator);
        if self.capabilities.session_capabilities.close.is_none() {
            return Ok(());
        }
        match self
            .request::<Value>(
                "session/close",
                &CloseSessionRequest::new(SessionId::new(locator)),
                RPC_TIMEOUT,
            )
            .await
        {
            Ok(_) | Err(RpcFailure::Remote) => Ok(()),
            Err(RpcFailure::Transport) => Err(ApplicationError::DriverUnavailable),
        }
    }

    async fn start_prompt(
        &mut self,
        locator: &str,
        run_id: RunId,
        request: &PromptRequest,
    ) -> Result<(), ApplicationError> {
        if self.active_runs.contains_key(locator) {
            return Err(ApplicationError::Conflict);
        }
        let request_id = self.next_id().map_err(ApplicationError::from)?;
        self.write_message(&json!({
            "jsonrpc": "2.0",
            "id": request_id,
            "method": "session/prompt",
            "params": request,
        }))
        .await?;
        self.prompts.insert(request_id, run_id);
        self.active_runs.insert(locator.to_owned(), run_id);
        Ok(())
    }

    async fn cancel(&mut self, locator: &str) -> Result<(), ApplicationError> {
        if !self.active_runs.contains_key(locator) {
            return Ok(());
        }
        // The pending `session/prompt` then resolves with `cancelled`.
        self.write_message(&json!({
            "jsonrpc": "2.0",
            "method": "session/cancel",
            "params": CancelNotification::new(SessionId::new(locator)),
        }))
        .await
    }

    fn next_id(&mut self) -> Result<u64, RpcFailure> {
        let request_id = self.next_request_id;
        self.next_request_id = self
            .next_request_id
            .checked_add(1)
            .ok_or(RpcFailure::Transport)?;
        Ok(request_id)
    }

    async fn request<R: DeserializeOwned>(
        &mut self,
        method: &str,
        params: &impl Serialize,
        timeout: Duration,
    ) -> Result<R, RpcFailure> {
        let request_id = self.next_id()?;
        self.write_message(&json!({
            "jsonrpc": "2.0",
            "id": request_id,
            "method": method,
            "params": params,
        }))
        .await
        .map_err(|_| RpcFailure::Transport)?;
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            let line = tokio::time::timeout_at(deadline, self.stdout.next_line())
                .await
                .map_err(|_| RpcFailure::Transport)?
                .map_err(|_| RpcFailure::Transport)?
                .ok_or(RpcFailure::Transport)?;
            let message: Value = serde_json::from_str(&line).map_err(|_| RpcFailure::Transport)?;
            if message.get("method").is_none()
                && message.get("id").and_then(Value::as_u64) == Some(request_id)
            {
                if message.get("error").is_some() {
                    return Err(RpcFailure::Remote);
                }
                return serde_json::from_value(
                    message.get("result").cloned().unwrap_or(Value::Null),
                )
                .map_err(|_| RpcFailure::Transport);
            }
            self.dispatch(message)
                .await
                .map_err(|_| RpcFailure::Transport)?;
        }
    }

    /// Handles every message that is not the response to the request currently awaited.
    async fn dispatch(&mut self, message: Value) -> Result<(), ApplicationError> {
        match (
            message.get("method").and_then(Value::as_str),
            message.get("id"),
        ) {
            (Some(method), Some(id)) => {
                let reply = match method {
                    "session/request_permission" => {
                        let request: RequestPermissionRequest = serde_json::from_value(
                            message.get("params").cloned().unwrap_or(Value::Null),
                        )
                        .map_err(|_| ApplicationError::DriverUnavailable)?;
                        json!({"result": permission_response(&request)})
                    }
                    // No fs, terminal or elicitation capability is advertised.
                    _ => json!({"error": {"code": -32601, "message": "Method not found"}}),
                };
                let mut reply = reply;
                reply["jsonrpc"] = json!("2.0");
                reply["id"] = id.clone();
                self.write_message(&reply).await
            }
            // `session/update` carries transcript and usage that Sumi does not consume.
            (Some(_), None) => Ok(()),
            (None, Some(id)) => {
                if let Some(run_id) = id.as_u64().and_then(|id| self.prompts.remove(&id)) {
                    self.finish_prompt(run_id, &message);
                }
                Ok(())
            }
            (None, None) => Err(ApplicationError::DriverUnavailable),
        }
    }

    fn finish_prompt(&mut self, run_id: RunId, message: &Value) {
        self.active_runs.retain(|_, active| *active != run_id);
        let outcome = if let Some(error) = message.get("error") {
            Err(error_reason(error))
        } else {
            match message
                .get("result")
                .cloned()
                .map(serde_json::from_value::<PromptResponse>)
            {
                Some(Ok(response)) => match response.stop_reason {
                    StopReason::EndTurn => Ok(DriverTurnOutcome::Completed),
                    StopReason::Cancelled => Ok(DriverTurnOutcome::Interrupted),
                    StopReason::MaxTokens => Err("stop reason max_tokens".to_owned()),
                    StopReason::MaxTurnRequests => Err("stop reason max_turn_requests".to_owned()),
                    StopReason::Refusal => Err("stop reason refusal".to_owned()),
                    _ => Err("unknown stop reason".to_owned()),
                },
                _ => Err("malformed session/prompt response".to_owned()),
            }
        };
        let outcome = outcome.unwrap_or_else(|reason| {
            self.failure_reasons.insert(run_id, reason);
            DriverTurnOutcome::Failed
        });
        self.completions
            .push_back(DriverCompletion { run_id, outcome });
    }

    async fn write_message(&mut self, message: &Value) -> Result<(), ApplicationError> {
        let mut encoded = serde_json::to_vec(message).map_err(|_| ApplicationError::Internal)?;
        encoded.push(b'\n');
        self.stdin
            .write_all(&encoded)
            .await
            .map_err(|_| ApplicationError::DriverUnavailable)?;
        self.stdin
            .flush()
            .await
            .map_err(|_| ApplicationError::DriverUnavailable)
    }

    fn is_running(&mut self) -> bool {
        matches!(self.child.try_wait(), Ok(None))
    }

    async fn poll_messages(&mut self) -> Result<(), ApplicationError> {
        loop {
            let line = match tokio::time::timeout(Duration::from_millis(1), self.stdout.next_line())
                .await
            {
                Err(_) => break,
                Ok(Ok(Some(line))) => line,
                Ok(Ok(None)) | Ok(Err(_)) => return Err(ApplicationError::DriverUnavailable),
            };
            let message: Value =
                serde_json::from_str(&line).map_err(|_| ApplicationError::DriverUnavailable)?;
            self.dispatch(message).await?;
        }
        Ok(())
    }
}

fn permission_response(request: &RequestPermissionRequest) -> RequestPermissionResponse {
    let allowed = [
        PermissionOptionKind::AllowAlways,
        PermissionOptionKind::AllowOnce,
    ]
    .iter()
    .find_map(|kind| request.options.iter().find(|option| option.kind == *kind));
    RequestPermissionResponse::new(
        allowed.map_or(RequestPermissionOutcome::Cancelled, |option| {
            RequestPermissionOutcome::Selected(SelectedPermissionOutcome::new(
                option.option_id.clone(),
            ))
        }),
    )
}

fn error_reason(error: &Value) -> String {
    let message = error.get("message").and_then(Value::as_str).unwrap_or("");
    match error.get("data") {
        Some(data) if !data.is_null() => format!("{message}: {data}"),
        _ => message.to_owned(),
    }
}

fn executable_path() -> Result<OsString, ApplicationError> {
    let mut paths = Vec::new();
    if let Ok(current_executable) = std::env::current_exe()
        && let Some(parent) = current_executable.parent()
    {
        paths.push(parent.to_owned());
    }
    if let Some(path) = std::env::var_os("PATH") {
        paths.extend(std::env::split_paths(&path));
    }
    std::env::join_paths(paths).map_err(|_| ApplicationError::DriverUnavailable)
}

fn resolvable(program: &OsString) -> bool {
    let program = Path::new(program);
    if program.components().count() > 1 {
        return program.is_file();
    }
    executable_path().is_ok_and(|paths| {
        std::env::split_paths(&paths).any(|directory| directory.join(program).is_file())
    })
}

#[derive(Clone, Copy, Debug)]
enum RpcFailure {
    Remote,
    Transport,
}

impl From<RpcFailure> for ApplicationError {
    fn from(_: RpcFailure) -> Self {
        ApplicationError::DriverUnavailable
    }
}

#[async_trait(?Send)]
impl StructuredProviderClient for AcpRuntimeClient {
    async fn validate(&mut self, agent: &LocalAgent) -> Result<(), ApplicationError> {
        let provider = Provider::of(agent.driver)?;
        let agent_home = self.agent_home(agent.agent_id);
        if !agent_home.join("workspace").is_dir() || !agent_home.join(provider.home()).is_dir() {
            return Err(ApplicationError::DriverUnavailable);
        }
        let command = self.command(provider)?;
        if resolvable(&command[0]) {
            Ok(())
        } else {
            Err(ApplicationError::DriverUnavailable)
        }
    }

    async fn create_session(
        &mut self,
        agent_id: AgentId,
        driver: DriverKind,
    ) -> Result<String, ApplicationError> {
        let locator = self
            .ensure_process(agent_id, Provider::of(driver)?)
            .await?
            .create_session()
            .await?;
        self.locator_owners.insert(locator.clone(), agent_id);
        Ok(locator)
    }

    async fn resume_session(
        &mut self,
        agent_id: AgentId,
        driver: DriverKind,
        locator: &str,
    ) -> Result<bool, ApplicationError> {
        let restored = self
            .ensure_process(agent_id, Provider::of(driver)?)
            .await?
            .restore_session(locator)
            .await?;
        if restored {
            self.locator_owners.insert(locator.to_owned(), agent_id);
        }
        Ok(restored)
    }

    async fn start_turn(
        &mut self,
        run_id: RunId,
        locator: &str,
        input: &RunInput,
    ) -> Result<(), ApplicationError> {
        self.start_turn_once(run_id, locator, input).await?;
        self.run_inputs.insert(
            run_id,
            AcpRunInput {
                locator: locator.to_owned(),
                input: input.clone(),
                attempts: 1,
            },
        );
        Ok(())
    }

    async fn steer(
        &mut self,
        locator: &str,
        _: u64,
        _: &DispatchedItemInput,
    ) -> Result<SteerOutcome, ApplicationError> {
        let agent_id = self.owner_for_locator(locator)?;
        // ACP has no way to add input to a running prompt turn.
        if self.running(agent_id)?.active_runs.contains_key(locator) {
            Ok(SteerOutcome::Unsupported)
        } else {
            Ok(SteerOutcome::TooLate)
        }
    }

    async fn notice(
        &mut self,
        _: &str,
        _: &crate::computer::core::input::AttentionNoticeInput,
    ) -> Result<(), ApplicationError> {
        Ok(())
    }

    async fn interrupt(&mut self, locator: &str) -> Result<(), ApplicationError> {
        let agent_id = self.owner_for_locator(locator)?;
        self.running(agent_id)?.cancel(locator).await
    }

    async fn restart_agent(&mut self, agent_id: AgentId) -> Result<(), ApplicationError> {
        self.locator_owners.retain(|_, owner| *owner != agent_id);
        self.run_owners.retain(|_, owner| *owner != agent_id);
        self.run_inputs
            .retain(|_, input| input.input.agent.agent_id != agent_id);
        if let Some(mut process) = self.processes.remove(&agent_id) {
            let _ = process.child.kill().await;
            let _ = process.child.wait().await;
        }
        Ok(())
    }

    async fn delete_session(&mut self, locator: &str) -> Result<(), ApplicationError> {
        let agent_id = self.owner_for_locator(locator)?;
        if let Ok(process) = self.running(agent_id) {
            process.close_session(locator).await?;
        }
        self.locator_owners.remove(locator);
        Ok(())
    }

    async fn process_evidence(
        &mut self,
        run_id: RunId,
    ) -> Result<ProcessEvidence, ApplicationError> {
        let Some(agent_id) = self.run_owners.get(&run_id).copied() else {
            return Ok(ProcessEvidence::Lost);
        };
        Ok(self
            .processes
            .get_mut(&agent_id)
            .map_or(ProcessEvidence::Lost, |process| {
                if process.is_running() {
                    ProcessEvidence::Controlled
                } else {
                    ProcessEvidence::Lost
                }
            }))
    }

    async fn poll_completions(&mut self) -> Result<Vec<DriverCompletion>, ApplicationError> {
        let mut completions = Vec::new();
        for process in self.processes.values_mut() {
            process.poll_messages().await?;
            for completion in process.completions.drain(..) {
                let reason = process.failure_reasons.remove(&completion.run_id);
                completions.push((completion, reason));
            }
        }
        let mut terminal = Vec::new();
        for (completion, reason) in completions {
            let retry = completion.outcome == DriverTurnOutcome::Failed
                && reason.as_deref().is_some_and(is_retryable_failure)
                && self
                    .run_inputs
                    .get(&completion.run_id)
                    .is_some_and(|run| run.attempts < MAX_TURN_ATTEMPTS);
            if retry {
                let mut run = self
                    .run_inputs
                    .get(&completion.run_id)
                    .cloned()
                    .ok_or(ApplicationError::Internal)?;
                run.attempts += 1;
                if self
                    .start_turn_once(completion.run_id, &run.locator, &run.input)
                    .await
                    .is_ok()
                {
                    self.run_inputs.insert(completion.run_id, run);
                    continue;
                }
            }
            self.run_inputs.remove(&completion.run_id);
            self.run_owners.remove(&completion.run_id);
            terminal.push(completion);
        }
        Ok(terminal)
    }
}

fn is_retryable_failure(reason: &str) -> bool {
    let reason = reason.to_lowercase();
    if [
        "context",
        "unauthorized",
        "authentication",
        "invalid api key",
        "permission",
        "forbidden",
        "tool",
        "invalid argument",
    ]
    .iter()
    .any(|marker| reason.contains(marker))
    {
        return false;
    }
    [
        "timed out",
        "timeout",
        "connection reset",
        "connection closed",
        "broken pipe",
        "server error",
        "service unavailable",
        "overloaded",
        "bad gateway",
        "gateway timeout",
        "http 500",
        "http 502",
        "http 503",
        "http 504",
        "\"status\":500",
        "\"status\":502",
        "\"status\":503",
        "\"status\":504",
    ]
    .iter()
    .any(|marker| reason.contains(marker))
}

async fn codex_provider_environment(
    codex_home: &Path,
) -> Result<Option<(String, String)>, ApplicationError> {
    let Ok(encoded) = tokio::fs::read_to_string(codex_home.join("config.toml")).await else {
        return Ok(None);
    };
    let Ok(config) = toml::from_str::<toml::Table>(&encoded) else {
        return Ok(None);
    };
    let Some(provider_name) = config.get("model_provider").and_then(toml::Value::as_str) else {
        return Ok(None);
    };
    let Some(env_key) = config
        .get("model_providers")
        .and_then(toml::Value::as_table)
        .and_then(|providers| providers.get(provider_name))
        .and_then(toml::Value::as_table)
        .and_then(|provider| provider.get("env_key"))
        .and_then(toml::Value::as_str)
        .filter(|key| !key.is_empty() && !key.contains(['=', '\0']))
    else {
        return Ok(None);
    };

    let from_auth = tokio::fs::read(codex_home.join("auth.json"))
        .await
        .ok()
        .and_then(|encoded| serde_json::from_slice::<serde_json::Value>(&encoded).ok())
        .and_then(|auth| {
            auth.get(env_key)
                .and_then(serde_json::Value::as_str)
                .map(str::to_owned)
        });
    let value = from_auth.or_else(|| std::env::var(env_key).ok());
    Ok(value
        .filter(|value| !value.is_empty())
        .map(|value| (env_key.to_owned(), value)))
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;

    use uuid::Uuid;

    use super::*;
    use crate::{
        computer::core::{
            home::LocalAgentState,
            input::{AgentInput, ContextMessageInput, RunContextInput, WorkInput},
            scheduler::WorkStrength,
        },
        ids::{ChannelId, InboxItemId, MemberId, MessageId, SpaceId, ThreadId},
    };

    const FAKE_AGENT: &str = r#"#!/bin/sh
printf '%s\n' "$SUMI_DRIVER_TOKEN" >> "$LOG_DIR/spawns.log"
[ -n "$SUMI_SOCKET" ] || exit 10
[ -n "$CODEX_HOME" ] || exit 11
prompt=""
while IFS= read -r line; do
  id="${line#*\"id\":}"; id="${id%%,*}"
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}' ;;
    *'"method":"session/new"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{"sessionId":"session-1","modes":{"currentModeId":"read-only","availableModes":[{"id":"read-only","name":"Read"},{"id":"full-access","name":"Full"}]}}}' ;;
    *'"method":"session/set_mode"'*)
      printf '%s\n' "$line" >> "$LOG_DIR/modes.log"
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
    *'"method":"session/load"'*)
      printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"replayed"}}}}'
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
    *'"method":"session/prompt"'*)
      prompt="$id"
      case "$line" in
        *'Sumi'*) ;;
        *) exit 12 ;;
      esac
      printf '%s\n' '{"jsonrpc":"2.0","id":"permission-1","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"call-1"},"options":[{"optionId":"reject","name":"Reject","kind":"reject_once"},{"optionId":"once","name":"Once","kind":"allow_once"},{"optionId":"always","name":"Always","kind":"allow_always"}]}}' ;;
    *'"id":"permission-1"'*)
      printf '%s\n' "$line" >> "$LOG_DIR/permissions.log"
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$prompt"',"result":{"stopReason":"end_turn"}}' ;;
    *'"method":"session/cancel"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":'"$prompt"',"result":{"stopReason":"cancelled"}}' ;;
  esac
done
"#;

    fn run_input(agent: &LocalAgent) -> RunInput {
        let thread_id = ThreadId::from_uuid(Uuid::now_v7());
        RunInput {
            product_contract: "Use Sumi capabilities for collaboration facts".to_owned(),
            agent: AgentInput {
                agent_id: agent.agent_id,
                space_id: agent.space_id,
                identity: agent.name.clone(),
                role_revision: agent.role_revision,
                role: agent.role.clone(),
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
        }
    }

    fn item(thread_id: ThreadId) -> DispatchedItemInput {
        DispatchedItemInput {
            item_id: InboxItemId::from_uuid(Uuid::now_v7()),
            source_kind: "mention".to_owned(),
            strength: WorkStrength::Hard,
            task_id: None,
            channel_id: ChannelId::from_uuid(Uuid::nil()),
            thread_id,
            message_id: None,
            content: Some("new item".to_owned()),
            activity_events: Vec::new(),
        }
    }

    async fn next_completion(client: &mut AcpRuntimeClient) -> DriverCompletion {
        for _ in 0..200 {
            if let Some(completion) = client.poll_completions().await.unwrap().pop() {
                return completion;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        panic!("the fake ACP agent did not finish the prompt turn");
    }

    #[tokio::test]
    async fn codex_provider_environment_uses_the_declared_key_from_agent_auth() {
        let directory = tempfile::tempdir().unwrap();
        std::fs::write(
            directory.path().join("config.toml"),
            "model_provider = \"local\"\n\n[model_providers.local]\nenv_key = \"LOCAL_API_KEY\"\n",
        )
        .unwrap();
        std::fs::write(
            directory.path().join("auth.json"),
            r#"{"LOCAL_API_KEY":"provider-secret"}"#,
        )
        .unwrap();

        let environment = codex_provider_environment(directory.path()).await.unwrap();

        assert_eq!(
            environment
                .as_ref()
                .map(|(key, value)| (key.as_str(), value.as_str())),
            Some(("LOCAL_API_KEY", "provider-secret"))
        );
    }

    #[tokio::test]
    async fn acp_runtime_maps_sessions_turns_permissions_and_cancellation() {
        let directory = tempfile::tempdir().unwrap();
        let logs = directory.path().join("logs");
        std::fs::create_dir_all(&logs).unwrap();
        let computer_home = directory.path().join("computer");
        let agent_id = AgentId::from_uuid(Uuid::now_v7());
        let home = agent_home(&computer_home, agent_id);
        std::fs::create_dir_all(home.join("drivers/codex")).unwrap();
        std::fs::create_dir_all(home.join("workspace")).unwrap();
        let executable = directory.path().join("fake-acp");
        std::fs::write(&executable, FAKE_AGENT).unwrap();
        std::fs::set_permissions(&executable, std::fs::Permissions::from_mode(0o700)).unwrap();
        let commands = BTreeMap::from([(
            Provider::Codex,
            vec![
                OsString::from("/usr/bin/env"),
                OsString::from(format!("LOG_DIR={}", logs.display())),
                executable.into_os_string(),
            ],
        )]);
        let agent = LocalAgent {
            agent_id,
            space_id: SpaceId::from_uuid(Uuid::now_v7()),
            name: "Codex".to_owned(),
            role_revision: 1,
            role: "Implement the current Sumi Run".to_owned(),
            driver: DriverKind::Codex,
            state: LocalAgentState::Active,
        };
        let mut client = AcpRuntimeClient::with_commands(computer_home, commands, [7_u8; 32]);
        client.validate(&agent).await.unwrap();
        assert_eq!(
            client
                .validate(&LocalAgent {
                    driver: DriverKind::Claude,
                    ..agent.clone()
                })
                .await,
            Err(ApplicationError::DriverUnavailable),
            "a provider without its Agent Home directory cannot validate"
        );

        let locator = client
            .create_session(agent_id, DriverKind::Codex)
            .await
            .unwrap();
        assert_eq!(locator, "session-1");
        assert!(
            std::fs::read_to_string(logs.join("modes.log"))
                .unwrap()
                .contains(r#""modeId":"full-access""#)
        );

        let input = run_input(&agent);
        let thread_id = input.context.focus_thread_id;
        let first_run = RunId::from_uuid(Uuid::now_v7());
        client
            .start_turn(first_run, &locator, &input)
            .await
            .unwrap();
        assert_eq!(
            client.steer(&locator, 1, &item(thread_id)).await.unwrap(),
            SteerOutcome::Unsupported
        );
        assert_eq!(
            client.process_evidence(first_run).await.unwrap(),
            ProcessEvidence::Controlled
        );
        assert_eq!(
            next_completion(&mut client).await,
            DriverCompletion {
                run_id: first_run,
                outcome: DriverTurnOutcome::Completed,
            }
        );
        assert!(
            std::fs::read_to_string(logs.join("permissions.log"))
                .unwrap()
                .contains(r#""optionId":"always""#)
        );
        assert_eq!(
            client.steer(&locator, 2, &item(thread_id)).await.unwrap(),
            SteerOutcome::TooLate
        );

        let second_run = RunId::from_uuid(Uuid::now_v7());
        client
            .start_turn(second_run, &locator, &input)
            .await
            .unwrap();
        client.interrupt(&locator).await.unwrap();
        assert_eq!(
            next_completion(&mut client).await,
            DriverCompletion {
                run_id: second_run,
                outcome: DriverTurnOutcome::Interrupted,
            }
        );

        client.restart_agent(agent_id).await.unwrap();
        assert!(
            client
                .resume_session(agent_id, DriverKind::Codex, &locator)
                .await
                .unwrap()
        );
        assert_eq!(
            std::fs::read_to_string(logs.join("spawns.log"))
                .unwrap()
                .lines()
                .count(),
            2,
            "the ACP process is resident until the Agent restarts"
        );
        client.delete_session(&locator).await.unwrap();
        assert_eq!(
            client.steer(&locator, 3, &item(thread_id)).await,
            Err(ApplicationError::SessionLost)
        );
    }
}
