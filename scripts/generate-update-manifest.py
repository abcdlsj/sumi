#!/usr/bin/env python3
"""Generate signed latest.json + latest.json.sig for Seamless Auto-Update.

Uses standard RFC 8032 Ed25519 via OpenSSL (`openssl pkeyutl -sign -rawin`)
so CI requires zero external signing binaries and clients can verify with
standard Ed25519 libraries (`ed25519-dalek` in Rust, `crypto/ed25519` in Go).
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile


def sha256_file(path: pathlib.Path) -> tuple[str, int]:
    h = hashlib.sha256()
    size = 0
    with path.open("rb") as f:
        while True:
            chunk = f.read(1024 * 1024)
            if not chunk:
                break
            h.update(chunk)
            size += len(chunk)
    return h.hexdigest(), size


def openssl_sign_raw(key_path: pathlib.Path, data: bytes) -> str:
    with tempfile.NamedTemporaryFile(delete=False) as msg_file:
        msg_file.write(data)
        msg_path = pathlib.Path(msg_file.name)
    try:
        res = subprocess.run(
            [
                "openssl",
                "pkeyutl",
                "-sign",
                "-inkey",
                str(key_path),
                "-rawin",
                "-in",
                str(msg_path),
            ],
            check=True,
            capture_output=True,
        )
        return base64.b64encode(res.stdout).decode("ascii")
    finally:
        msg_path.unlink(missing_ok=True)


def openssl_verify_raw(pub_pem_path: pathlib.Path, data: bytes, sig_b64: str) -> None:
    sig_bytes = base64.b64decode(sig_b64)
    with tempfile.NamedTemporaryFile(delete=False) as msg_file:
        msg_file.write(data)
        msg_path = pathlib.Path(msg_file.name)
    with tempfile.NamedTemporaryFile(delete=False) as sig_file:
        sig_file.write(sig_bytes)
        sig_path = pathlib.Path(sig_file.name)
    try:
        subprocess.run(
            [
                "openssl",
                "pkeyutl",
                "-verify",
                "-pubin",
                "-inkey",
                str(pub_pem_path),
                "-rawin",
                "-in",
                str(msg_path),
                "-sigfile",
                str(sig_path),
            ],
            check=True,
            capture_output=True,
        )
    finally:
        msg_path.unlink(missing_ok=True)
        sig_path.unlink(missing_ok=True)


def canonical_asset_payload(
    app: str, version: str, platform: str, sha256_hex: str, size: int
) -> bytes:
    return f"v1:{app}:{version}:{platform}:{sha256_hex}:{size}".encode("utf-8")


def infer_format(filename: str) -> str:
    lower = filename.lower()
    if lower.endswith(".tar.gz") or lower.endswith(".tgz"):
        return "app-tar-gz"
    if lower.endswith(".zip"):
        return "app-zip"
    if lower.endswith(".exe"):
        return "nsis-exe"
    if lower.endswith(".msi"):
        return "msi"
    return "binary"


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate signed latest.json manifest")
    parser.add_argument("--app", required=True, help="App slug (e.g. sumi)")
    parser.add_argument("--repo", required=True, help="GitHub repo (e.g. abcdlsj/sumi)")
    parser.add_argument("--tag", required=True, help="Git tag (e.g. v6.0.8)")
    parser.add_argument("--version", required=True, help="SemVer version (e.g. 6.0.8)")
    parser.add_argument("--bundle-id", required=True, help="Bundle identifier")
    parser.add_argument("--bundle-name", required=True, help="macOS .app name (e.g. Sumi.app)")
    parser.add_argument("--min-os", default="13.0", help="Minimum macOS version")
    parser.add_argument("--notes-file", default="", help="Optional path to release notes")
    parser.add_argument("--out-dir", required=True, help="Output directory for latest.json + .sig")
    parser.add_argument(
        "--asset",
        action="append",
        required=True,
        help="Platform asset mapping: <platform>=<filepath> (e.g. darwin-aarch64=dist/App.zip)",
    )
    args = parser.parse_args()

    key_pem = os.environ.get("UPDATE_SIGNING_KEY", "").strip()
    if not key_pem:
        print("UPDATE_SIGNING_KEY not set; skipping signed update manifest generation.", file=sys.stderr)
        return 0

    version = args.version.lstrip("v")
    out_dir = pathlib.Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    notes = ""
    if args.notes_file and pathlib.Path(args.notes_file).is_file():
        notes = pathlib.Path(args.notes_file).read_text(encoding="utf-8").strip()[:500]

    with tempfile.TemporaryDirectory() as tmp:
        tmp_dir = pathlib.Path(tmp)
        key_path = tmp_dir / "signing.pem"
        pub_path = tmp_dir / "signing.pub.pem"
        key_path.write_text(key_pem + "\n", encoding="utf-8")
        os.chmod(key_path, 0o600)

        # Derive public PEM for immediate self-verification.
        subprocess.run(
            ["openssl", "pkey", "-in", str(key_path), "-pubout", "-out", str(pub_path)],
            check=True,
            capture_output=True,
        )

        platforms: dict[str, dict[str, object]] = {}
        for item in args.asset:
            if "=" not in item:
                raise SystemExit(f"invalid --asset mapping (expected platform=path): {item}")
            platform, raw_path = item.split("=", 1)
            asset_path = pathlib.Path(raw_path)
            if not asset_path.is_file():
                raise SystemExit(f"asset file not found for {platform}: {asset_path}")

            digest, size = sha256_file(asset_path)
            payload = canonical_asset_payload(args.app, version, platform, digest, size)
            sig_b64 = openssl_sign_raw(key_path, payload)
            openssl_verify_raw(pub_path, payload, sig_b64)

            fmt = infer_format(asset_path.name)
            url = f"https://github.com/{args.repo}/releases/download/{args.tag}/{asset_path.name}"
            entry: dict[str, object] = {
                "url": url,
                "signature": sig_b64,
                "sha256": digest,
                "size": size,
                "format": fmt,
            }
            if platform.startswith("darwin-"):
                entry["bundle_name"] = args.bundle_name
                entry["bundle_id"] = args.bundle_id
                entry["min_os"] = args.min_os
            platforms[platform] = entry

        manifest = {
            "schema_version": 1,
            "app": args.app,
            "version": version,
            "pub_date": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "channel": "stable",
            "release_url": f"https://github.com/{args.repo}/releases/tag/{args.tag}",
            "notes": notes,
            "platforms": platforms,
        }

        manifest_bytes = (
            json.dumps(manifest, indent=2, ensure_ascii=False, sort_keys=True) + "\n"
        ).encode("utf-8")
        manifest_sig = openssl_sign_raw(key_path, manifest_bytes)
        openssl_verify_raw(pub_path, manifest_bytes, manifest_sig)

        (out_dir / "latest.json").write_bytes(manifest_bytes)
        (out_dir / "latest.json.sig").write_text(manifest_sig + "\n", encoding="utf-8")

    print(f"Generated and verified {out_dir / 'latest.json'} + latest.json.sig ({len(platforms)} platforms)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
