//! Local pairing: where this Computer keeps its identity and how it obtains one.

use super::*;

#[derive(Serialize, Deserialize)]
pub(super) struct ComputerSecrets {
    pub(super) computer_id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) token: String,
}

#[derive(Serialize)]
struct BeginPairing<'a> {
    token_hash: String,
    hostname: &'a str,
    os: &'static str,
    daemon_version: &'static str,
}

#[derive(Deserialize)]
struct PairingStarted {
    pairing_id: Uuid,
    code: String,
    expires_at: String,
    /// Set when the Web UI is deployed apart from the Server.
    web_origin: Option<url::Url>,
}

#[derive(Deserialize)]
struct PairingStatus {
    status: String,
    computer_id: Option<Uuid>,
    space_id: Option<Uuid>,
}

pub(super) async fn find_paired_computer(
    root: &Path,
) -> anyhow::Result<Option<(PathBuf, ComputerSecrets)>> {
    let mut spaces = match tokio::fs::read_dir(root).await {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error).context("failed to read Computer state root"),
    };
    let mut found = Vec::new();
    while let Some(space) = spaces.next_entry().await? {
        if !space.file_type().await?.is_dir() || space.file_name() == "pairing" {
            continue;
        }
        let mut computers = tokio::fs::read_dir(space.path()).await?;
        while let Some(computer) = computers.next_entry().await? {
            if !computer.file_type().await?.is_dir() {
                continue;
            }
            let secrets_path = computer.path().join("secrets.json");
            if let Ok(encoded) = tokio::fs::read(&secrets_path).await {
                let secrets = serde_json::from_slice(&encoded)
                    .with_context(|| format!("invalid {}", secrets_path.display()))?;
                found.push((computer.path(), secrets));
            }
        }
    }
    ensure!(
        found.len() <= 1,
        "multiple paired Computers exist under {}; set computer.state_dir to one Computer Home",
        root.display()
    );
    Ok(found.pop())
}

pub(super) async fn pair(
    root: &Path,
    server: &url::Url,
) -> anyhow::Result<(PathBuf, ComputerSecrets)> {
    let mut token_bytes = [0_u8; 32];
    OsRng.fill_bytes(&mut token_bytes);
    let token = URL_SAFE_NO_PAD.encode(token_bytes);
    let hostname = hostname::get()
        .context("failed to read Computer hostname")?
        .to_string_lossy()
        .into_owned();
    let client = reqwest::Client::builder()
        .connect_timeout(std::time::Duration::from_secs(10))
        .timeout(std::time::Duration::from_secs(30))
        .build()?;
    let endpoint = server.join("api/v1/computer-pairings")?;
    let started = client
        .post(endpoint)
        .json(&BeginPairing {
            token_hash: hex::encode(Sha256::digest(token.as_bytes())),
            hostname: &hostname,
            os: if cfg!(target_os = "macos") {
                "macos"
            } else {
                "linux"
            },
            daemon_version: env!("CARGO_PKG_VERSION"),
        })
        .send()
        .await
        .context("failed to start Computer pairing")?
        .error_for_status()
        .context("Server rejected Computer pairing")?
        .json::<PairingStarted>()
        .await
        .context("invalid Computer pairing response")?;
    let pairing_url = started
        .web_origin
        .as_ref()
        .unwrap_or(server)
        .join(&format!(
            "pair-computer/{}/?code={}",
            started.pairing_id, started.code
        ))?;
    tracing::info!(url=%pairing_url, expires_at=%started.expires_at, "Confirm this Computer in Sumi");

    let status_url = server.join(&format!(
        "api/v1/computer-pairings/{}/status",
        started.pairing_id
    ))?;
    loop {
        tokio::select! {
            _ = shutdown_signal() => anyhow::bail!("Computer pairing canceled"),
            _ = tokio::time::sleep(std::time::Duration::from_secs(2)) => {}
        }
        let response = match client
            .get(status_url.clone())
            .bearer_auth(&token)
            .send()
            .await
        {
            Ok(response) if response.status().is_server_error() => {
                tracing::warn!(status = %response.status(), "Computer pairing status is unavailable; retrying");
                continue;
            }
            Ok(response) => response,
            // The pairing URL stays valid across a network blip; restarting would replace it.
            Err(error) => {
                tracing::warn!(%error, "Computer pairing status request failed; retrying");
                continue;
            }
        };
        let status = response
            .error_for_status()
            .context("Server rejected Computer pairing status")?
            .json::<PairingStatus>()
            .await
            .context("invalid Computer pairing status")?;
        match status.status.as_str() {
            "pending" => continue,
            "expired" => anyhow::bail!("Computer pairing expired"),
            "confirmed" => {
                let computer_id = status
                    .computer_id
                    .context("confirmed pairing has no Computer")?;
                let space_id = status.space_id.context("confirmed pairing has no Space")?;
                let home = root
                    .join(space_id.to_string())
                    .join(computer_id.to_string());
                ensure_private_directory(&home).await?;
                let secrets = ComputerSecrets {
                    computer_id,
                    space_id,
                    token,
                };
                write_secrets(&home.join("secrets.json"), &secrets).await?;
                return Ok((home, secrets));
            }
            _ => anyhow::bail!("Server returned an unknown Computer pairing status"),
        }
    }
}

async fn write_secrets(path: &Path, secrets: &ComputerSecrets) -> anyhow::Result<()> {
    let encoded = serde_json::to_vec_pretty(secrets)?;
    let mut options = tokio::fs::OpenOptions::new();
    options.create_new(true).write(true);
    let mut file = options
        .open(path)
        .await
        .with_context(|| format!("failed to create {}", path.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        file.set_permissions(std::fs::Permissions::from_mode(0o600))
            .await?;
    }
    use tokio::io::AsyncWriteExt;
    file.write_all(&encoded).await?;
    file.sync_all().await?;
    Ok(())
}
