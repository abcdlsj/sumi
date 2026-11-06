use argon2::{Argon2, PasswordHash, PasswordHasher, PasswordVerifier, password_hash::SaltString};
use async_trait::async_trait;
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use rand::{
    RngCore,
    distributions::{Distribution, Uniform},
    rngs::OsRng,
};

use crate::server::application::ports::{
    ApplicationError, InvitationTokenPort, PairingCodePort, PasswordPort, RawInvitationToken,
    RawPairingCode, RawSessionToken, SessionTokenPort,
};

pub(super) struct Argon2Passwords;

#[async_trait]
impl PasswordPort for Argon2Passwords {
    async fn hash(&self, password: &str) -> Result<String, ApplicationError> {
        let password = password.to_owned();
        tokio::task::spawn_blocking(move || {
            let salt = SaltString::generate(&mut OsRng);
            Argon2::default()
                .hash_password(password.as_bytes(), &salt)
                .map(|hash| hash.to_string())
                .map_err(|_| ApplicationError::Internal)
        })
        .await
        .map_err(|_| ApplicationError::Internal)?
    }

    async fn verify(&self, password: &str, stored_hash: Option<&str>) -> bool {
        let password = password.to_owned();
        let stored_hash = stored_hash.map(str::to_owned);
        tokio::task::spawn_blocking(move || {
            let Some(stored_hash) = stored_hash else {
                // Same KDF work as a real account, so timing does not reveal registered emails.
                let _ = verify_password(&password, absent_account_hash());
                return false;
            };
            // A malformed hash and a wrong password are intentionally indistinguishable.
            verify_password(&password, &stored_hash)
        })
        .await
        .unwrap_or(false)
    }
}

fn verify_password(password: &str, stored_hash: &str) -> bool {
    PasswordHash::new(stored_hash).is_ok_and(|parsed| {
        Argon2::default()
            .verify_password(password.as_bytes(), &parsed)
            .is_ok()
    })
}

/// A hash with the default parameters that no password is known for.
fn absent_account_hash() -> &'static str {
    static HASH: std::sync::OnceLock<String> = std::sync::OnceLock::new();
    HASH.get_or_init(|| {
        let mut secret = [0_u8; 32];
        OsRng.fill_bytes(&mut secret);
        Argon2::default()
            .hash_password(&secret, &SaltString::generate(&mut OsRng))
            .map(|hash| hash.to_string())
            .expect("Argon2 hashes a random secret with default parameters")
    })
}

pub(super) struct RandomSessionTokens;

impl SessionTokenPort for RandomSessionTokens {
    fn generate(&self) -> RawSessionToken {
        RawSessionToken::new(random_token())
    }
}

pub(super) struct RandomInvitationTokens;

impl InvitationTokenPort for RandomInvitationTokens {
    fn generate(&self) -> RawInvitationToken {
        RawInvitationToken::new(random_token())
    }
}

pub(super) struct NumericPairingCodes;

impl PairingCodePort for NumericPairingCodes {
    fn generate(&self) -> RawPairingCode {
        let code = Uniform::from(0..1_000_000).sample(&mut OsRng);
        RawPairingCode::new(format!("{code:06}"))
    }
}

fn random_token() -> String {
    let mut bytes = [0_u8; 32];
    OsRng.fill_bytes(&mut bytes);
    URL_SAFE_NO_PAD.encode(bytes)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pairing_codes_are_six_digits_and_hidden_from_debug_output() {
        let code = NumericPairingCodes.generate();
        assert_eq!(code.expose().len(), 6);
        assert!(code.expose().bytes().all(|byte| byte.is_ascii_digit()));
        assert!(!format!("{code:?}").contains(code.expose()));
    }

    #[tokio::test]
    async fn password_verification_rejects_wrong_password_and_corrupt_hash() {
        let passwords = Argon2Passwords;
        let hash = passwords.hash("correct horse battery").await.expect("hash");
        assert!(passwords.verify("correct horse battery", Some(&hash)).await);
        assert!(!passwords.verify("wrong password", Some(&hash)).await);
        assert!(
            !passwords
                .verify("correct horse battery", Some("not-a-hash"))
                .await
        );
        assert!(!passwords.verify("correct horse battery", None).await);
    }

    /// An unknown email must cost the KDF like a wrong password, or the response time tells an
    /// attacker which emails have accounts.
    #[tokio::test]
    async fn an_unknown_account_costs_the_same_verification_work() {
        let passwords = Argon2Passwords;
        let hash = passwords.hash("correct horse battery").await.expect("hash");
        // Warm the absent-account hash so the measurement compares verification only.
        passwords.verify("warm up", None).await;
        let started = std::time::Instant::now();
        passwords.verify("wrong password", Some(&hash)).await;
        let known = started.elapsed();
        let started = std::time::Instant::now();
        passwords.verify("wrong password", None).await;
        let unknown = started.elapsed();
        assert!(
            unknown * 4 >= known,
            "unknown account took {unknown:?}, known account {known:?}"
        );
    }

    /// Argon2 is CPU-bound and slow. On a single-threaded runtime an inline hash would keep the
    /// spawned task from ever yielding, so the yield below would run only after the hash finished.
    #[tokio::test(flavor = "current_thread")]
    async fn password_hashing_leaves_the_runtime_thread_free() {
        let hashing = tokio::spawn(async {
            Argon2Passwords
                .hash("correct horse battery")
                .await
                .expect("hash")
        });
        tokio::task::yield_now().await;
        assert!(
            !hashing.is_finished(),
            "password hashing ran on the async runtime thread"
        );
        hashing.await.expect("hashing task");
    }

    #[test]
    fn session_tokens_are_unique_and_hidden_from_debug_output() {
        let tokens = RandomSessionTokens;
        let first = tokens.generate();
        let second = tokens.generate();
        assert_ne!(first.expose(), second.expose());
        assert_ne!(first.sha256_hash(), second.sha256_hash());
        let rendered = format!("{first:?}");
        assert!(!rendered.contains(first.expose()));
    }

    #[test]
    fn invitation_tokens_are_unique_and_hidden_from_debug_output() {
        let tokens = RandomInvitationTokens;
        let first = tokens.generate();
        let second = tokens.generate();
        assert_ne!(first.expose(), second.expose());
        assert_ne!(first.sha256_hash(), second.sha256_hash());
        assert!(!format!("{first:?}").contains(first.expose()));
    }
}
