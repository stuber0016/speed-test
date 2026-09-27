# Speed Test PoC

This PoC has two Go services with no dependencies outside the standard library:

- **Client service**: runs speed tests and exposes them over an HTTP API. You drive it from a CLI (`stctl`) or a small web page.
- **Target service**: the other end of the test. It serves bytes, accepts bytes and answers pings.

## Run it

You need Docker with Compose v2. Go is only needed to run the tests.

```sh
make up          # build and start both services, wait until they're healthy
make speedtest   # run a test from the terminal and watch it live
```

Other targets:

- `make list`: recent tests. `make get ID=<id>` shows one.
- `make down`: stop everything.
- `make keys`: rotate the secrets.
- `make test`: run the Go tests.

The CLI (`stctl`) ships inside the client image, so these targets run it with `docker compose exec` in the client container that's already running. No extra container starts, and the API key is already in that container's environment.

The web UI is at **http://localhost:8080**. Paste `CLIENT_API_KEY` from `.env` into it.

Plain `docker compose up --build` works too. `make` only wraps it.

Tune a run with `ARGS`:

```sh
make speedtest ARGS="-streams 8 -duration 15 -warmup 2 -pings 50"
```

**Secrets** live in `.env`, which compose reads automatically, and `stctl` reads it too. It ships with obviously fake dev values so the stack runs with zero setup. `make keys` swaps them for random 256-bit tokens, then `make up` restarts the services with them. No key is ever typed on a command line.

**Ports:** if 8080 or 8081 is already taken on your machine, add `CLIENT_PORT=18080` and `TARGET_PORT=18081` (or any free ports) to `.env`. Compose, `make` and the natively run binaries all pick them up.

<details>
<summary>Without Docker</summary>

```sh
set -a; . ./.env; set +a
go run ./cmd/target &
go run ./cmd/client &
go run ./cmd/stctl run
```
</details>

<details>
<summary>Raw API (curl)</summary>

```sh
set -a; . ./.env; set +a
curl -s -X POST -H "Authorization: Bearer $CLIENT_API_KEY" localhost:8080/api/tests \
     -d '{"streams":4,"duration_s":8}'                     # → 202 {"id": "...", ...}
curl -s -H "Authorization: Bearer $CLIENT_API_KEY" localhost:8080/api/tests/<id>
curl -s -H "Authorization: Bearer $CLIENT_API_KEY" localhost:8080/api/tests
```
</details>

## Architecture

```
 controller                    Client service (:8080)                  Target service (:8081)
┌───────────┐  CLIENT_API_KEY ┌──────────────────────────┐ TARGET_TOKEN ┌──────────────────┐
│ stctl CLI │ ──────────────▶ │ HTTP API    /api/tests   │ ───────────▶ │ GET  /ping       │
│ web page  │  POST, poll GET │ Store (in-memory, 20)    │              │ GET  /download   │
└───────────┘                 │ Runner: latency → down → │              │ POST /upload     │
                              │         up, N streams    │              │ GET  /healthz    │
                              └──────────────────────────┘              └──────────────────┘
```

```
cmd/target, cmd/client, cmd/stctl   three binaries; one Dockerfile builds a target image and a client image (the client image also carries stctl)
internal/protocol                   the Client↔Target contract (paths, limits, JSON types), shared by both sides
internal/target                     Target handlers
internal/client                     Runner (measurement), stats, Store, API, embedded web page
internal/auth                       bearer-token middleware
internal/service                    env, graceful shutdown, container healthcheck
```

### Client ↔ Target contract

Every endpoint except `/healthz` requires `Authorization: Bearer <TARGET_TOKEN>`.

| Endpoint | Behaviour |
|---|---|
| `GET /ping` | Returns `204`. Used for latency. |
| `GET /download?bytes=N` | Streams exactly N bytes of random data. N must be in 1…2 GiB. `Cache-Control: no-store`. |
| `POST /upload` | Reads and discards the body (≤ 2 GiB), then replies `{"bytes_received": N, "duration_ms": D}`. |
| `GET /healthz` | Returns `200`. Liveness only. |

Errors come back as `{"error": "..."}`. The Target keeps no state and does no measuring. All measurement logic lives in the Client, so you can swap or scale Targets without changing how the numbers are computed.

### Client API (for controllers)

Every endpoint under `/api/` requires `Authorization: Bearer <CLIENT_API_KEY>`.

| Endpoint | Behaviour |
|---|---|
| `POST /api/tests` | Starts a test. The body is optional: `{streams, duration_s, warmup_s, pings}`. Returns `202` with the test, `409` if one is already running, or `400` for a bad config. |
| `GET /api/tests/{id}` | Returns `status` (`running`/`done`/`failed`), the current `phase`, live `progress`, and the `latency`, `download` and `upload` results as each one finishes. |
| `GET /api/tests` | Returns the last 20 tests, newest first. |
| `GET /` | The web page. It's public, and the page sends the key itself. |

Tests are **asynchronous**. A test takes about 20s, which is too long to hold an HTTP request open, so the controller starts one and polls. **Only one test runs at a time**, because two tests would share the same link and each would measure about half of it.

### How measurement works

- **Latency**:
  - Two unmeasured pings open and warm a keep-alive connection.
  - Then N sequential pings are timed.
  - The result is RTT plus a little HTTP overhead, without the TCP handshake.
  - Reported as median, min, avg, p95 and max. Jitter is the mean absolute difference between consecutive samples.
- **Download**:
  - N parallel streams (default 4) each fetch 2 GiB blobs in a loop, adding to a shared atomic byte counter.
  - The counter is sampled every 250ms.
  - The first `warmup_s` (default 1s) is dropped to skip TCP slow start.
  - Throughput = bytes in the window / window length, in Mbit/s (10⁶).
  - At the deadline the Client cancels the requests.
- **Upload**:
  - Uses the same sampling. Each stream POSTs a body that ends itself (EOF) at the deadline, so every request finishes cleanly and the Target reports what it received.
  - The headline number comes from bytes counted on the client side in the post-warm-up window. Once buffers fill up, bytes written by the client and bytes received by the server grow at the same rate, and the warm-up cut already removes the initial rush of bytes into buffers.
  - `server_confirmed_bytes` (the Target's total, warm-up included) is a cross-check that data really arrived. The integration test asserts it matches the total the client sent.
- Why multiple streams: one TCP stream often can't fill a long, fat link. Parallel streams are what Ookla-style tools do.

## Auth: what and why

- **Two static bearer tokens, one per trust boundary**:
  - `CLIENT_API_KEY` guards controller → Client.
  - `TARGET_TOKEN` guards Client → Target.
- Tokens are compared in constant time.
- Controllers never hold the Target token, so a leaked UI key can't be used to push traffic at a Target directly.
- Why this over the alternatives, for a PoC:
  - One operator runs both sides. There's no need yet for per-user identity, scopes or expiry.
  - Bearer tokens are trivial to use from curl, a browser and Go alike.
  - It maps directly onto what production would use. The header stays the same and only where the token comes from changes: short-lived signed tokens (JWT/HMAC) issued by a control plane, or mTLS between services.
- **Caveat:** the traffic is plain HTTP, so the tokens travel in cleartext. That's fine on a Docker network or localhost, and not fine over the internet. TLS is the first item under "next".

## Assumptions and simplifications

- **The Docker bridge gives unrealistic numbers** (tens of Gbit/s, sub-0.1ms latency) because nothing leaves the host. That's expected: the PoC proves the mechanism, not a network. For meaningful numbers, run the Target on another machine and point the Client at it with `TARGET_URL=http://host:8081`.
- **One Target per Client**, set by `TARGET_URL` at startup.
- **No TLS** (see Auth). Static tokens with no rotation other than a restart.
- **State is in memory** (last 20 tests). A restart loses history, and a restart mid-test loses that test.
- **Polling, not push.** 300–400ms polling is plenty for a PoC.
- **HTTP/1.1 over TCP only.** No UDP, no packet-loss metric, no loaded latency (bufferbloat).
- **Fixed-duration phases**, where Ookla-style tools adapt stream count and duration to link speed. The defaults (4 streams, 8s, 1s warm-up) are reasonable for anything from tens of Mbit/s up to multi-Gbit/s.
- **No rate limiting or abuse protection on the Target** beyond the token and 2 GiB-per-request caps.
- If you Ctrl+C `stctl run`, only the CLI stops. The test finishes on the server, and `stctl get <id>` shows it. There's no cancel endpoint.
- Timestamps in `stctl` show in the container's timezone (UTC) when run via compose.

## Possible next extensions

1. **TLS on both hops, then mTLS or short-lived signed tokens** between Client and Target. That makes it safe to put a Target on the public internet.
2. **Pick a Target per test, and support multiple Targets**: a registry, choosing the nearest by latency, `target` in `POST /api/tests`.
3. **Measurement quality:**
   - Adapt stream count and duration to link speed.
   - Measure latency under load (bufferbloat).
   - Trim outliers instead of only cutting the warm-up.
   - Report the TCP_INFO RTT and retransmits.
4. **Persist results** (SQLite or Postgres) and add a cancel endpoint.
5. **Server-sent events** for live progress instead of polling.
6. **Protect the Target**: per-token rate and concurrency limits, plus Prometheus metrics on both services.
7. Load-test the Target to see how many concurrent tests one instance can serve before it becomes the bottleneck.
