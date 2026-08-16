// The edge wrapper for hivemind.
//
// hivemind is a Go server that keeps every room in memory. This Worker's only
// job is to put it on the public internet at hivemind.jacksonwearn.com and hand
// every request to one container instance.
//
// Why one instance: a room code is meaningless to a process that does not hold
// that room. Two container instances would be two disjoint sets of rooms with
// requests landing on whichever one the platform chose, so a host would read a
// code off the television that half the room could not join. Routing every
// request through a single Durable Object id is the exact equivalent of
// replicas: 1 on the Kubernetes side.

import { Container, getContainer } from "@cloudflare/containers";

export class HivemindContainer extends Container<Env> {
  // The port the Go server listens on.
  defaultPort = 8080;

  // hivemind serves /healthz, not the default /ping. Without this the startup
  // probe never passes and the container is never marked ready.
  //
  // This probe reaches the container directly rather than through the Worker,
  // which is why the fetch handler below can refuse /healthz publicly without
  // breaking readiness.
  pingEndpoint = "/healthz";

  // hivemind makes no outbound calls at all: no DNS, no upstreams, no
  // telemetry. Everything the browser needs is embedded in the binary. Turning
  // egress off costs nothing and removes the whole category.
  enableInternet = false;

  // Comfortably longer than the app's own ten-minute room idle timeout, so the
  // game decides when a room goes away rather than the platform pulling the
  // floor out from under it.
  //
  // An open connection counts as activity -- measured: a stream held for 200s
  // against a 120s sleepAfter kept the container alive, while going quiet for
  // 210s put it to sleep. So a game in progress is never at risk; this value
  // only governs how long an empty container lingers before it stops costing
  // anything.
  sleepAfter = "15m";

  envVars = {
    // This is what the join QR encodes. It has to be the public hostname --
    // anything else and the television looks perfect while every scan fails.
    HIVEMIND_BASE_URL: "https://hivemind.jacksonwearn.com",
    HIVEMIND_LOG_LEVEL: "info",
    // HIVEMIND_COOKIE_SECRET is deliberately unset. It signs the cookie that
    // lets a phone reclaim its seat, and the app mints an ephemeral one per
    // process. A durable secret would only matter if a seat could outlive the
    // process, and it cannot: anything that restarts the container has already
    // destroyed every room the cookie could point at.
  };

  override onStart() {
    console.log("hivemind container started");
  }

  override onStop({ exitCode, reason }: { exitCode: number; reason: string }) {
    // Every live room dies with the container, by design. Worth logging, since
    // it is the difference between "the game ended" and "the game vanished".
    console.log("hivemind container stopped", { exitCode, reason });
  }

  override onError(error: unknown) {
    console.error("hivemind container error", String(error));
  }
}

interface Env {
  HIVEMIND: DurableObjectNamespace<HivemindContainer>;
}

// One id, so one container, so one set of rooms.
const INSTANCE = "shared";

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    // /healthz reports the live room count. That is a free "is my attack
    // working" signal for anyone flooding the room cap, and nothing on the
    // public internet has a reason to read it.
    //
    // Refused here rather than with a WAF rule so it lives in the repo and gets
    // reviewed. The container's own readiness probe is unaffected -- it talks
    // to the container directly and never passes through this handler.
    if (url.pathname === "/healthz") {
      return new Response("not found", { status: 404 });
    }

    try {
      return await getContainer(env.HIVEMIND, INSTANCE).fetch(request);
    } catch (error) {
      // Most likely a cold start that has not finished, or an instance being
      // replaced. Say so plainly and ask for a retry rather than surfacing a
      // stack trace to somebody who just wanted to play a game.
      console.error("container fetch failed", String(error));
      return new Response("hivemind is starting up, try again in a moment.", {
        status: 503,
        headers: { "Retry-After": "5", "Content-Type": "text/plain" },
      });
    }
  },
};
