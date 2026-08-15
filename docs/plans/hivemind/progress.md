# Progress: hivemind

## Current Status: In Progress

| Phase | Status | Updated | Notes |
|-------|--------|---------|-------|
| 0. `go-ci.yaml` in jcwearn/workflows | Complete | 2026-08-15 | workflows#18 merged, released as v1.6.0, `v1` moved. This repo is its first consumer and CI is green |
| 1. Service skeleton | Complete | 2026-08-15 | main.go, web server, health, Docker (8.4MB distroless), Makefile, CI wiring |
| 2. Lobby engine | Complete | 2026-08-15 | Room actor, registry, SSE fan-out, signed cookies, inline-SVG QR |
| 3. The game | Complete | 2026-08-15 | Pure `internal/game`, tally, ramp, shared lives, phase machine |
| 4. Polish | Complete | 2026-08-15 | Three real layout defects found and fixed by finally looking at it; README now carries a recorded GIF and two stills (hivemind#2) |
| 5. Deploy to k3s | Awaiting merge | 2026-08-15 | k3s-cluster#723. NOT Cloudflare Tunnel -- the cluster uses Envoy Gateway + external-dns. Blocked on making the GHCR package public |
| 6. Second game + `Game` interface | Not Started | — | Deliberately deferred until there are two implementations |

## Handoff Notes

**Phase 0 is done.** workflows#18 merged as v1.6.0 and the `v1` tag moved, so
all four checks here are green. The shared `go-ci.yaml` was confirmed working on
its first real consumer: the run log shows it reading `.golangci-lint-version`
("Finding needed golangci-lint version... Installing golangci-lint binary
v2.12.2"), which is the mechanism the whole contract rests on.

**Verified working end to end**, both via `go run` and from the built image:
create room → join from two clients → both vote → the tally updates on the
shared screen → start → the snake steers where the room voted. Confirmed the
held-vote model actually steers by watching the head climb one row per tick.

**Graceful shutdown measured at 0.08s with two live SSE streams open.** This is
the thing worth not regressing: `web.Server.Shutdown()` must run *before*
`http.Server.Shutdown()`. An SSE response is an ordinary response, not a
hijacked connection, so net/http waits for each handler to return on its own —
and a healthy stream never does. Reversing the order costs the full 10s timeout
on every connected phone.

**Now verified visually, and it mattered.** Freezing a rendered page (splicing a
captured SSE frame in and neutralising `sse-connect`) makes the app
screenshottable — headless Chrome otherwise hangs forever waiting on the stream
for network-idle. Doing that found three real defects: an invisible board, the
vote tally pushed below the fold, and a board that had stopped being square.
All fixed in hivemind#2.

Two traps worth knowing for next time. Headless Chrome clamps its viewport to a
**500px minimum** and then crops the screenshot to whatever `--window-size`
asked for, which fabricates a convincing horizontal-overflow bug on any phone
render -- use an iframe of the target width instead. And `git fetch --tags` will
not move a tag that already exists locally, which made a correctly-moved `v1`
look unmoved; `--force` is required.

**Still not done by a human on real hardware:** `make party`, scanned with two
actual phones, on an actual television.

**Decisions made along the way:**

- Votes *persist* until changed rather than clearing each tick. At up to eight
  ticks a second the alternative is a game about tapping fast, which is worse
  and unkind to old phones.
- A tie keeps the current heading rather than breaking randomly, so a deadlocked
  room behaves predictably.
- Lives are a shared pool, not per player — eliminating people one at a time
  leaves the funniest participants watching.
- The controller's buttons sit outside the SSE-swapped region so a swap can't
  fight a thumb mid-press.
- No separate `Format` CI step: golangci-lint v2 reports `formatters:` findings
  from the same `run`, tagged with the formatter, so it already reads correctly.

**Coverage:** game 93.5%, lobby 88.8%, web 80.5%. The `ui` figure is meaningless
— generated templ code dominates the denominator.

**Next session:** merge workflows#18 first, then confirm this repo's CI goes
green. Phase 4 (README GIF, late-join spectator view) and Phase 5 (k3s manifests
in a separate PR to jcwearn/k3s-cluster, plus the Cloudflare Tunnel hostname for
hivemind.internal.invalid) are both unstarted.
