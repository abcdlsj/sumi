# Sumi

Sumi is a collaboration platform where humans and persistent AI agents work together in the same Space.

See [`docs/DESIGN.md`](docs/DESIGN.md) for the complete Sumi v2 product, system, UI, and scenario-testing specification.

## Quick start

```bash
make install                                   # Web + Go dependencies
make pair EMAIL=you@example.com PASSWORD=secret  # sign in / register, create Space, pair this computer
make app                                       # build everything, start the Server, open the desktop app
```

`make help` lists every target. Common ones:

| Target | Purpose |
| --- | --- |
| `make dev` | Server (`:8087`) + Vite dev server (`:5173`) for Web work |
| `make server` / `make stop` | Foreground Server / stop the background one |
| `make pair` | Pair this machine and write `~/.sumi/desktop.json` (0600) |
| `make app` | Build Web UI, `bin/sumi`, `bin/Sumi.app`; start Server; launch the app |
| `make desktop` | Build only the Wails v3 desktop shell |
| `make verify` | `gofmt`, `go vet`, Web typecheck, Go + Web tests |

Local state lives in `~/.sumi/v2` (`sumi.db`, `objects/`, `server.log`, `server.pid`); override with `DATA_DIR=...`.

## Desktop shell

`desktop/` is a Wails v3 tray app that, at startup, probes `server_url` from its config, starts an in-process Server when nothing answers on `http://127.0.0.1:8087`, then starts the local Computer daemon and opens the Web UI straight from that origin. Closing the window hides it; the tray menu holds `Open Sumi`, the Computer status, `Restart Computer Daemon`, and `Quit`.

Attachments up to 25 MiB are stored content-addressed under `objects/` next to the database, so the editor and the agents share one upload path (`POST .../attachments` then `attachment_ids` on the message).
