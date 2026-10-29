use std::path::Path;

use anyhow::{Context, ensure};
use sha2::{Digest, Sha256};
use tokio::io::AsyncWriteExt;

use crate::{
    cli::ComputerReleaseArgs,
    protocol::{
        update::{ComputerRelease, current_target},
        version::CURRENT,
    },
};

const MAX_ARTIFACT_BYTES: usize = 200 * 1024 * 1024;

pub(crate) async fn computer(args: ComputerReleaseArgs) -> anyhow::Result<()> {
    semver::Version::parse(&args.version).context("Computer release version must be SemVer")?;
    ensure!(
        args.artifact.is_file(),
        "Computer release artifact must be a regular file"
    );

    let artifact_content = tokio::fs::read(&args.artifact).await?;
    ensure!(
        artifact_content.len() <= MAX_ARTIFACT_BYTES,
        "Computer release artifact is too large"
    );
    let target = args.target.unwrap_or_else(|| current_target().to_owned());
    ensure!(
        target != "unsupported",
        "this build target is not supported for Computer releases"
    );
    ensure!(
        target
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_')),
        "Computer release target is invalid"
    );
    ensure!(
        args.url.scheme() == "https",
        "Computer release URL must use HTTPS"
    );
    let release = ComputerRelease {
        version: args.version,
        protocol_version: CURRENT.value(),
        target,
        url: args.url,
        sha256: hex::encode(Sha256::digest(&artifact_content)),
    };

    let target_dir = args.output_dir.join(&release.target);
    ensure_private_directory(&args.output_dir).await?;
    ensure_private_directory(&target_dir).await?;
    write_atomically(
        &target_dir.join("manifest.json"),
        &serde_json::to_vec_pretty(&release)?,
    )
    .await?;
    println!("{}", target_dir.display());
    Ok(())
}

async fn ensure_private_directory(path: &Path) -> anyhow::Result<()> {
    tokio::fs::create_dir_all(path).await?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700)).await?;
    }
    Ok(())
}

async fn write_atomically(path: &Path, content: &[u8]) -> anyhow::Result<()> {
    let pending = path.with_extension("json.new");
    let mut file = tokio::fs::File::create(&pending).await?;
    file.write_all(content).await?;
    file.sync_all().await?;
    drop(file);
    tokio::fs::rename(pending, path).await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[tokio::test]
    async fn computer_release_command_writes_a_manifest_for_the_published_artifact() {
        let directory = tempdir().unwrap();
        let artifact = directory.path().join("sumi");
        tokio::fs::write(&artifact, b"computer binary")
            .await
            .unwrap();
        let output = directory.path().join("stable");
        let url: url::Url = "https://github.com/abcdlsj/sumi/releases/download/v1.2.3/sumi"
            .parse()
            .unwrap();
        computer(ComputerReleaseArgs {
            artifact,
            version: "1.2.3".into(),
            target: Some("x86_64-unknown-linux-gnu".into()),
            url: url.clone(),
            output_dir: output.clone(),
        })
        .await
        .unwrap();

        let target_dir = output.join("x86_64-unknown-linux-gnu");
        let manifest: ComputerRelease = serde_json::from_slice(
            &tokio::fs::read(target_dir.join("manifest.json"))
                .await
                .unwrap(),
        )
        .unwrap();
        assert_eq!(manifest.url, url);
        assert_eq!(
            manifest.sha256,
            hex::encode(Sha256::digest(b"computer binary"))
        );
        assert_eq!(manifest.protocol_version, CURRENT.value());
        assert_eq!(
            std::fs::read_dir(target_dir).unwrap().count(),
            1,
            "only the manifest is written"
        );
    }
}
