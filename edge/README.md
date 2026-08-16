# edge

The Cloudflare Worker + Container wrapper that puts hivemind on the public
internet at **hivemind.jacksonwearn.com**.

The Go app is unchanged and unaware of any of this. All this directory does is
run the same image on Cloudflare's edge and hand it every request.

## Two instances, on purpose

| | Host | Runs on | Reachable from |
|---|---|---|---|
| **Public** | `hivemind.jacksonwearn.com` | Cloudflare Container | anywhere |
| **Internal** | a private hostname | k3s (`jcwearn/k3s-cluster`) | the tailnet |

They are **separate processes with separate rooms**. A code created on one is
not joinable on the other, and the QR encodes a single base URL — so whichever
instance the host opens is the one their guests have to reach. The internal one
is the staging environment and the fallback.

## Why one container instance

Every room lives in one process's memory. Two instances would be two disjoint
sets of rooms with requests landing on whichever the platform picked, so a host
would read a code off the television that half the room could not join.

Every request routes through a single Durable Object id (`"shared"`), which is
the exact equivalent of `replicas: 1` on the Kubernetes side.

## Deployment

Deployed by **Workers Builds** on push to `main`, the same Git integration
`jackson-wearn` and `cf-worker-email` use. Cloudflare's build environment has a
current Docker, so nothing local is needed.

> **The deploy command must be `wrangler deploy`.** Preview builds default to
> `wrangler versions upload`, which uploads the Worker but does **not** update
> the container image — a preview would silently run the previous container.

To deploy by hand instead:

```bash
cd edge && npm ci && npx wrangler deploy
```

That needs Docker locally. Note wrangler shells out to `docker build --load
--provenance`, which needs Docker 23+/buildx 0.10+; older installs fail with
`unknown flag`.

## The image is built twice

`release.yaml` publishes `ghcr.io/jcwearn/hivemind` for the k3s deployment, and
wrangler builds its own image for the container. **GHCR is not a registry
Cloudflare Containers can pull from** — the supported set is Cloudflare's own
registry, Docker Hub, Amazon ECR and Google Artifact Registry.

Both builds come from the same root `Dockerfile`, so they are equivalent, but
they are genuinely two builds and can drift if one is built from a different
commit. If that ever matters, the fix is to mirror GHCR to Docker Hub in
`release.yaml` and reference the image by tag here instead of by Dockerfile.

## Measurements behind the configuration

Taken against a throwaway deployment of this exact image before committing to
the design:

| | |
|---|---|
| SSE frame arrival | min 299ms, **median 400ms**, max 408ms |
| Cold start | 2.07s |
| Warm request | 0.11s |

The 400ms median is the game's own tick interval, so nothing in the path
batches SSE and `lite` (1/16 vCPU) sustains the tick without drift.

`sleepAfter` is safe for a game in progress: a stream held for 200s against a
120s `sleepAfter` kept the container alive, while going quiet for 210s put it to
sleep and dropped the rooms. **An open connection counts as activity.** The
value here is 15m — longer than the app's own ten-minute room idle timeout, so
the game decides when a room goes away rather than the platform.

## `/healthz` is refused publicly

The Worker returns 404 for `/healthz`, which otherwise reports the live room
count — a free "is my attack working" signal for anyone probing the room cap.

It is refused in code rather than with a WAF rule so it lives in the repo and
gets reviewed. The container's readiness probe is unaffected: `pingEndpoint`
reaches the container directly and never passes through the Worker.
