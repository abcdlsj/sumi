//! Filesystem helpers for state that belongs to one local user.
//!
//! Secrets, Agent Homes, staged updates and release directories only ever hold data for the
//! owner: a directory another account can read would expose them, so every creation path shares
//! one implementation here.

use std::path::Path;

/// Creates `path` and its parents, then makes it accessible only by its owner.
pub(crate) async fn ensure_private_directory(path: &Path) -> std::io::Result<()> {
    tokio::fs::create_dir_all(path).await?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        tokio::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700)).await?;
    }
    Ok(())
}
