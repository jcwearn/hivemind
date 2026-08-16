# Progress: hivemind

## Current Status: Live

| Phase | Status | Updated | Notes |
|-------|--------|---------|-------|
| 0. `go-ci.yaml` in jcwearn/workflows | Complete | 2026-08-15 | workflows#18, released as v1.6.0. This repo is its first consumer |
| 1. Service skeleton | Complete | 2026-08-15 | main.go, web server, health, Docker (8.4MB distroless), Makefile, CI |
| 2. Lobby engine | Complete | 2026-08-15 | Room actor, registry, SSE fan-out, signed cookies, inline-SVG QR |
| 3. The game | Complete | 2026-08-15 | Pure `internal/game`, tally, ramp, shared lives, phase machine |
| 4. Polish | Complete | 2026-08-15 | Three real layout defects found by finally looking at it; README GIF and stills (#2) |
| 5a. Resource limits | Complete | 2026-08-16 | #3 — required before any public exposure |
| 5b. Internal instance (k3s) | Complete | 2026-08-16 | k3s-cluster#723, tailnet only. Bump to 0.4.0 in k3s-cluster#724 |
| 5c. Public instance (Cloudflare Containers) | **Live** | 2026-08-16 | #5 — https://hivemind.jacksonwearn.com |
| 6. Second game + `Game` interface | Not Started | — | Deliberately deferred until there are two implementations |

## Handoff Notes

**Two instances, deliberately.** The public one runs on Cloudflare Containers
(`edge/`); the internal one runs on k3s and is reachable only from the tailnet.
Different zones, so they do not collide and nothing about the k3s side had to
change. They are separate processes with **separate rooms** — a code from one is
not joinable on the other. The internal one is staging.

**Verified live**, from a laptop through the real public hostname: TLS valid,
Cloudflare in front, `/healthz` refused publicly (404), the QR encodes the public
host, two players joined and voted, and SSE arrived at a **400ms median** — the
game's own tick interval, so nothing in the path batches it. Cold start 1.27s.

**Still not done by a human on real hardware:** scanning that QR with an actual
phone camera, over cellular, and playing a round on a television. Everything so
far has been driven from a laptop.

### Outstanding, and both need the Cloudflare dashboard

1. **Connect Workers Builds** so pushes deploy automatically (the Worker already
   exists, so it is "connect an existing Worker to a repo", root directory
   `edge/`). **The deploy command must be `wrangler deploy`** — the default
   `wrangler versions upload` uploads the Worker but does *not* update the
   container image, so a deploy would appear to succeed while running the
   previous container.
2. **Rate limiting rule on `POST /rooms` and Bot Fight Mode** on the
   `jacksonwearn.com` zone. Not urgent now that the caps exist, but it is the
   defence-in-depth layer.

### Things worth not relearning

- **Do not put the internal domain, LAN prefixes or the tailnet name in this
  repo.** It is public. One slipped into `docs/plans/` and a PR description and
  required a history rewrite to remove; the old content still lives in GitHub's
  immutable `refs/pull/*` refs, which only GitHub Support can purge.
- **Rewriting history closes open PRs.** #4 was auto-closed when its head SHA
  became unreachable, and GitHub refuses to reopen those. It became #5.
- **`wrangler delete` leaves the container application and image behind.** They
  need `wrangler containers delete` and `wrangler containers images delete`, and
  images count against the 50 GB account storage.
- **wrangler needs a modern Docker** (`docker build --load --provenance`).
  Docker 20.10 / buildx 0.8.2 fails with `unknown flag`. Workers Builds has a
  current one, which is the real answer.
- **Headless Chrome clamps its viewport to 500px** and then crops the screenshot
  to whatever width was asked for, which fabricates a convincing
  horizontal-overflow bug. Render inside an iframe of the target width instead.
- **`git fetch --tags` will not move a tag that already exists locally.** Use
  `--force`, or a correctly-moved tag looks unmoved.

### Measurements worth keeping

| | |
|---|---|
| SSE frame arrival (public) | min 210ms, **median 400ms**, max 525ms |
| Cold start / warm | 1.27s / 0.11s |
| 100 idle rooms, 10s | 0.14s CPU (was 1.43s before the render fix) |
| Five 10 MB POSTs | +0.6 MB RSS (was tens of MB per request) |
| Image | 8.4 MB, distroless, nonroot |

`sleepAfter` is safe for a game in progress: a stream held 200s against a 120s
`sleepAfter` kept the container alive, while going quiet for 210s put it to
sleep and dropped the rooms. An open connection counts as activity.
