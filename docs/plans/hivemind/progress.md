# Progress: hivemind

## Current Status: In Progress

| Phase | Status | Updated | Notes |
|-------|--------|---------|-------|
| 0. `go-ci.yaml` in jcwearn/workflows | Awaiting merge | 2026-08-15 | PR jcwearn/workflows#18, `release:minor`. All checks green. Must merge AND tag before this repo's `ci.yml` can resolve `@v1` |
| 1. Service skeleton | Complete | 2026-08-15 | main.go, web server, health, Docker (8.4MB distroless), Makefile, CI wiring |
| 2. Lobby engine | Complete | 2026-08-15 | Room actor, registry, SSE fan-out, signed cookies, inline-SVG QR |
| 3. The game | Complete | 2026-08-15 | Pure `internal/game`, tally, ramp, shared lives, phase machine |
| 4. Polish | Not Started | — | Arcade styling exists; still wants a README GIF and a spectator/late-join pass |
| 5. Deploy to k3s + Tunnel | Not Started | — | Separate PR to jcwearn/k3s-cluster |
| 6. Second game + `Game` interface | Not Started | — | Deliberately deferred until there are two implementations |

## Handoff Notes

**Phase 0 is the blocker.** `ci.yml`, `build-image.yml`, `release.yml` and
`require-release-label.yml` all reference `jcwearn/workflows/...@v1`. The `v1`
tag currently points at v1.5.1, which predates `go-ci.yaml`, so CI on this repo
will fail with "workflow not found" until workflows#18 is merged and the release
job moves the tag. Nothing else is waiting on anything.

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
