# `iec104_client` — Architecture & Step-by-Step Workflow

Client-side (master / controlling station) documentation for the IEC 60870-5-104
test system in this repository.

- **Scope:** this document describes **only `iec104_client/`**. The outstation
  (`iec104_server/`) is mentioned exclusively where a wire exchange is needed to
  explain a client-side step, and is always labelled *peer side (context only)*.
- **Related documents:** root [`README.md`](../README.md),
  [`architecture.md`](architecture.md) (whole system), and
  [`iec_60870_5_104_bug_report.md`](../iec_60870_5_104_bug_report.md)
  (upstream library defects that shape this code).
- **Library stack:** [`wendy512/iec104`](https://github.com/wendy512/iec104)
  v1.0.4 (`client` package: IEC 104 transport wrapper),
  [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5) v1.2.5
  (`cs104` transport state machine + `asdu` codec). The MSL5 backend bridge (§7)
  uses only the Go standard library (`encoding/json`, `bufio`). Go 1.27.1.
- **Module:** `github.com/alfas/iec104-client` → binary `iec104-client.exe`.
- **Defaults:** host `127.0.0.1`, port **2404**, common address (CA) **1**,
  run time indefinite (**`-duration 0`**, the default — pass e.g. `30s` for a
  fixed run), ASDU params `asdu.ParamsWide` (COT 2 B, CA 2 B,
  **IOA 3 B**, UTC), stdio bridge on stdin/stdout (NDJSON).

---

## 1. What the client is

The client is the IEC 104 **master**: it opens the TCP connection, activates
data transfer with `STARTDT`, issues control-direction commands (`C_*` ASDUs)
and receives monitoring data plus command confirmations (`M_*` / `C_*`). It
also runs as a **stdio bridge** (§7): the MSL5 backend spawns it as a child
process and, over stdin/stdout using newline-delimited JSON (NDJSON), issues
those same commands and receives the same inbound events instead of using the
demo-only in-process path.

It writes nothing to disk — every observation is logged to **stderr** (stdout
is reserved for protocol frames), in addition to (optionally) being forwarded to
the backend as `event` frames:

| File | Role |
| :--- | :--- |
| `iec104_client/main.go` | Flags, `client.Settings` construction, connect/shutdown lifecycle (stdin EOF / SIGTERM / `-duration`), redirects `os.Stdout` to stderr and hands the real stdout to the bridge, starts the stdio bridge, optional demo command sequence (`sendDemoCommands`, only with `-demo`) |
| `iec104_client/call.go` | `masterCall` — the `client.ASDUCall` implementation: every inbound callback, plus `onCommandReply` for command confirmations; forwards every event to `bridge.publishEvent` |
| `iec104_client/bridge.go` | `bridge` — the NDJSON stdio protocol: `serve` (stdin reader), `dispatch`/`dispatchPoint`, `publishEvent`, the single stdout writer goroutine, the `point` JSON type and the `registry` (§7) |
| `iec104_client/bridge_test.go` | Pipe-based round-trip tests of the bridge protocol |
| `iec104_client/typemap.go` | `asduTyp` string ↔ `asdu.TypeID` conversion tables and `commandValueFromPayload` (§7) |

Everything else (TCP, APDU framing, I-/S-/U-frames, sequence numbers, timers,
ASDU encode/decode) lives in the two dependencies.

---

## 2. Architecture

### 2.1 Layer map

```text
┌───────────────────────────────────────────────────────────────────────┐
│ APPLICATION  (this repository)          package main                  │
│                                                                       │
│   main.go                                                             │
│     flag parsing · client.NewSettings · client.New · Set*Handler      │
│     Connect / Close · starts stdio bridge · sendDemoCommands (-demo)   │
│                                                                       │
│   call.go                                                             │
│     masterCall  ── implements client.ASDUCall (8 inbound callbacks)   │
│       OnInterrogation · OnCounterInterrogation · OnRead               │
│       OnTestCommand · OnClockSync · OnResetProcess                    │
│       OnDelayAcquisition · OnASDU (+ onCommandReply)                  │
│       every callback also calls bridge.publishEvent(...)              │
│                                                                       │
│   bridge.go / typemap.go                NDJSON stdio bridge           │
│     bridge.serve(stdin): dispatch → client.Send*Cmd → ack   (§7)      │
│     publishEvent → out channel → writer goroutine → stdout            │
└───────────────────────────────┬───────────────────────────────────────┘
                                │  application never touches frames
┌───────────────────────────────▼───────────────────────────────────────┐
│ LIBRARY WRAPPER   github.com/wendy512/iec104/client                   │
│                                                                       │
│   core.go     Client{client104, settings, onConnectHandler, ...}      │
│               Settings{Host, Port, AutoConnect, Cfg104, Params, ...}  │
│               Connect() · Close() · Set*Handler()                     │
│               Send*Cmd() ─► command{} ─► doSend() ─► asdu.*Cmd()      │
│               testConnect()   (probe dial, closed immediately)        │
│   handle.go   clientHandler{call ASDUCall}  →  forwards to masterCall │
│               GetDataType(TypeID) DataType  (type-id → category)      │
│   interface.go  type ASDUCall interface { On…(*asdu.ASDU) error }     │
│   errors.go     NotConnected                                          │
└───────────────────────────────┬───────────────────────────────────────┘
                                │  ASDUs only, no APCI
┌───────────────────────────────▼───────────────────────────────────────┐
│ TRANSPORT STATE MACHINE   github.com/wendy512/go-iecp5/cs104          │
│                                                                       │
│   client.go   running()  ─ dial + auto-reconnect loop                 │
│               run()      ─ per-connection state machine               │
│                            goroutines: recvLoop / sendLoop /          │
│                                        handlerLoop                    │
│                            channels:   sendASDU sendRaw rcvRaw rcvASDU│
│                            seq:        seqNoSend ackNoSend            │
│                                        seqNoRcv  ackNoRcv  pending[]  │
│                            frames:     I / S / U (STARTDT, STOPDT,    │
│                                        TESTFR)                         │
│   config.go   Config{ConnectTimeout0(t₀)=30s, SendUnAckLimitK(k)=12,  │
│                      SendUnAckTimeout1(t₁)=15s,                       │
│                      RecvUnAckLimitW(w)=8,              t₂=10s, t₃=20s}│
│   apci.go     APDU/APCI encode, parse, newIFrame/newSFrame/newUFrame  │
│   clientOption.go  autoReconnect, reconnectInterval, remote server URL│
└───────────────────────────────┬───────────────────────────────────────┘
                                │  ASDUs
┌───────────────────────────────▼───────────────────────────────────────┐
│ ASDU CODEC   github.com/wendy512/go-iecp5/asdu                        │
│   asdu.go · identifier.go · information.go · mproc.go · cproc.go      │
│   params: ParamsWide {COT 2 B, CA 2 B, IOA 3 B, UTC}                  │
│   encode: AppendNormalize/AppendScaled/AppendFloat32/AppendCP56Time2a │
│   decode: Get*() accessors — NOTE: these CONSUME the ASDU (Bug 1)     │
└───────────────────────────────┬───────────────────────────────────────┘
                                │  bytes
                        net.Conn (TCP, default port 2404)
```

### 2.2 Responsibility split

| Concern | Owner |
| :--- | :--- |
| Flags, common address, run duration, shutdown triggers | `iec104_client/main.go` |
| Which commands to send and in what order | `sendDemoCommands` (`main.go`) |
| Turning an ASDU type into human-readable output | `masterCall` (`call.go`) |
| `ASDUCall` → concrete handler bridging | `client/clientHandler` (`client/handle.go`) |
| Command construction from `(ca, typeId, ioa, value)` | `doSend` + `asdu.*Cmd` (`client/core.go`) |
| Probe dial, connect-timeout gate, STARTDT/STOPDT triggers | `Client.Connect` / `Client.Close` (`client/core.go`) |
| TCP, APDU framing, I/S/U frames, k/w/t₀–t₃, keepalive, reconnect | `cs104.Client` |
| ASDU marshal/unmarshal | `asdu` package |

### 2.3 Concurrency model

`main` blocks on a context, while the library runs its own goroutines:

```text
main goroutine
  flag.Parse → settings → client.New → bridge.start (emits "ready")
  → go: wait for outstation → Connect()                      (blocks ≤ t₀)
  → go: bridge.serve(stdin)                                  (NDJSON reader)
  → select { ctx.Done (‑duration, Ctrl+C / SIGTERM), stdin EOF, connect error }
  → c.Close()  →  SendStopDt()  →  client104.Close()  →  bridge.stop()  →  exit

cs104: running()            dial + reconnect loop   (one goroutine,
                            started by Start())
       └─ run()             per-connection state machine, called by running()
                            for each connection; owns the send/ack logic
            ├─ recvLoop()   TCP → 2-byte APDU framing → rcvRaw
            ├─ sendLoop()   sendRaw → TCP write
            └─ handlerLoop() rcvASDU → UnmarshalBinary → clientHandler
                             → masterCall callbacks   ← ALL inbound app code runs here

onServerActive spawn        go sf.onServerActive(sf)
                            → sendDemoCommands runs on its own goroutine
```

Consequences for application code:

- **Inbound callbacks are serialised** on the single `handlerLoop` goroutine, so
  `masterCall` needs no mutex — each callback runs to completion before the next
  ASDU is dispatched.
- **Outbound sends are safe from any goroutine**: `Send*Cmd` only marshals and
  pushes onto the buffered `sendASDU` channel that `run()` drains.
- Channel buffers: `sendASDU` = `k<<4` (192), `sendRaw` = `k<<5` (384),
  `rcvASDU` = `w<<4` (128), `rcvRaw` = `w<<5` (256) with the defaults
  `k = 12`, `w = 8`. A full `sendASDU` yields `ErrBufferFulled`.
- Safer still: `clientHandler` wraps every dispatch in `recover()`, so a panic
  inside `masterCall` is logged as `cs104 client => [C] client handler …`
  rather than killing the process.

### 2.4 Client-side data structures

| Structure | Defined in | Fields that matter here |
| :--- | :--- | :--- |
| `client.Settings` | `client/core.go` | `Host`, `Port`, `AutoConnect`, `ReconnectInterval` (default 1 min), `Cfg104 *cs104.Config`, `Params *asdu.Params`, `TLS`, `LogCfg` |
| `client.LogCfg` | `client/core.go` | `Enable bool`, `LogProvider` |
| `client.Client` | `client/core.go` | `client104 *cs104.Client` (transport), `settings`, `onConnectHandler`, `connectionLostHandler` |
| `client.command` | `client/core.go` | `typeId`, `ca`, `ioa`, `t`, `qoc`, `qos`, `value` — the request built by `SendCmd`/`Send*Cmd` |
| `masterCall` | `call.go` | `bridge *bridge` — nil-safe; forwards every callback to `bridge.publishEvent` (§7) |
| `bridge` | `bridge.go` | `client commander` (the `*client.Client`), `reg *registry`, `out chan any` (outgoing frames, drained by one writer goroutine) — the stdio protocol (§7) |
| `cs104.Client` | `cs104/client.go` | `conn`, `rcvASDU`/`sendASDU`/`rcvRaw`/`sendRaw`, `seqNoSend`/`ackNoSend`/`seqNoRcv`/`ackNoRcv`, `pending []seqPending`, `isActive` |

---

## 3. How `iec104_client` works — step by step

### Step 1 — Parse the flags

```go
host     := flag.String("host", "127.0.0.1", "server host")
port     := flag.Int("port", 2404, "server port")
ca       := flag.Uint("ca", 1, "ASDU common address")
duration := flag.Duration("duration", 0, "how long to run (0 = until stdin closes or Ctrl+C/SIGTERM)")
verbose  := flag.Bool("verbose", false, "enable wendy512/go-iecp5 library logging…, written to stderr")
demo     := flag.Bool("demo", false, "send the built-in demo command sequence once the outstation is active and skip the stdio bridge entirely (no stdin/stdout protocol)")
```

`ca` is converted to `uint16` only at the call site of `sendDemoCommands`; the
rest of the file uses the pointer values directly. `-demo` defaults to `false`:
without it, the client sends nothing on its own and simply waits for `dispatch`
requests on stdin (§7) once the outstation is active.

### Step 2 — Build `client.Settings`

```go
settings := client.NewSettings()   // Host=localhost, Port=2404, AutoConnect=true,
                                   // ReconnectInterval=1min, Cfg104=cs104.DefaultConfig(),
                                   // Params=asdu.ParamsWide
settings.Host = *host
settings.Port = *port
settings.AutoConnect = true
settings.LogCfg = &client.LogCfg{
    Enable:      *verbose,
    LogProvider: stderrLogger{…},     // the library default writes to os.Stdout
}
```

- `Cfg104` stays at `cs104.DefaultConfig()`: t₀ 30 s, k 12, t₁ 15 s, w 8,
  t₂ 10 s, t₃ 20 s. No flag overrides it.
- `Params` stays `asdu.ParamsWide` → **3-octet IOA**, 2-octet COT/CA, UTC.
- `verbose` is the only way to see library diagnostics: the underlying `clog`
  starts with `has = 0` (output disabled), and `client.LogCfg.Enable` is what
  calls `LogMode(true)`. The `LogProvider` is always a stderr logger, because
  the library's built-in logger writes to `os.Stdout`, which carries the
  protocol. This matters because inbound ASDU **decode failures are
  swallowed with `Warn`** (see §5).

### Step 3 — Create the client and bind the callbacks

```go
b := newBridge(nil)               // nil with -demo; client wired in below, once c exists
c := client.New(settings, &masterCall{bridge: b})
b.setClient(c)
c.SetOnConnectHandler(func(_ *client.Client) { … "connected to outstation" … })
c.SetServerActiveHandler(func(c *client.Client) {
    … "outstation active" …
    if demo { sendDemoCommands(c, ca) }    // otherwise: wait for dispatch on stdin
})

b.start(protoOut)    // writer goroutine + {"op":"ready","version":1}   (§7)
go func() { waitForOutstation(); c.Connect() }()
go func() { serveErr <- b.serve(in) }()   // NDJSON reader on stdin
```

`b` is created before `c` because `masterCall` needs a `*bridge` to forward
events to, but `bridge.client` needs the constructed `*client.Client` to
dispatch commands on — the two-step assignment breaks that circular
dependency. `ready` is sent as soon as the client object exists; the IEC 104
TCP session does not have to be up yet (commands dispatched before it is are
rejected in their `ack`, and `waitForOutstation` keeps probing the outstation
before the first `Connect()`).

`client.New` performs three chained constructions:

```text
client.New(settings, call)
  ├─ newClientOption(settings)
  │    cs104.NewOption() → SetConfig(Cfg104) → SetParams(Params)
  │                      → SetAutoReconnect(true) → SetReconnectInterval(1m)
  │                      → SetTLSConfig(nil) → AddRemoteServer("tcp://host:port")
  ├─ clientHandler{call: masterCall}     ← the ASDUCall bridge
  └─ cs104.NewClient(handler, option)    ← transport, own logger prefix
                                            "cs104 client => "
     (LogMode(Enable) + SetLogProvider applied when settings.LogCfg != nil)
```

Note the callback indirection: the transport knows only
`cs104.ClientHandlerInterface`; `client.clientHandler` (in `handle.go`) forwards
each method to the matching `ASDUCall` method on `masterCall`.

### Step 4 — `Connect()`

`Connect()` is two operations and one gate:

```text
func (c *Client) Connect() error {
    testConnect()          // ① probe dial
    client104.Start()      // ② spawn running() — returns immediately
    wg.WaitTimeout(t₀)     // ③ block until the first real connection is up
}
```

1. **Probe dial.** `testConnect` parses `tcp://host:port` and does a
   `net.DialTimeout(…, ConnectTimeout0)` followed immediately by `conn.Close()`.
   It only answers "is something listening?" and never exchanges a byte.
   *This is the second TCP connection visible per session* — the root
   [`README.md`](../README.md) records 2 connections per run for this reason. On
   failure `Connect` returns that dial error and `log.Fatalf("connect failed: %v")`
   terminates the program.
2. **Start.** `client104.Start()` launches `running()`; the dial itself happens
   asynchronously there, so `Start()` never blocks.
3. **Connect-timeout gate.** `wg.WaitTimeout(settings.Cfg104.ConnectTimeout0)`
   waits on a `waitgroup.WaitGroup` that the library's own connect handler
   releases exactly once (`firstConnect.CompareAndSwap`) — the counter is marked
   "first connect only", so a reconnect does **not** re-release it. On expiry:

   ```text
   connection timeout of 30.000000 seconds
   ```

   Note this is the *library* timeout gate, distinct from the probe dial.

### Step 5 — TCP up, `STARTDT` sent, connect callback fires

Inside `running()` → `openConnection()` → `run(ctx)`:

```text
run(ctx):
  cleanUp()                    // seq numbers → 0, pending → nil, drain channels
  setConnectStatus(connected)
  go recvLoop(); go sendLoop(); go handlerLoop()
  onConnect(sf)                // ← the library's own handler:
                               //      SendStartDt()  (U-frame STARTDT, act)
                               //      if c.onConnectHandler != nil { c.onConnectHandler(c) }
```

So: as soon as the TCP connection is established, the client sends
**`STARTDT` (activate)** and the application prints

```text
[client] connected to outstation
```

The `waitgroup` from Step 4 is released here, which is what unblocks `Connect`.

### Step 6 — `STARTDT_con` → server active → demo commands (or bridge idle)

`isActive` is `inactive` until the U-frame **`STARTDT_con`** arrives. Only then
may I-frames flow (`cs104.Send` otherwise returns `ErrNotActive`). On that frame:

```text
case uStartDtConfirm:
    isActive = active
    go sf.onServerActive(sf)   // ← separate goroutine
```

which reaches, in the application,

```go
c.SetServerActiveHandler(func(c *client.Client) {
    logf("[client] outstation active (STARTDT confirmed)\n")
    if demo {
        logf("[client] -demo set - sending demo commands\n")
        sendDemoCommands(c, ca)
    }
})
```

```text
[client] outstation active (STARTDT confirmed)
[client] -demo set - sending demo commands          ← only printed with -demo
```

**This is the trigger point of all master traffic sent by this program itself**
— nothing is sent between `Connect()` and the confirmation. Without `-demo`
(the default) nothing is sent at all until the backend writes a `dispatch`
request on stdin (§7); the client is otherwise fully passive but keeps
receiving and forwarding whatever the outstation reports.

### Step 7 — The demo command sequence (`-demo` only)

`sendDemoCommands` walks a slice of `{name, fn}` pairs and calls them in order,
printing `[client] <name> sent` or `[client] ERROR sending <name>: <err>`:

| # | Name | Client call | ASDU | IOA | Value / qualifier |
| :-- | :-- | :-- | :-- | :-- | :-- |
| 1 | general interrogation | `SendInterrogationCmd(ca)` | `C_IC_NA_1` | 0 | `QOIStation` (20) |
| 2 | counter interrogation | `SendCounterInterrogationCmd(ca)` | `C_CI_NA_1` | 0 | `QCCTotal` + `QCCFrzRead` (0x05) |
| 3 | read command | `SendReadCmd(ca, 100)` | `C_RD_NA_1` | 100 | — |
| 4 | clock synchronization | `SendClockSynchronizationCmd(ca, time.Now())` | `C_CS_NA_1` | 0 | CP56Time2a = now |
| 5 | test command | `SendTestCmd(ca)` | `C_TS_TA_1` | 0 | `t = time.Now()` |
| 6 | reset process | `SendResetProcessCmd(ca)` | `C_RP_NA_1` | 0 | `QPRGeneralRest` (0x01) |
| 7 | single command | `SendCmd(ca, C_SC_NA_1, InfoObjAddr(1000), true)` | `C_SC_NA_1` | 1000 | `true` |
| 8 | double command | `SendCmd(ca, C_DC_NA_1, InfoObjAddr(2000), uint8(DPIDeterminedOn))` | `C_DC_NA_1` | 2000 | `DPIDeterminedOn` (2) |
| 9 | set-point float | `SendCmd(ca, C_SE_NC_1, InfoObjAddr(3000), float32(125.5))` | `C_SE_NC_1` | 3000 | 125.5 |
| 10 | bitstring command | `SendCmd(ca, C_BO_TA_1, InfoObjAddr(4000), uint32(0x00FF00FF))` | `C_BO_TA_1` | 4000 | 0x00FF00FF + CP56Time2a |
| 11 | bitstring (untagged twin) | `SendCmd(ca, C_BO_NA_1, InfoObjAddr(4001), uint32(0x0F0F0F0F))` | `C_BO_NA_1` | 4001 | 0x0F0F0F0F |

All 11 are sent back-to-back with no pacing; the 12-slot k-window is far from
being exhausted. Commands 10/11 are an intentional A/B pair — see §4.

### Step 8 — Outbound path (one command, end to end)

```text
sendDemoCommands                       main.go
  │  c.SendInterrogationCmd(ca)         (or SendCmd / SendTestCmd / …)
  ▼
client.Client.SendInterrogationCmd     client/core.go
  │  builds command{typeId: C_IC_NA_1, ca: ca}
  ▼
client.Client.doSend(cmd)              client/core.go
  │  ① if !IsConnected() → return NotConnected          ("not yet connected")
  │  ② coa := activationCoa()   → Cause = Activation (6), IsTest/IsNegative false
  │  ③ switch cmd.typeId → the matching asdu constructor:
  │       C_IC_NA_1 → client104.InterrogationCmd(coa, ca, QOIStation)
  │       C_CI_NA_1 → client104.CounterInterrogationCmd(coa, ca, qcc)
  │       C_CS_NA_1 → client104.ClockSynchronizationCmd(coa, ca, cmd.t)
  │       C_RD_NA_1 → client104.ReadCmd(coa, ca, cmd.ioa)
  │       C_RP_NA_1 → client104.ResetProcessCmd(coa, ca, QPRGeneralRest)
  │       C_TS_TA_1 → asdu.TestCommandCP56Time2a(client104, coa, ca, cmd.t)
  │       C_SC_*    → asdu.SingleCmd(…SingleCommandInfo{Value bool, Qoc})     (cast.ToBoolE)
  │       C_DC_*    → asdu.DoubleCmd(…DoubleCommandInfo{Value uint8, Qoc})    (cast.ToUint8E)
  │       C_SE_NA_1/TA_1 → asdu.SetpointCmdNormal  (int16 → asdu.Normalize)
  │       C_SE_NB_1/TB_1 → asdu.SetpointCmdScaled  (int16)
  │       C_SE_NC_1/TC_1 → asdu.SetpointCmdFloat   (float32)
  │       C_BO_NA_1/TA_1 → asdu.BitsString32Cmd    (uint32)
  │       (TA variants also carry Time = cmd.t)
  │     QOC: Qual=QOCNoAdditionalDefinition, InSelect=false; QOS: Qual=0
  ▼
asdu constructor → cs104.Client.Send(asdu)      cs104/client.go
  │  ① !IsConnected()          → ErrUseClosedConnection
  │  ② isActive == inactive    → ErrNotActive       (i.e. before STARTDT_conf)
  │  ③ a.MarshalBinary()       → []byte
  │  ④ non-blocking push to sendASDU (buffer 192); full → ErrBufferFulled
  ▼
cs104.Client.run()  ← the state machine loop
  │  ⑤ only drains sendASDU while active AND outstanding ≤ k
  ▼
sendIFrame(asduBytes)                           cs104/client.go
  │  ⑥ I-frame = sendSN (seqNoSend), rcvSN (seqNoRcv)
  │  ⑦ ackNoRcv = seqNoRcv; seqNoSend = (seqNoSend + 1) & 32767
  │  ⑧ pending = append(pending, seqPending{seq, time.Now()})   ← t₁ tracking
  │  ⑨ push to sendRaw
  ▼
cs104.Client.sendLoop()   →  conn.Write(apdu)  →  TCP 2404
```

`MarshalBinary` is where a wrong `value` type surfaces (e.g. `cast.ToBoolE`
failing for a `C_SC_NA_1`), and it is returned straight to `sendDemoCommands`,
which prints it and continues with the next command.

### Step 9 — Inbound path (one ASDU, end to end)

```text
TCP 2404
  │
  ▼
cs104.Client.recvLoop()                                     cs104/client.go
  │  ① read 2-byte APCI start/length header, validate startFrame (0x68),
  │     length = rawData[1] + 2, bounds-check APCICtlFiledSize+2 … APDUSizeMax
  │     (bad header → resynchronise: rdCnt, length = 0, 2)
  │  ② push full APDU to rcvRaw
  ▼
cs104.Client.run()  ── APCI switch
  │  ③ sAPCI : updateAckNoOut(rcvSN)         → validate + drop confirmed pending
  │  ④ iAPCI : discard if isActive == inactive ("station not active")
  │             updateAckNoOut(head.rcvSN) && head.sendSN == seqNoRcv
  │             push payload to rcvASDU; seqNoRcv++ (& 32767)
  │             if unacked ≥ w → sendSFrame(seqNoRcv)
  │  ⑤ uAPCI : STARTDT_con → active + go onServerActive
  │             STOPDT_con → inactive · TESTFR_act → TESTFR_con · TESTFR_con → clear t₃
  ▼
cs104.Client.handlerLoop()                                 cs104/client.go
  │  ⑥ asdu.NewEmptyASDU(&params) → UnmarshalBinary(rawAsdu)
  │       ⚠ on error: sf.Warn("asdu UnmarshalBinary failed,…") and CONTINUE
  │         (silent unless -verbose — see §5 and Bug 3)
  │  ⑦ sf.clientHandler(asduPack)      ← recover() wraps this call
  ▼
client.clientHandler()                                     client/handle.go
  │  ⑧ switch asduPack.Identifier.Type:
  │       C_IC_NA_1 → h.call.OnInterrogation(rxAsdu)
  │       C_CI_NA_1 → h.call.OnCounterInterrogation(rxAsdu)
  │       C_RD_NA_1 → h.call.OnRead(rxAsdu)
  │       C_CS_NA_1 → h.call.OnClockSync(rxAsdu)
  │       C_TS_NA_1 → h.call.OnTestCommand(rxAsdu)
  │       C_RP_NA_1 → h.call.OnResetProcess(rxAsdu)
  │       C_CD_NA_1 → h.call.OnDelayAcquisition(rxAsdu)
  │       default    → h.call.OnASDU(rxAsdu)     ← M_* data AND C_TS_TA_1 /
  │                                               C_SC_* / C_DC_* / C_SE_* /
  │                                               C_BO_* confirmations
  ▼
masterCall  (call.go)  →  logf("[client] …")  (stderr)
```

### Step 10 — Callback reference (`masterCall`)

Two levels of dispatch land in `call.go`: dedicated type-id branches (upstream
`clientHandler`) and everything else through `OnASDU`, which fans out again.

| Inbound ASDU | Reaches `masterCall` via | `masterCall` method | Printed fields |
| :--- | :--- | :--- | :--- |
| `C_IC_NA_1` | `InterrogationHandler` | `OnInterrogation` | `GetInterrogationCmd()` → IOA, QOI, `Coa` |
| `C_CI_NA_1` | `CounterInterrogationHandler` | `OnCounterInterrogation` | `GetCounterInterrogationCmd()` → IOA, `qcc.Value()`, `Coa` |
| `C_RD_NA_1` | `ReadHandler` | `OnRead` | `GetReadCmd()`, `Coa` |
| `C_TS_NA_1` | `TestCommandHandler` | `OnTestCommand` | `GetTestCommand()` → IOA, FBP, `Coa` |
| `C_CS_NA_1` | `ClockSyncHandler` | `OnClockSync` | `GetClockSynchronizationCmd()` → IOA, time (UTC/RFC3339) |
| `C_RP_NA_1` | `ResetProcessHandler` | `OnResetProcess` | `GetResetProcessCmd()` → IOA, QRP |
| `C_CD_NA_1` | `DelayAcquisitionHandler` | `OnDelayAcquisition` | `GetDelayAcquireCommand()` → IOA, msec *(never fires — §4)* |
| `C_TS_TA_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetTestCommandCP56Time2a()` → IOA, FBP, time |
| `C_SC_NA_1` / `C_SC_TA_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetSingleCmd()` → IOA, value, QOC |
| `C_DC_NA_1` / `C_DC_TA_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetDoubleCmd()` → IOA, value, QOC |
| `C_SE_NA_1` / `C_SE_TA_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetSetpointNormalCmd()` → IOA, value |
| `C_SE_NB_1` / `C_SE_TB_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetSetpointCmdScaled()` → IOA, value |
| `C_SE_NC_1` / `C_SE_TC_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetSetpointFloatCmd()` → IOA, value |
| `C_BO_NA_1` / `C_BO_TA_1` | `ASDUHandler` → `OnASDU` | `onCommandReply` | `GetBitsString32Cmd()` → IOA, `0x%08X`, time |
| `M_SP_*` (single point) | `ASDUHandler` → `OnASDU` | `GetDataType() == SinglePoint` | IOA, value, QDS, COT, time |
| `M_DP_*` (double point) | `ASDUHandler` → `OnASDU` | `DoublePoint` | IOA, value, QDS, COT, time |
| `M_ME_NA_1`/`_ND_1`/`_TA_1`/`_TD_1` (normalized) | `ASDUHandler` → `OnASDU` | `MeasuredValueNormal` | IOA, `value.Float64()`, QDS, COT, time |
| `M_ME_NC_1`/`_TC_1`/`_TF_1` (short float) | `ASDUHandler` → `OnASDU` | `MeasuredValueFloat` | IOA, value, QDS, COT, `packet.Type` (`TID<M_ME_TF_1>` …), time |
| `M_IT_*` (integrated totals, `M_IT_NA_1`) | `ASDUHandler` → `OnASDU` | `IntegratedTotals` | IOA, `CounterReading`, `SeqNumber`, `HasCarry`, `IsAdjusted`, `IsInvalid`, COT, time |
| anything else | `ASDUHandler` → `OnASDU` | `default` branch | `type`, `Coa` only |

`OnASDU` implements the two-way split itself:

```text
OnASDU(packet):
   if onCommandReply(packet) { return }      // C_TS_TA_1 / C_SC_* / C_DC_* /
                                             // C_SE_* / C_BO_* → true
   switch client.GetDataType(packet.Type) {  // monitoring data
       case client.SinglePoint: …
       case client.DoublePoint: …
       case client.MeasuredValueNormal: …
       case client.MeasuredValueFloat: …
       case client.IntegratedTotals: …
       default: generic one-liner
   }
```

`client.GetDataType` is the upstream type-id → category mapping (`handle.go`);
types the client does not interpret (`BitString32`, `StepPosition`, protection
events, the `*CommandInfo` categories) intentionally fall through to the
`default` branch or to `onCommandReply`.

### Step 11 — Sequence numbers & timers on the client

The client is the *sending* side of the demo commands and the *receiving* side of
the data flood, so both halves of the IEC 104 numbering apply:

| Mechanism | Client behaviour |
| :--- | :--- |
| I-frame send SN | `seqNoSend` on every outbound command, `pending[]` records the send time; incremented `& 32767` |
| I-frame receive SN | `seqNoRcv` must equal the peer's `sendSN`, else the state machine aborts with "fatal incoming acknowledge…" |
| Acknowledge (`S`-frame) | sent when `seqNoRcv - ackNoRcv >= w` (8), or as soon as the `timeoutResolution` ticker (`100 ms`) sees an unacked I-frame — in practice the ack goes out ~100–200 ms after reception — or after `t₂` |
| `k` window | after 12 unacked outbound I-frames the client stops draining `sendASDU` until acks arrive |
| `t₀` | TCP connect timeout (also the `Connect()` gate) |
| `t₁` | 15 s without an ack for `pending[0]`, or without `STARTDT_con`/`STOPDT_con`/`TESTFR_con` → the connection is torn down |
| `t₂` | 10 s ack-delay upper bound |
| `t₃` | 20 s idle → the client sends `TESTFR` (act) as keepalive; any received frame resets the idle timer |
| Auto-reconnect | `AutoConnect = true`; on loss `running()` re-dials after ~500–1000 ms (or `ReconnectInterval`, 1 min, if the dial itself keeps failing), then re-sends `STARTDT` and re-fires `onConnect` → `[client] connected to outstation` again |

Because `onConnect` / `onServerActive` fire on **every** (re)connection, the demo
command sequence (when `-demo` is set) is re-sent after a reconnect — that is
the documented library behaviour (the comment on `SetOnConnectHandler` warns
the callback may be called more than once), not a defect in `main.go`. The
stdio bridge is unaffected by IEC 104 reconnects: it keeps reading stdin and
keeps answering `dispatch` requests throughout, queuing them onto whatever
`*client.Client` state currently applies (a request issued while disconnected
gets an `accepted:false` ack).

### Step 12 — Shutdown

```text
main            : waits for the first of: -duration elapsed, SIGINT / SIGTERM,
                  stdin EOF (the backend closed the pipe), connect error
                  c.Close()                             (only if Connect() had succeeded)
client.Close()  : client104.SendStopDt()   ← U-frame STOPDT (act)
                  client104.Close()        ← cancels running()'s ctx (→ run())
cs104 run()     : deferred cleanup:
                    isActive = inactive
                    setConnectStatus(disconnected)
                    checkTicker.Stop()
                    conn.Close()           ← unblocks recvLoop / sendLoop
                    wg.Wait()
                    onConnectionLost(sf)   ← no-op here (no handler registered)
main            : bridge.stop()            ← flushes queued frames, ends the stdout writer
                  log.Println("client stopped")
exit            : code 0 (clean stdin EOF / signal / -duration), 1 on a stdin
                  read error or a failed Connect() — the backend then restarts it
```

`Close()` cancels the context that `running()` stored in `closeCancel`, so the
dial/reconnect loop exits — but only once that context exists: the call is a
silent no-op while `closeCancel` is still `nil` (i.e. before `Start()` /
`running()` has run). In this program `Connect()` always precedes `Close()`, so
shutdown is complete (and `Close()` is skipped if `Connect()` never succeeded);
the process then exits immediately. The backend sends `SIGTERM` after 3 s and
`SIGKILL` after 5 s if the process is still running, so shutdown must stay
quick.

---

## 4. Command coverage on the client side

What the wrapper *can* send (`cli.SendCmd` → `doSend` type switch, or a dedicated
`Send*Cmd`) versus what the demo actually sends:

| ASDU | Send path available | Sent by the demo? |
| :--- | :--- | :--- |
| `C_IC_NA_1` | `SendInterrogationCmd` | ✅ #1 |
| `C_CI_NA_1` | `SendCounterInterrogationCmd` | ✅ #2 |
| `C_RD_NA_1` | `SendReadCmd` | ✅ #3 |
| `C_CS_NA_1` | `SendClockSynchronizationCmd` | ✅ #4 |
| `C_TS_TA_1` | `SendTestCmd` | ✅ #5 |
| `C_RP_NA_1` | `SendResetProcessCmd` | ✅ #6 |
| `C_SC_NA_1` / `C_SC_TA_1` | `SendCmd` | ✅ #7 (NA only) |
| `C_DC_NA_1` / `C_DC_TA_1` | `SendCmd` | ✅ #8 (NA only) |
| `C_SE_NA_1`/`TA_1`, `NB_1`/`TB_1`, `NC_1`/`TC_1` | `SendCmd` | ✅ #9 (`NC_1` only) |
| `C_BO_NA_1` / `C_BO_TA_1` | `SendCmd` | ✅ #10 / #11 |
| `C_RC_NA_1` / `C_RC_TA_1` (step command) | `doSend` has a branch, **no** `Send*Cmd` wrapper | ❌ |
| `C_CD_NA_1` (delay acquisition) | not exposed by the wrapper (only `cs104.Client.DelayAcquireCommand`) | ❌ — so `OnDelayAcquisition` never fires |
| `C_TS_NA_1` (untagged test) | not exposed by the wrapper | ❌ — so `OnTestCommand` never fires |

### The `C_BO_TA_1` / `C_BO_NA_1` A/B pair

Commands #10 and #11 are sent deliberately as a matched pair (IOA 4000 vs. 4001):

- `C_BO_TA_1` (type **64**) leaves the client successfully — `MarshalBinary`
  succeeds and `Send` returns `nil` — but the **receiving** peer drops it inside
  its own decoder, because `asdu.infoObjSize` has no entry for type IDs 58–64.
  The client therefore never receives a confirmation for IOA 4000, and it cannot
  detect the drop.
- `C_BO_NA_1` (type **51**) is registered, decodes normally and completes the
  round-trip, producing
  `[client] bitstring32 command confirmation: IOA=4001 value=0x0F0F0F0F …`.

This is an upstream `go-iecp5` defect ("Bug 3") that hits whichever peer
*receives* such a type; it is **not** a client-side bug. The client keeps the
complete `C_BO_TA_1` code path in place so nothing more than an upstream patch is
needed later. Full analysis: [`iec_60870_5_104_bug_report.md`](../iec_60870_5_104_bug_report.md) §14
and [`architecture.md`](architecture.md) §4.

---

## 5. Client-side error handling & logging

**Return values that the demo can print as
`[client] ERROR sending …`:**

| Error | Raised by | Meaning |
| :--- | :--- | :--- |
| `client.NotConnected` | `doSend` when `!IsConnected()` | `Send*Cmd` called before `Connect()` succeeded |
| `ErrUseClosedConnection` | `cs104.Send` | socket is gone |
| `ErrNotActive` | `cs104.Send` when `isActive == inactive` | command issued before `STARTDT_con` |
| `ErrBufferFulled` | `cs104.Send` (full `sendASDU`) | >192 commands queued at once — never hit by 11 back-to-back commands |
| `cast.To*E` / `MarshalBinary` errors | `doSend` value conversion | wrong Go type for the requested ASDU type |

`sendDemoCommands` logs and **continues** with the next command, so one failure
does not abort the sequence. A failing `Connect()` is fatal (logged to stderr,
exit code 1); in bridge mode the outstation is first probed with a plain TCP
dial (`waitForOutstation`) so a down outstation delays `Connect()` instead of
killing the bridge.

**Everything is logged to stderr.** In bridge mode stdout carries only NDJSON
protocol lines, so `main` points `os.Stdout` and the standard logger at stderr
(`logf` writes there explicitly) and installs a stderr `LogProvider` for the
library.

**Library logging is off by default.** The dependency logger (`clog`) is created
with `has = 0`, i.e. `Debug`/`Warn`/`Error`/`Critical` print nothing. The
`-verbose` flag is the only switch:

```go
settings.LogCfg = &client.LogCfg{Enable: *verbose, LogProvider: stderrLogger{…}}
                                                  // → client104.LogMode(*verbose)
```

With `-verbose` the library prints its own prefixed lines, e.g.

```text
cs104 client => [D] run started!
cs104 client => [D] TX uFrame {…}
cs104 client => [W] asdu UnmarshalBinary failed,asdu: type identification unknown
```

The `[W] … UnmarshalBinary failed` line is the **only** trace of an inbound frame
that the peer dropped or that the client could not decode — the client-side
`handlerLoop` treats it as a warning and keeps looping (`continue`). Running
without `-verbose` makes such frames completely invisible, which is exactly why
the `C_BO_TA_1` defect stayed hidden.

Inbound application callbacks are wrapped in `recover()` by
`cs104.Client.clientHandler`, so a panic in `masterCall` is reported as
`cs104 client => [C] client handler …` and does not stop reception.

---

## 6. Build & run (client only)

```powershell
# build — run the build from inside the module folder
cd iec104_client; go build ./...          # writes .\iec104_client\iec104-client.exe

# run (needs an outstation listening on the target port); runs as the stdio
# bridge (NDJSON on stdin/stdout) unless -demo is set - normally the MSL5
# backend spawns it, stdin EOF shuts it down
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -ca 1 -duration 40s

# reinstate the old always-on demo command sequence
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s -demo

# run forever, stop with Ctrl+C
.\iec104_client\iec104-client.exe -duration 0

# surface the library frame/decode log (needed to see dropped inbound frames)
.\iec104_client\iec104-client.exe -duration 40s -verbose
```

For the Raspberry Pi CM4/CM5 (see the root [`README.md`](../README.md) for the full
`pull -> build -> install` workflow), build and install the static arm64 binaries
from the repo root:

```bash
./pull.sh
./build.sh                 # -> dist/iec104-client (static linux/arm64, CGO off)
sudo ./deploy/install.sh   # -> /opt/iec104 + systemd iec104-server / iec104-client-demo
                           #    (the backend spawns /opt/iec104/iec104-client itself)
```

| Flag | Default | Meaning |
| :--- | :--- | :--- |
| `-host` | `127.0.0.1` | outstation host |
| `-port` | `2404` | outstation port |
| `-ca` | `1` | ASDU common address used for all sent commands |
| `-duration` | `0` | run time; `0` = until Ctrl+C / SIGTERM or, in bridge mode, stdin EOF (indefinite) |
| `-verbose` | `false` | enable the dependency `clog` (frames + decode errors), written to stderr |
| `-demo` | `false` | send the built-in demo command sequence once active and skip the stdio bridge entirely (no stdin/stdout protocol) |

The binary is named after the module path (`github.com/alfas/iec104-client`), not
the folder, hence `iec104-client.exe`.

### Expected output (first seconds, with `-demo`; all on stderr)

```text
[client] connected to outstation
[client] outstation active (STARTDT confirmed)
[client] -demo set - sending demo commands
[client] general interrogation sent
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationCon>
[client] single point:    IOA=100 value=true     QDS=0 COT=COT<InterrogatedByStation>
[client] double point:    IOA=200 value=2        QDS=0 COT=COT<InterrogatedByStation>
[client] measured normal: IOA=300 value=0.305176 QDS=0 COT=COT<InterrogatedByStation>
[client] measured float:  IOA=301 value=100.000  QDS=0 COT=COT<InterrogatedByStation> type=TID<M_ME_NC_1>
[client] measured float:  IOA=302 value=100.000  QDS=0 COT=COT<InterrogatedByStation> type=TID<M_ME_TF_1>
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationTerm>
[client] counter interrogation sent
[client] counter interrogation reply: IOA=0 QCC=0x05 COT=COT<ActivationCon>
[client] integrated totals: IOA=400 count=0 SQ=1 CY=false CA=false IV=false COT=COT<RequestByGeneralCounter>
[client] counter interrogation reply: IOA=0 QCC=0x05 COT=COT<ActivationTerm>
… (read, clock sync, test, reset process, single/double/set-point/bitstring
   confirmations, then periodic + spontaneous data until -duration) …
[client] client stopped
```

*(Values above are from the verified 2026-09-25 run recorded in the root
[`README.md`](../README.md).)*

---

## 7. Stdio bridge (NDJSON)

This section covers `bridge.go` and `typemap.go` in file-by-file detail;
[`architecture.md`](architecture.md) §5 covers the same feature from the
whole-system perspective and [`../IEC104_GO_BRIDGE.md`](../IEC104_GO_BRIDGE.md)
is the authoritative wire-protocol specification.

### 7.1 Why it exists

The demo command sequence (Step 7, only sent with `-demo`) is useful for
smoke-testing, but the real consumer of this client is the MSL5 NestJS
backend, which needs to (a) push its own list of points to read/write and
(b) receive every resulting IEC 104 frame asynchronously. Both processes run on
the same Raspberry Pi, so the backend **spawns this binary as a child process**
(`IEC104_BRIDGE_BIN` / `IEC104_BRIDGE_ARGS`) and exchanges one JSON object per
line over its stdin/stdout — no sockets, no code generation. If the process
exits the backend restarts it with exponential backoff (1 s … 30 s).

### 7.2 The protocol (version 1)

| Channel | Direction | Content |
| :--- | :--- | :--- |
| stdin | backend → Go | `{"op":"dispatch","reqId":"7","type":"OUT","points":[<point>,…]}`; **EOF = clean shutdown** |
| stdout | Go → backend | `{"op":"ready","version":1}` (first line), `{"op":"ack","reqId":"7","accepted":true,"message":""}` per dispatch, `{"op":"event","point":<point>}` asynchronously. **Nothing else** |
| stderr | Go → backend log | every log line (forwarded as `[bridge] …`) |

The `point` object (Go type `point` in `bridge.go`) mirrors the backend's
`Iec104Dto` field for field:

```json
{"id":"…","type":"OUT","designation":"…","channelId":"…","channelName":"…",
 "asduTyp":"C_SC_NA_1",
 "asduHigh":null,"asduLow":1,            // Common Address, split big-endian
 "ioaHigh":null,"ioaMiddle":null,"ioaLow":5,  // Information Object Address, split big-endian
 "typ":"Wert","active":true,
 "payload":{"value":true},               // command value in / {"value":…,"cot":…} out
 "createdAt":…,"updatedAt":…}
```

The address parts are `*int` in Go: `null`/missing means "not set", and `0` is a
legitimate value (unlike proto3). The CA and the 3-octet IOA are split across
several integers because IEC 104's `ParamsWide` IOA has no matching scalar
type; splitting big-endian byte-for-byte keeps the bridge free of any
assumption about field width on either side. Fields the bridge does not
interpret (`createdAt`, `intervall`, `asduRet*`, …) are `json.RawMessage` and
echoed untouched; unknown fields are ignored.

### 7.3 `typemap.go` — translating between the wire format and `asdu.TypeID`

`asdu.TypeID.String()` returns a debug format (`"TID<C_IC_NA_1>"`), not the bare
mnemonic the protocol uses, so this file hand-builds both directions:

| Function | Direction | Notes |
| :--- | :--- | :--- |
| `typeIDFromName(name string) (asdu.TypeID, bool)` | `asduTyp` string → `asdu.TypeID` | backs `dispatch` — unknown names are rejected per-point |
| `typeNameFromID(id asdu.TypeID) string` | `asdu.TypeID` → `asduTyp` string | backs every `publishEvent` call; falls back to `id.String()` for types absent from the table (should not happen for standard 101 types) |
| `commandValueFromPayload(typeId asdu.TypeID, payload json.RawMessage) (any, error)` | `{"value": ...}` JSON → Go value | mirrors the type switch inside `wendy512/iec104`'s `doSend` (`cast.ToBoolE` for `C_SC_*`, `cast.ToUint8E` for `C_DC_*`/`C_RC_*`, `cast.ToInt16E` for `C_SE_N/TA_1`/`NB_1`/`TB_1`, `cast.ToFloat32E` for `C_SE_NC_1`/`TC_1`, `cast.ToUint32E` for `C_BO_*`) |

### 7.4 `bridge.go` — the implementation

| Type / function | Role |
| :--- | :--- |
| `point`, `request`, `readyFrame`, `ackFrame`, `eventFrame` | the JSON shapes above; `reqId` is kept as `json.RawMessage` and echoed verbatim |
| `pointKey{ca, ioa}` | the only stable join key between a dispatched point and a later inbound event — IEC 104 has no concept of the backend row `id` |
| `registry` | `sync.RWMutex`-guarded `map[pointKey]*point`; `upsert` is called once per dispatched point (even for monitoring types — see below), `lookup` is called from `publishEvent` so replies/spontaneous data can echo back `id`/`designation`/`channelId`/… |
| `commander` | the subset of `*client.Client` the bridge drives (`SendCmd`, `SendInterrogationCmd`, …); lets tests substitute a fake outstation client |
| `bridge` | holds `client commander`, `reg *registry`, `out chan any` (capacity 1024), a `done` channel and the writer's `WaitGroup` |
| `start(w io.Writer)` | launches the single writer goroutine on `w` (the real stdout) and queues the `ready` frame |
| `serve(r io.Reader) error` | reads stdin with `bufio.Reader.ReadBytes(''\n'')` — **not** `bufio.Scanner`, whose 64 KB line limit would truncate large batches; handles each line, returns `nil` on EOF |
| `handleLine` | decodes one request; invalid JSON and unknown `op` are logged to stderr and ignored; `dispatch` produces exactly one `ack` with the same `reqId` |
| `dispatch` / `dispatchPoint` | loops the points, collects failures into one `accepted:false` ack (instead of failing the batch on the first error); `dispatchPoint` resolves `asduTyp` → `typeId`, reassembles CA/IOA, **always** registers the point (`reg.upsert`) even for `M_*` types so later spontaneous data can be correlated, then issues the matching `Send*Cmd` / `SendCmd` call driven purely by `typeId` — not by the batch-level `type` |
| `publishEvent(ca, ioa, typeId, cot, value any)` | **nil-safe** (no-op on a `nil *bridge`, so `masterCall` needs no separate guard); builds the outgoing `point`, looks up known metadata via `reg.lookup` (for unknown points `type` is inferred: `M_*` → `IN`, `C_*` → `OUT`), marshals `{"value":..., "cot":...}` into `payload`, and does a **non-blocking** send into `out` |
| `writeLoop` | the only goroutine that touches stdout: marshals each frame, appends `\n`, batches whatever is queued into one flush; on `stop()` it drains the queue before returning |
| `commonAddrFromPoint` / `infoObjAddrFromPoint` | reassemble the split CA/IOA fields, big-endian, mirroring the split done by `publishEvent` |

### 7.5 `dispatch` → command, end to end

```text
backend  {"op":"dispatch","reqId":"7","type":"OUT","points":[{"asduTyp":"C_SC_NA_1","asduLow":1,"ioaLow":5,"payload":{"value":true}}]}
  │
  ▼
bridge.serve → handleLine → dispatch                     bridge.go
  │  for each point → dispatchPoint(p)
  ▼
dispatchPoint
  │  ① typeIDFromName("C_SC_NA_1") → asdu.C_SC_NA_1                    typemap.go
  │  ② commonAddrFromPoint / infoObjAddrFromPoint → ca, ioa
  │  ③ reg.upsert(ca, ioa, p)                                          ← for later correlation
  │  ④ commandValueFromPayload(C_SC_NA_1, `{"value":true}`) → true     typemap.go
  │  ⑤ b.client.SendCmd(uint16(ca), asdu.C_SC_NA_1, ioa, true)
  ▼
… same outbound path as the demo sequence (§3 Step 8) …
  ▼
TCP → outstation

ack → out channel → writer → stdout:  {"op":"ack","reqId":"7","accepted":true,"message":""}
```

The `ack` is sent once the batch is **accepted/queued**, not when the outstation
has answered.

### 7.6 Inbound event → `event` frame, end to end

```text
TCP → outstation reply (e.g. C_SC_NA_1 ActivationCon)
  ▼
… same inbound path as any reply (§3 Step 9) …
  ▼
masterCall.onCommandReply                                            call.go
  │  sc := packet.GetSingleCmd()
  │  m.bridge.publishEvent(packet.CommonAddr, sc.Ioa, packet.Type, packet.Coa, sc)
  ▼
bridge.publishEvent                                                  bridge.go
  │  ① typeNameFromID(C_SC_NA_1) → "C_SC_NA_1"                        typemap.go
  │  ② reg.lookup(ca, ioa) → the point dispatched in §7.5, reused for id/designation/…
  │  ③ json.Marshal({"value": sc, "cot": "COT<ActivationCon>"}) → payload
  │  ④ non-blocking send into the out channel
  ▼
bridge.writeLoop (single goroutine, owns stdout)
  │  json.Marshal(eventFrame) + '\n'
  ▼
backend reads {"op":"event","point":{…}} from the child's stdout
```

### 7.7 Concurrency & safety notes

- `publishEvent` is called from **every** `masterCall` callback, which all run
  on the IEC 104 library's single `handlerLoop` goroutine (§2.3). The channel
  send is therefore required to be non-blocking — a stuck or slow stdout
  reader must never stall protocol processing. When the buffer is full the
  frame is dropped and `[bridge] event buffer full, dropping frame` is logged
  to stderr rather than blocking.
- Exactly one goroutine writes stdout, so frames never interleave; a frame is
  always one `json.Marshal` result plus `\n`.
- `registry` guards its state with its own `sync.RWMutex`; `bridge` itself holds
  no additional lock.
- `dispatchPoint` runs on the stdin-reader goroutine, fully independent from
  `handlerLoop` — `client.SendCmd` is documented as safe to call from any
  goroutine (§2.3). Acks are produced there too, so they may block briefly on
  the writer but never stall `handlerLoop`.
- **stdout is protocol only.** `main` assigns `os.Stdout = os.Stderr` (and keeps
  the original file for the writer), the standard `log` package already writes
  to stderr, and the library logger gets a stderr `LogProvider`. A stray line on
  stdout would be logged by the backend as a malformed bridge line and ignored,
  but must never be relied on.
- `publishEvent` is nil-safe so `masterCall{bridge: nil}` (`-demo`, or a unit
  test that doesn't need the bridge) works without a separate guard at every
  call site.
- Tests (`bridge_test.go`) drive a `bridge` with `io.Pipe` as stdin/stdout and a
  fake `commander`: `ready`, `dispatch` → `ack` (accepted and rejected), events
  (known and unknown points), a batch larger than 64 KB, malformed lines, EOF →
  clean `serve` return, a full buffer never blocking `publishEvent`, and that
  every stdout line is valid JSON.

---## 8. Extension points

| Goal | Where to change |
| :--- | :--- |
| Send a new/another command from the demo | add an entry to the `cmds` slice in `sendDemoCommands` (`main.go`); if the ASDU type has no wrapper yet, use the generic `c.SendCmd(ca, typeId, ioa, value)` |
| React to a new monitoring type | add a `case` to the `switch client.GetDataType(packet.Type)` block in `OnASDU` (`call.go`) |
| React to a new command confirmation | add a `case` in `onCommandReply` (`call.go`) |
| Support a new `asduTyp` over the bridge | add it to `typeIDByName` (`typemap.go`); if it's a command type, also add a case to `commandValueFromPayload` and to the `dispatchPoint` type switch (`bridge.go`) |
| Add a new bridge message (`op`) | add a frame type and a `case` in `handleLine` (`bridge.go`), extend [`../IEC104_GO_BRIDGE.md`](../IEC104_GO_BRIDGE.md) and bump `protocolVersion` if the backend must know |
| Change defaults (t₀–t₃, k, w, TLS, 2-byte IOA) | build a `cs104.Config` / pick `asdu.ParamsStandard` and assign `settings.Cfg104` / `settings.Params` before `client.New` |
| Disable auto-reconnect | set `settings.AutoConnect = false` |
| Handle connection loss | `c.SetConnectionLostHandler(func(_ *client.Client) { … })` (not registered in this demo) |
| Enable diagnostics in production code | `c.SetLogCfg(client.LogCfg{Enable: true})` or a custom `clog.LogProvider` |

**Rule to respect inside any callback** (from Bug 1): an `*asdu.ASDU` is
**consumed** by its `Get*()` accessors — they advance the internal
information-object buffer. Read the parameters you need once, and never hand the
same packet back to `SendReplyMirror()`. The client here only reads, so it is
safe; but the same rule applies if a `masterCall` callback is ever made to reply.

---

## 9. References

**Application code**

- `iec104_client/main.go` — flags, settings, lifecycle, stdout/stderr split, starts the stdio bridge, `sendDemoCommands`
- `iec104_client/call.go` — `masterCall`, `onCommandReply`, `bridge.publishEvent` forwarding
- `iec104_client/bridge.go` — `bridge`, `point`, `registry`, `serve`, `dispatch`, `publishEvent`, `writeLoop`
- `iec104_client/bridge_test.go` — pipe-based protocol tests
- `iec104_client/typemap.go` — `asduTyp` ↔ `asdu.TypeID` tables, `commandValueFromPayload`

**Upstream packages (v1.0.4 / v1.2.5)**

- `github.com/wendy512/iec104/client` — `core.go` (`Client`, `Settings`,
  `Connect`, `Close`, `Send*Cmd`, `doSend`, `testConnect`), `handle.go`
  (`clientHandler`, `GetDataType`), `interface.go` (`ASDUCall`),
  `errors.go` (`NotConnected`)
- `github.com/wendy512/go-iecp5/cs104` — `client.go` (`running`, `run`,
  `recvLoop`, `sendLoop`, `handlerLoop`), `config.go` (t₀–t₃, k, w),
  `apci.go`, `clientOption.go`
- `github.com/wendy512/go-iecp5/asdu` — `asdu.go` (`ParamsWide`), `identifier.go`
  (type IDs, `infoObjSize`), `information.go`/`mproc.go`/`cproc.go` (data and
  command ASDUs)
- `github.com/wendy512/go-iecp5/clog` — `clog.go` (log disabled by default)

**Repository documents**

- [`../README.md`](../README.md) — build/run, verified run log, library bugs
- [`architecture.md`](architecture.md) — whole-system architecture and workflow, stdio bridge overview (§5)
- [`../IEC104_GO_BRIDGE.md`](../IEC104_GO_BRIDGE.md) — the NDJSON wire protocol between the backend and this client
- [`../iec_60870_5_104_bug_report.md`](../iec_60870_5_104_bug_report.md) —
  Bug 1 (`Get*Cmd()` + `SendReplyMirror`), Bug 2 (GI sequence), Bug 3
  (`infoObjSize` 58–64)

