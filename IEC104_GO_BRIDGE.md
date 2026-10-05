IEC104_GO_BRIDGE.md 

# IEC 104 bridge: what the Go service must implement

The MSL5 backend no longer talks gRPC to the external IEC 104 Go service. It
**spawns the Go binary as a child process** and talks to it over
**stdin/stdout using newline-delimited JSON (NDJSON)**. This document lists
everything that must change in the Go repository (`iec104_client`). The
backend side lives in `apps/backend/src/iec104/bridge/`.

## 1. Process model

- The backend starts the binary with `IEC104_BRIDGE_BIN` and the arguments in
  `IEC104_BRIDGE_ARGS` (a JSON string array, e.g. `["-host","127.0.0.1","-port","2404"]`,
  or whitespace-separated flags). No shell is involved.
- **stdin** carries requests from the backend, **stdout** carries *only*
  protocol lines, **stderr** is the log channel (the backend forwards every
  stderr line to its own log, prefixed with `[bridge]`).
- **stdin EOF is the shutdown signal**: close the IEC 104 client cleanly
  (`Close()`) and exit with code 0. The backend sends `SIGTERM` after 3 s and
  `SIGKILL` after 5 s if the process is still running.
- If the process exits, the backend restarts it with exponential backoff
  (1 s, 2 s, 4 s ... up to 30 s). The IEC 104 TCP session therefore restarts
  whenever the backend restarts.
- The binary must be built for the Pi: `GOOS=linux GOARCH=arm64`.

## 2. Wire protocol (version 1)

UTF-8, one JSON object per line, terminated by `\n`. A message must never
contain a raw newline (`encoding/json` guarantees this). Lines are unbounded
in length, so a "Lesen" batch with many points is legal.

### Go -> backend (stdout)

First line after startup, once stdin/stdout handling and the IEC 104 client
are initialised (the IEC 104 TCP connection itself does **not** have to be up):

```json
{"op":"ready","version":1}
```

The backend kills a bridge that sends no `ready` within 10 s or announces a
version other than `1`.

Reply to every `dispatch`, echoing its `reqId`:

```json
{"op":"ack","reqId":"7","accepted":true,"message":""}
{"op":"ack","reqId":"8","accepted":false,"message":"unknown asdu_typ: X_FOO_1 (point 3)"}
```

The backend waits at most 5 s for the ack. Reply when the batch has been
**accepted/queued**, not when the outstation has answered (same semantics as
the old `DispatchAck`). Collect per-point failures into one `accepted:false`
ack, as today.

Asynchronous frames (replies, spontaneous data, command confirmations):

```json
{"op":"event","point":{"id":"…","type":"OUT","asduTyp":"C_SC_NA_1","asduHigh":null,"asduLow":1,"ioaHigh":null,"ioaMiddle":null,"ioaLow":5,"payload":{"value":true,"cot":"COT<ActivationCon>"}}}
```

### Backend -> Go (stdin)

```json
{"op":"dispatch","reqId":"7","type":"OUT","points":[ <point>, … ]}
```

- `reqId`: opaque string, unique per in-flight request.
- `type`: `"IN"` or `"OUT"`.
- `points`: the full list of rows of that direction.

### The `point` object

Same field names and `null` semantics as the backend's `Iec104Dto`
(`packages/iec104-contracts/src/iec104.ts`):

| Field | JSON type | Notes |
|---|---|---|
| `id` | string | backend row id; **echo it back on events** |
| `type` | `"IN"` \| `"OUT"` | required on events |
| `designation`, `channelId`, `channelName` | string | optional, echo back |
| `asduTyp` | string | mnemonic such as `C_SC_NA_1`; required on events |
| `asduHigh`, `asduLow` | int or `null` | common address, big-endian split (as before) |
| `ioaHigh`, `ioaMiddle`, `ioaLow` | int or `null` | information object address, big-endian split |
| `typ` | `"Wert"` \| `"Prozent"` | |
| `active` | bool | |
| `payload` | **object** or `null` | command value in dispatch (`{"value":…}`), decoded value out (`{"value":…,"cot":…}`). A real JSON object now, **not** a string |
| `createdAt`, `updatedAt` | number or ISO string | backend metadata; treat as opaque, ignore or echo |
| `intervall`, `transmissionThreshold` | string / int or `null` | 104-IN only |
| `asduRetTyp`, `ioaRetHigh`, `ioaRetMiddle`, `ioaRetLow` | string / int or `null` | 104-OUT only |

Unknown fields must be ignored in both directions. Missing/`null` numbers mean
"not set": unlike proto3, `0` is now a legitimate value and is not confused
with "unset". The backend only accepts integers or `null` for the numeric
fields of an event; anything else is turned into `null`.

## 3. Required changes in the Go repository

1. **Remove gRPC entirely**: `google.golang.org/grpc`, `proto/iec104.proto`,
   `pb/*.go`, the `protoc` regeneration instructions, `startGRPCServer`,
   `-grpc-socket` and the stale-socket handling (and `go mod tidy`).
2. **Replace `*pb.Iec104Point`** with a hand-written struct with JSON tags
   matching the table above (use `*int` / `*string` for nullable fields and
   `map[string]any` or `json.RawMessage` for `payload`). `registry`
   (`pointKey{ca, ioa}`), `typemap.go` and `dispatchPoint` stay as they are,
   apart from reading the command value from `payload["value"]` instead of
   parsing `payload_json`.
3. **stdin reader**: loop with `bufio.Reader.ReadBytes('\n')` (do **not** use
   `bufio.Scanner`; its 64 KB default line limit would truncate large
   batches). For each `dispatch` line call the existing per-point logic and
   write exactly one `ack` with the same `reqId`. On `io.EOF`, shut down
   cleanly and exit 0. Ignore lines that are not valid JSON or have an
   unknown `op` (log them to stderr).
4. **stdout writer**: a **single** goroutine owns `os.Stdout`; everything
   (`ready`, `ack`, `event`) is sent to it through one buffered channel and
   written as one `json.Marshal` result plus `\n` per line, so lines never
   interleave.
5. **Keep `publishEvent` non-blocking** (`handlerLoop` must never stall): the
   channel send is a non-blocking `select`; when the buffer is full, drop the
   frame and log it to stderr, as `eventHub` does today. `eventHub` collapses
   to this one channel because there is exactly one consumer. Acks may block
   briefly (they are produced on the reader goroutine, not on `handlerLoop`).
6. **stdout is protocol only.** Move every `fmt.Print*`/`fmt.Fprint(os.Stdout…)`
   (the `masterCall` observation printing, the `-demo` output, banners) to
   stderr, and make sure the standard `log` package and the `clog` library
   logger write to stderr. A single stray line on stdout is logged as
   "malformed bridge line" and ignored by the backend, but never rely on that.
7. **Startup**: initialise the IEC 104 client and handlers, then emit `ready`.
   If initialisation fails, print the error to stderr and exit non-zero (the
   backend will restart with backoff).
8. **Drop the standalone-demo coupling** if it conflicts with stdio: `-demo`
   may stay, but its output must go to stderr.
9. **Tests**: a pipe-based round trip (`io.Pipe` as stdin/stdout) covering
   `ready`, `dispatch` to `ack`, an `event` after a simulated callback, EOF to
   clean exit, and a check that nothing but valid JSON lines reaches stdout.
10. **Docs**: update `architecture.md` section 5 and section 7 of
    `iec104_client_architecture.md` (the `Iec104Bridge` gRPC bridge) to
    describe this protocol; delete the "canonical proto lives in
    `grpc_client_nestjs`" remark.

## 4. Backend configuration

| Variable | Default | Meaning |
|---|---|---|
| `IEC104_BRIDGE_BIN` | unset (bridge disabled, "Lesen" only logs an error) | absolute path of the Go binary |
| `IEC104_BRIDGE_ARGS` | empty | JSON string array or whitespace-separated flags, e.g. outstation host/port/common address |
| `IEC104_BRIDGE_RESTART_DELAY_MS` | `1000` | base of the restart backoff (max 30 s) |
| `IEC104_BRIDGE_ACK_TIMEOUT_MS` | `5000` | how long a `dispatch` waits for its `ack` |

The old `IEC104_GRPC_SOCKET_PATH` and `IEC104_GRPC_RECONNECT_DELAY_MS` are no
longer used.

## 5. Rollout

Backend and Go binary must be deployed together: an old gRPC-only binary never
prints `ready`, so the backend kills and restarts it every 10 s and logs
`no "ready" from bridge`. Where the binary lives (inside the backend release,
or at a fixed path outside it) is a deployment decision, not part of the
protocol.
