# Plan: hivemind — a Go + htmx party game, and a shared `go-ci.yaml`

## Context

Two goals, one project.

1. **A portfolio piece.** Every web project in `/Users/jackson/projects` is React + TypeScript on Cloudflare Pages. There is no Go web app, no htmx, no server-rendered UI, and no browser-facing realtime app anywhere in the tree. A Go + htmx realtime game is genuinely new ground and demonstrates a different set of skills than `borderline`.
2. **A gap in `jcwearn/workflows`.** There are `node-ci.yaml` and `python-ci.yaml` but no Go equivalent, so `agent-orchestrator`, `agent-operator`, and `alertmanager-ldap` each carry hand-rolled, drifting CI (agent-operator's pins are already a generation behind). A shared `go-ci.yaml` fixes that for every Go repo, and hivemind becomes its first consumer.

**hivemind** is Jackbox-style: one shared big screen, everyone joins from their phone by scanning a QR code, and *everybody steers the same snake at once*. Each tick the server tallies every player's vote and the plurality direction wins. It's chaos, it's instantly legible to a stranger looking at the README, and — critically — the shared-screen state is a small grid that renders as HTML, which is what makes htmx the right tool rather than a gimmick.

Decisions already made: `templ` for rendering, near-zero dependencies, homelab k3s + Cloudflare Tunnel for hosting, one game first with the multi-game interface deferred.

---

## Architecture

### Transport: SSE down, HTTP POST up

The single most important design decision, and the thing to lead with in the README.

- **Server → client: SSE.** htmx's SSE extension is first-class and maintained; the WebSocket extension is not. An `EventSource` is plain HTTP, so it traverses Cloudflare Tunnel with zero special configuration, reconnects automatically, and is trivially testable — an SSE stream is just an `httptest` response body you read frames from.
- **Client → server: ordinary `hx-post`.** Player input is low-frequency discrete events (a vote, a join, a start). A form POST returning an HTML fragment is exactly what htmx is for.

This is a deliberate rejection of the `gorilla/websocket` hub in `agent-orchestrator` — that hub is a global broadcast map with no rooms, synchronous writes under a shared mutex, no ping/pong, no write deadlines, and `CheckOrigin` returning unconditional `true`. **Do not port it.** Take only the shape of its `Register`/`Unregister` pair.

### One goroutine per room (single-writer actor)

The headline Go story. A room owns all of its state in plain struct fields; every mutation arrives on a channel; nothing else ever touches it. **No mutex guards any game state.**

```go
// internal/lobby/room.go
func (r *Room) run(ctx context.Context) {
    tick := time.NewTicker(r.tickInterval)
    defer tick.Stop()
    for {
        select {
        case <-ctx.Done():   return
        case c := <-r.cmds:  r.apply(c)   // join, vote, start, subscribe, unsubscribe
        case <-tick.C:       r.step()     // tally -> advance -> render -> broadcast
        case <-r.idle.C:     return       // GC empty rooms
        }
    }
}
```

The only lock in the program is a `sync.RWMutex` in `lobby.Registry` guarding `map[string]*Room` — it guards the map, never game state. Say so plainly in the README rather than overclaiming "lock-free".

### Render once, broadcast N times

`step()` renders the board fragment to a single `[]byte` and hands that same slice to every subscriber. Rendering is O(1) in player count, not O(n).

Each subscriber gets a buffered `chan []byte` (cap ~4). If the buffer is full, **drop the frame** rather than blocking the room goroutine:

```go
frame := r.renderFrame()          // rendered ONCE per tick
for s := range r.screens {
    select {
    case s.ch <- frame:
    default:
        r.dropped++               // slow client; next snapshot heals it
    }
}
```

This is safe precisely because every frame is a **full state snapshot, not a delta** — a dropped frame costs nothing. That trade (snapshots over deltas, at a 20×20 grid) is the reason a slow phone can never stall the game for everyone else, which is the exact failure mode the agent-orchestrator hub has.

### Pure game logic, injected time

`internal/game` is a pure package: `Step(State, map[PlayerID]Direction) State`. No I/O, no clock, no randomness except an injected `*rand.Rand`. The tick loop in `internal/lobby` owns time; the rules own nothing else. This makes the entire game exhaustively table-testable with no fakes.

### Identity and reconnect

No accounts, no database, no persistence of any kind. A player gets an HMAC-signed cookie holding a random `playerID`; a phone that locks and wakes rejoins the same seat. Rooms are garbage-collected after an idle timeout. The container is fully stateless and trivially redeployable — a documented choice, not an omission. High-score persistence is explicitly out of scope for v1.

---

## Layout

```
hivemind/
  cmd/hivemind/main.go        wiring, slog, graceful shutdown
  internal/game/              pure rules: state, step, collision, ramp  (no I/O)
  internal/lobby/             registry, room actor, subscribers, room codes
  internal/web/               mux, handlers, SSE, signed cookies
  internal/ui/                *.templ components + committed *_templ.go
  static/                     htmx + sse ext (vendored, SRI-pinned), styles.css
  docs/plans/hivemind/        plan.md + progress.md  (per rules/planning.md)
  Dockerfile  .dockerignore  Makefile  .golangci.yml  .golangci-lint-version
  renovate.json
```

**Routes** (Go 1.25 `http.ServeMux` method+wildcard patterns — no chi):

| Route | Purpose |
|---|---|
| `GET /` | landing: host a game, or enter a code |
| `POST /rooms` | create room → 303 to screen |
| `GET /r/{code}/screen` | big screen: board, QR, roster, live vote tally |
| `GET /r/{code}/screen/events` | SSE: board + votes + roster frames |
| `GET /j/{code}` · `POST /j/{code}` | phone join, name entry, sets signed cookie |
| `GET /r/{code}/play` | controller: four direction buttons |
| `GET /r/{code}/play/events` | SSE: per-player state (your vote, lives, phase) |
| `POST /r/{code}/vote` | cast vote, returns updated button fragment |
| `POST /r/{code}/start` | host starts the round |
| `GET /healthz` | liveness |

**Room codes**: 4 chars from an ambiguity-free alphabet (`ABCDEFGHJKMNPQRSTVWXYZ23456789` — no I/L/O/U/0/1), case-insensitive on input. 810k combinations, collision-checked against the registry.

**Dependencies** — the whole of `go.mod`:

- `github.com/a-h/templ` (runtime)
- `rsc.io/qr` — exposes `Code.Size` and `Code.Black(x, y)`, so the join QR renders as **inline SVG from a templ component**. No image encoding, no data URIs, crisp on a projector.
- `tool` directives: `github.com/a-h/templ/cmd/templ` (codegen), pinning the version in `go.mod` where Renovate bumps it.

Routing, SSE, templating, and testing are all stdlib.

---

## Phase 0 — `go-ci.yaml` in `jcwearn/workflows`

**Do this first and merge it first**; hivemind can't reference `@v1` until it's tagged. Separate repo, separate PR, `release:minor` label (a brand-new workflow file is minor per `README.md` lines 44–64).

Note the naming split, which is easy to get backwards: the **reusable** workflow is `<lang>-ci.yaml`, the **caller template** is `templates/ci-<lang>.yaml`. Both are needed.

**`.github/workflows/go-ci.yaml`** — pattern it directly on `python-ci.yaml`, which is the closest analogue. Match the house style exactly: a long `CONTRACT, not inputs` header explaining what the repo must provide and which tool enforces each line; `permissions: contents: read`; `concurrency: group: go-ci-${{ github.workflow }}-${{ github.ref }}` with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`; a single `check` job; actions pinned by 40-char SHA with a trailing `# vN`; `set -euo pipefail` in every `run:`; em-dash-free prose using `--`.

Contract:

| Requirement | Enforced by |
|---|---|
| `go.mod` | `setup-go` with `go-version-file: go.mod` |
| `.golangci-lint-version`, pinned exact | `golangci-lint-action` `version-file:` — errors without it |
| `.golangci.yml` | the repo's own linter config |
| tests discoverable by `go test ./...` | `go test` |

The `.golangci-lint-version` file is the important design point and the reason this fits the house doctrine cleanly. `golangci-lint-action` **does not** read `go.mod` `tool` directives, and its README calls `install-mode: goinstall` not recommended — but it does support `version-file`, accepting `.golangci-lint-version`. That makes the linter version a fact the *repo* declares, in a file Renovate bumps, exactly like `.node-version` and `.python-version` in the sibling workflows. The workflow never decides a tool version.

Steps: checkout (`persist-credentials: false`) → `setup-go` (`go-version-file: go.mod`) → **tidy drift check** → lint → format → test → build.

- **Tidy drift**: `go mod tidy` then `git diff --exit-code go.mod go.sum`. Include it, and document *why* in the header: this is the `npm install --package-lock-only` case, not the `uv lock` case. With a complete `go.sum`, `go mod tidy` resolves from the module cache and does not upgrade versions, so it's a pure function of the tree and cannot go red on a repo nobody touched.
- **Lint**: `golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9` with `version-file: .golangci-lint-version`. It emits inline PR annotations by default, which is the `--output-format=github` equivalent in `python-ci.yaml`.
- **Format**: a separate step, per house doctrine that a formatting failure reading as a lint failure sends people to the wrong place. The action installs the binary onto `PATH`, so a plain `run: golangci-lint fmt --diff` follows it. *Verify at implementation time that the action's binary install mode does export `PATH`;* fall back to `test -z "$(gofmt -l .)"` if not.
- **Test**: `go test -race ./...`. The race detector matters enormously for a project whose whole thesis is a concurrent actor model. Note that `-race` needs `CGO_ENABLED=1` (fine on `ubuntu-latest`) even though the release build is `CGO_ENABLED=0`.
- **Build**: `go build ./...` as a cheap sanity check.

Job name `Lint, format, test, build`; only input `timeout-minutes` (default 10).

**`templates/ci-go.yaml`** — mirror `templates/ci-node.yaml`: `on: push[main] / pull_request / workflow_dispatch`, explicit `permissions: contents: read` on the job, `uses: jcwearn/workflows/.github/workflows/go-ci.yaml@v1` with no trailing comment (Renovate rewrites it to a digest + `# v1`), and the closing comment that bespoke steps get their own job or file.

**Also**: a `## go-ci.yaml` section in `README.md` matching the node/python sections at lines 275 and 374 (Usage / Inputs / The contract). `actionlint` in `ci.yaml` lints `templates/*.yaml` too, so the template must pass.

**Flag for the user**: `.golangci-lint-version` needs a Renovate custom manager in `jcwearn/renovate-config` to get bumped automatically. Worth a follow-up; not a blocker.

---

## Phase 1 — Service skeleton

New repo `jcwearn/hivemind`, module `github.com/jcwearn/hivemind`, Go 1.25.7.

`cmd/hivemind/main.go` — lift these three patterns from `agent-orchestrator/cmd/main.go` essentially verbatim; they're the best code in that repo:

- slog JSON handler, level from env with graceful fallback, `*slog.Logger` injected into every constructor, no globals.
- `signal.NotifyContext(ctx, SIGINT, SIGTERM)` + `httpSrv.Shutdown` with a 10s timeout.
- `/healthz` returning a real check, not a static 200.

**One shutdown subtlety to get right.** `agent-orchestrator` has the problem that `Shutdown` never waits on hijacked WebSocket conns. With SSE the failure is the mirror image: an SSE response is an *ordinary* response, so `Shutdown` **will** block on every open stream until the timeout expires. Handle it explicitly — close a `done` channel that every SSE handler `select`s on, so streams end promptly and shutdown is fast.

Also in this phase: landing page and templ setup, `Makefile` (`build test lint fmt generate run docker-build` — fuller than agent-orchestrator's thin one), `.golangci.yml` (agent-orchestrator has *none*, and parts of that tree aren't `gofmt`-clean — don't inherit that gap), `.dockerignore` using the deny-all-then-allow pattern, `renovate.json` copied from agent-orchestrator (`config:best-practices`, `gomodTidy`, auto `release:patch` label), and `docs/plans/hivemind/{plan.md,progress.md}`.

**Dockerfile** — two stages, no Node stage, since `*_templ.go` is committed and the image needs no codegen:

```dockerfile
FROM golang:1.25@sha256:... AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY static/ static/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hivemind ./cmd/hivemind

FROM gcr.io/distroless/static-debian12:nonroot@sha256:...
COPY --from=build /hivemind /hivemind
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/hivemind"]
```

Distroless, not `debian:bookworm-slim` — agent-orchestrator only uses Debian because it needs the Coder CLI at runtime, and its `curl -fsSL https://coder.com/install.sh | sh` layer is an unpinned supply-chain hole worth not copying.

**CI wiring** — three workflow files, all calling `jcwearn/workflows`:

- `ci.yml` ← `templates/ci-go.yaml`
- `build-image.yml` ← `templates/ci-docker.yaml`
- `release.yml` + `require-release-label.yml` ← `release.yaml`, `with: image: ghcr.io/jcwearn/hivemind`, `permissions: contents: write` + `packages: write`

Plus **one bespoke local workflow**, `codegen.yml`, running `go tool templ generate && git diff --exit-code`. This deliberately does *not* go in the shared `go-ci.yaml` — templ is hivemind's choice, not every Go repo's, and `templates/ci-node.yaml`'s closing comment establishes the precedent that bespoke steps get their own file.

## Phase 2 — Lobby engine

`internal/lobby`: `Registry` (mutex-guarded `map[string]*Room`, code generation, collision check, idle GC), `Room` actor and command types, subscriber fan-out with buffered channels and frame-dropping, signed-cookie player identity and reconnect.

`internal/web`: SSE handler writing `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, an explicit `http.Flusher` assertion, and a **heartbeat comment (`:ping\n\n`) every ~20s** — required to survive Cloudflare's idle timeout. Join, roster, and screen handlers. Inline-SVG QR component.

Deliverable: multiple phones join a room and appear on the big screen roster live.

`agent-orchestrator/internal/coder/workspace.go`'s `Pool` (sentinel errors, snapshot-returning `Status()`, clean separation of bookkeeping from side effects) is the best in-memory primitive in that repo and maps well onto seat assignment — worth reading before writing `Registry`.

## Phase 3 — The game

`internal/game`: grid, snake, food, `Direction`, `Step(State, votes) State`, plurality tally (ties keep the current heading; 180° reversals filtered so the snake can't eat its own neck), hard walls, 3 shared lives with a respawn countdown, tick ramp 400ms → 120ms as score climbs, phase machine `Lobby → Countdown → Playing → GameOver`.

`internal/ui`: board component, live vote-tally bars on both screen and phone, controller buttons with optimistic highlight.

Deliverable: a playable game end to end.

## Phase 4 — Polish

Arcade/CRT styling, mobile ergonomics (large touch targets, no zoom on tap, viewport meta), spectator mode for late joiners, an empty-room and error state, and a README with an animated GIF of a real session. Vendor htmx + the SSE extension into `static/` with SRI-pinned versions rather than a CDN — it keeps the single-binary story intact and matches the near-zero-dependency thesis.

## Phase 5 — Ship it

**This phase changed materially during execution.** The original plan put the
public instance on the k3s homelab behind Cloudflare Tunnel. What shipped
instead is two instances, and the reasoning is worth keeping:

1. Merge + tag Phase 0, then merge a `release:minor` PR on hivemind →
   `release.yaml` publishes `ghcr.io/jcwearn/hivemind:{1.2.3,1.2,1,sha-abc1234}`.
2. **Internal instance — PR to `jcwearn/k3s-cluster`**: Deployment, Service,
   HTTPRoute and FluxCD wiring, reachable from the tailnet only.
   - **`replicas: 1`, `strategy: Recreate`.** Non-negotiable: room state is
     in-memory, so two pods must never serve simultaneously.
   - A `BackendTrafficPolicy` raising Envoy's timeouts, because its 300s default
     would sever every SSE stream mid-round on a five-minute cadence.
3. **Public instance — Cloudflare Containers** (`edge/`), not Cloudflare Tunnel
   and not Tailscale Funnel.
   - Funnel was ruled out empirically: it serves TLS only for its own `.ts.net`
     name, and connecting to the relay with a different SNI returns
     `no peer certificate available` — a hard handshake failure. Reaching a
     custom hostname would have required Cloudflare to proxy it anyway.
   - Containers keeps the homelab out of the public path entirely and makes the
     link independent of home uptime, which is the better property for something
     linked from a portfolio.
   - Gated on a throwaway spike first, because two behaviours were undocumented
     and either would have sunk the design: whether SSE survives the
     Worker → Durable Object → container path, and whether an open stream keeps
     the container awake. Both passed; see the measurements in `edge/README.md`.
4. Add hivemind to the portfolio page in `jcwearn/jackson-wearn`.

## Phase 6 — Later, only if it earns it

Add a second game (Committee Tetris or a bomb-party word game), and **extract the `Game` interface at that point** — when two real implementations justify the abstraction rather than before. This is why v1 ships one game: per `rules/code-quality.md`, an interface designed against a single implementation is a guess.

---

## Verification

**Unit** — `go test -race ./...`

- `internal/game`: table-driven, deterministic, no fakes. Vote tallying incl. ties and reversals, wall and self collision, food and growth, tick ramp, life loss and respawn, phase transitions.
- `internal/lobby`: drive the room actor through its command channel with an injected clock. Assert join/leave/reconnect, code collision handling, idle GC, and — importantly — that **a subscriber that never drains its channel does not block the room goroutine**. That test is the one that proves the central claim.

**Integration** — `httptest.NewServer(h.Routes())`, lifting the harness shape from `agent-orchestrator/internal/server/server_test.go` (real server + cookie jar client, `t.Cleanup`). Assert: create room → join from two clients → both appear on the screen SSE stream → both vote → the board frame advances in the tallied direction. Read SSE frames directly off the response body with a `bufio.Scanner`.

**Manual, the part that actually matters** — run `make run`, open the screen on a laptop, scan the QR with two real phones on the same network, and play a round. Then confirm a phone that locks and wakes rejoins its seat. Verify with `docker run` against the built image before deploying.

**Post-deploy** — hit the public hostname from cellular (not just LAN) and confirm the SSE stream stays open past 100 seconds. That's the specific thing the heartbeat exists to prove, and the failure mode Cloudflare would otherwise introduce.

---

## Open items to confirm during implementation

- Whether `golangci-lint-action`'s binary install exports `PATH` for a following `golangci-lint fmt --diff` step (fall back to `gofmt -l`).
- Renovate custom manager for `.golangci-lint-version` in `jcwearn/renovate-config`.
- Which domain is the right home for the public instance.

## Git workflow note

This touches **three repos**, each needing its own branch and PR per `rules/git-workflow.md`, merged in order: `jcwearn/workflows` (Phase 0, must be tagged first) → `jcwearn/hivemind` (Phases 1–4) → `jcwearn/k3s-cluster` (Phase 5). Every PR needs a `release:*` label.
