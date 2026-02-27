# Rahio Multipath Protocol — Theoretical Specification

> **Version**: 0.1 (Draft)
> **Date**: 2026-02-17
> **Status**: Theoretical Design

---

## Table of Contents

1. [Overview](#1-overview)
2. [Terminology](#2-terminology)
3. [Architecture](#3-architecture)
4. [Connection Lifecycle](#4-connection-lifecycle)
5. [Packet Format](#5-packet-format)
6. [Subflow Management](#6-subflow-management)
7. [Scheduler Specification](#7-scheduler-specification)
8. [Sequence and Reassembly](#8-sequence-and-reassembly)
9. [Flow Control](#9-flow-control)
10. [Failover](#10-failover)
11. [Bidirectional Data Transfer](#11-bidirectional-data-transfer)

---

## 1. Overview

**Rahio** is a userspace multipath transport protocol implemented in Go.
It splits a single application byte stream across multiple TCP subflows,
each bound to a different network interface (ISP), and reassembles them
in order at the receiver.

The protocol is **application-layer agnostic** — it sits between the
application and TCP, providing a `net.Conn`-like interface. The application
does not know multipath is in use.

### Core Properties

| Property | Value |
|----------|-------|
| **Transport** | TCP (one TCP connection per subflow) |
| **Interface** | `net.Conn` compatible (Read/Write/Close) |
| **Scheduling** | Pluggable (default: round-robin) |
| **Ordering** | Guaranteed (sequence-number based reassembly) |
| **Directions** | Full-duplex (independent schedulers per direction) |
| **Obfuscation** | None at this layer (handled by upper layers) |

---

## 2. Terminology

| Term | Definition |
|------|-----------|
| **Connection** | One logical Rahio session between client and server |
| **Subflow** | One TCP connection within a connection |
| **Scheduler** | Algorithm that selects which subflow to send next chunk on |
| **Chunk** | A fixed or variable-size fragment of the application byte stream |
| **Packet** | A chunk plus Rahio metadata (sequence number, subflow ID, etc.) |
| **Sequence Number** | Global counter that orders all packets across all subflows |
| **Reassembly Buffer** | Receiver-side store for out-of-order packets |
| **State** | Per-connection scheduler storage (mirrors kernel SK_STORAGE) |

---

## 3. Architecture

```
┌───────────────────────────────────────────────────────┐
│  APPLICATION                                          │
│  Any protocol (HTTP, SSH, VPN, raw bytes)             │
└───────────────────────────────────────────────────────┘
             │ Write([]byte)   Read([]byte)
             ↓
┌───────────────────────────────────────────────────────┐
│  RAHIO MULTIPATH ENGINE                              │
│  ─────────────────────────────────────────────────    │
│  send path:                                           │
│    1. Split data into chunks                          │
│    2. Assign sequence numbers                         │
│    3. Scheduler selects subflow                       │
│    4. Serialize packet                                │
│    5. Write to subflow TCP connection                 │
│                                                       │
│  receive path:                                        │
│    1. Read from all subflow connections               │
│    2. Deserialize packet                              │
│    3. Insert into reassembly buffer                   │
│    4. Deliver in-order to application                 │
└───────────────────────────────────────────────────────┘
             │ per-subflow TCP Write/Read
             ↓
┌──────────┐  ┌──────────┐  ┌──────────┐
│ Subflow 0│  │ Subflow 1│  │ Subflow N│
│ TCP conn │  │ TCP conn │  │ TCP conn │
│ via eth0 │  │ via wlan0│  │ via ppp0 │
└──────────┘  └──────────┘  └──────────┘
```

---

## 4. Connection Lifecycle

### 4.1 Handshake

The client opens one TCP connection per subflow to the server.
All subflows connect to the **same server address and port**.

The first packet on each subflow is a **HANDSHAKE** packet:

```
Client → Server (each subflow)

HANDSHAKE Packet:
  ConnectionID : [16 bytes random UUID]  ← ties all subflows together
  NumSubflows  : [1 byte]                ← total number of subflows
  SubflowIndex : [1 byte]                ← index of this subflow (0-N)
  Version      : [1 byte]                ← protocol version
```

The server groups subflows by `ConnectionID`. When all `NumSubflows`
connections have arrived, the server creates a `MultipathConn` and
makes it available to the application via `Accept()`.

**This mirrors the kernel's approach**: all MPTCP subflows share one
`mptcp_sock` identified by a connection token.

### 4.2 Data Transfer

After handshake, both sides enter data transfer mode.
Each side runs its own independent scheduler (send direction only).

### 4.3 Close

When the application calls `Close()`:
1. A **CLOSE** packet is sent on all subflows.
2. All subflow TCP connections are closed.
3. Scheduler state is released (mirrors kernel's `sched_ops.release`).

---

## 5. Packet Format

Every Rahio packet follows this binary layout.
All integers are **big-endian**.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
├─────────────────────────────────────────────────────────────────┤
│  Version (8)    │  Type (8)       │  SubflowIndex (8) │ Flags(8)│
├─────────────────────────────────────────────────────────────────┤
│                     ConnectionID [0:3] (32)                     │
├─────────────────────────────────────────────────────────────────┤
│                     ConnectionID [4:7] (32)                     │
├─────────────────────────────────────────────────────────────────┤
│                     ConnectionID [8:11] (32)                    │
├─────────────────────────────────────────────────────────────────┤
│                     ConnectionID [12:15] (32)                   │
├─────────────────────────────────────────────────────────────────┤
│                     SequenceNumber [0:3] (32)                   │
├─────────────────────────────────────────────────────────────────┤
│                     SequenceNumber [4:7] (32)                   │
├─────────────────────────────────────────────────────────────────┤
│                     Timestamp (64)  [0:3]                       │
├─────────────────────────────────────────────────────────────────┤
│                     Timestamp (64)  [4:7]                       │
├─────────────────────────────────────────────────────────────────┤
│                     DataLength (32)                             │
├─────────────────────────────────────────────────────────────────┤
│                     Checksum (32)                               │
├─────────────────────────────────────────────────────────────────┤
│                     Data (variable)                             │
│                     ...                                         │
└─────────────────────────────────────────────────────────────────┘

Total header size: 48 bytes
Total packet size: 48 + DataLength bytes
```

### 5.1 Field Definitions

| Field | Size | Description |
|-------|------|-------------|
| **Version** | 1 byte | Protocol version. Currently `0x01` |
| **Type** | 1 byte | Packet type (see §5.2) |
| **SubflowIndex** | 1 byte | Index of subflow that sent this packet (0-255) |
| **Flags** | 1 byte | Bitmask (see §5.3) |
| **ConnectionID** | 16 bytes | Random UUID shared by all subflows of one connection |
| **SequenceNumber** | 8 bytes | Global monotonic counter across ALL subflows |
| **Timestamp** | 8 bytes | Microseconds since epoch. Used for RTT measurement |
| **DataLength** | 4 bytes | Length of Data field in bytes |
| **Checksum** | 4 bytes | CRC32 over all header fields + Data |
| **Data** | variable | Application payload for this packet |

### 5.2 Packet Types

| Value | Name | Description |
|-------|------|-------------|
| `0x00` | `DATA` | Carries application payload |
| `0x01` | `HANDSHAKE` | Opens a subflow and identifies connection |
| `0x02` | `HANDSHAKE_ACK` | Server confirms subflow accepted |
| `0x03` | `ACK` | Acknowledges received sequence numbers |
| `0x04` | `CLOSE` | Signals connection teardown |
| `0x05` | `PING` | Measures RTT on a subflow |
| `0x06` | `PONG` | Reply to PING |

### 5.3 Flags

| Bit | Name | Meaning |
|-----|------|---------|
| `0x01` | `FIN` | Last packet in this direction |
| `0x02` | `RST` | Reset — abort connection immediately |
| `0x04` | `SCHEDULED` | This subflow was selected by scheduler (mirrors `mptcp_subflow_set_scheduled`) |

### 5.4 Wire Framing

Every Rahio packet is prefixed with its length before being written
to the TCP connection:

```
[4 bytes: packet length (uint32)] [N bytes: packet]
```

This allows the receiver to extract complete packets from the TCP
byte stream regardless of packet boundaries.

---

## 6. Subflow Management

### 6.1 Subflow States

Each subflow has a state machine:

```
CONNECTING → ACTIVE → CLOSING → CLOSED
                ↓
            DEGRADED   (RTT too high, not preferred)
```

### 6.2 Subflow Data Structure

Modelled after kernel's `mptcp_subflow_context`:

```
Subflow {
    Index       : uint8         // Position in subflow list
    TCPConn     : net.Conn      // Underlying TCP connection
    Interface   : string        // Network interface name ("eth0")
    LocalAddr   : net.IP        // Local IP on this interface
    RemoteAddr  : net.IP        // Server IP reached via this interface
    State       : SubflowState  // ACTIVE, DEGRADED, etc.
    RTT         : duration      // Measured round-trip time
    BytesSent   : uint64        // Total bytes sent on this subflow
    BytesRecv   : uint64        // Total bytes received on this subflow
    Scheduled   : bool          // Selected by scheduler for next send
                                // (mirrors mptcp_subflow_set_scheduled)
}
```

### 6.3 Subflow List

Each connection maintains an ordered list of subflows.
The order is fixed after the handshake.
This matches the kernel: `msk->first` is the first subflow,
subsequent subflows are linked as a list.

```
Connection.Subflows = [Subflow0, Subflow1, Subflow2, ...]
                           ↑
                       msk->first equivalent
```

---

## 7. Scheduler Specification

The scheduler is a **pluggable component** — modelled directly after
the kernel's `struct mptcp_sched_ops`.

### 7.1 Scheduler Interface

```
SchedulerOps {
    Name()                      string   // Identifier (e.g. "roundrobin")
    Init(conn: Connection)               // Called when connection is created
    Release(conn: Connection)            // Called when connection is destroyed
    SelectSubflow(conn: Connection) int  // Returns index of next subflow
}
```

This is the **exact equivalent** of the kernel's:
```c
struct mptcp_sched_ops {
    void (*init)    (struct mptcp_sock *msk);
    void (*release) (struct mptcp_sock *msk);
    int  (*get_send)(struct mptcp_sock *msk);
    char name[16];
};
```

### 7.2 Scheduler State Storage

Each scheduler maintains **per-connection state**.
This mirrors the kernel's `BPF_MAP_TYPE_SK_STORAGE` —
a per-socket storage map that associates scheduler data
with each connection.

```
SchedulerStorage {
    connectionID → SchedulerState
}
```

The state is created in `Init()` and destroyed in `Release()`.
It is private to the scheduler and opaque to the connection.

### 7.3 Round-Robin Scheduler — Formal Specification

This scheduler is a direct translation of the kernel's
`mptcp_bpf_rr.c` into protocol-level terms.

#### State

```
RoundRobinState {
    LastSentSubflow : int   // Index of subflow used most recently
                            // -1 means no packet has been sent yet
                            // Mirrors kernel's: struct sock *last_snd
}
```

#### Init

```
procedure Init(conn):
    state = RoundRobinState { LastSentSubflow: -1 }
    storage[conn.ConnectionID] = state
```

#### Release

```
procedure Release(conn):
    delete storage[conn.ConnectionID]
```

#### SelectSubflow (the core algorithm)

This directly mirrors `bpf_rr_get_send()`:

```
procedure SelectSubflow(conn) → subflowIndex:

    state = storage[conn.ConnectionID]
    subflows = conn.Subflows                // Ordered list

    // Step 1: Default to first subflow (mirrors: next = msk->first)
    next = 0

    // Step 2: If no history, use first subflow (mirrors: if !ptr->last_snd goto out)
    if state.LastSentSubflow == -1:
        goto mark_and_return

    // Step 3: Find the subflow used last time
    //         (mirrors: bpf_for_each(mptcp_subflow, subflow, msk))
    for i = 0 to len(subflows)-1:
        if i == state.LastSentSubflow:

            // Step 4: Advance to next subflow
            //         (mirrors: bpf_iter_mptcp_subflow_next(&___it))
            nextIndex = i + 1

            // Step 5: Wrap around if at end (mirrors: if !subflow break)
            if nextIndex >= len(subflows):
                next = 0        // Wrap to first
            else:
                next = nextIndex

            break

mark_and_return:
    // Step 6: Mark as scheduled (mirrors: mptcp_subflow_set_scheduled(next, true))
    subflows[next].Scheduled = true

    // Step 7: Remember choice (mirrors: ptr->last_snd = mptcp_subflow_tcp_sock(next))
    state.LastSentSubflow = next

    return next
```

#### Example Execution

```
Subflows: [0, 1, 2]

SelectSubflow():  LastSentSubflow=-1 → next=0 → LastSentSubflow=0 → return 0
SelectSubflow():  LastSentSubflow=0  → find 0 → next=1 → LastSentSubflow=1 → return 1
SelectSubflow():  LastSentSubflow=1  → find 1 → next=2 → LastSentSubflow=2 → return 2
SelectSubflow():  LastSentSubflow=2  → find 2 → next=3 → wrap → next=0 → return 0
SelectSubflow():  LastSentSubflow=0  → find 0 → next=1 → LastSentSubflow=1 → return 1
...
```

### 7.4 Other Schedulers (Future)

The same `SchedulerOps` interface supports other schedulers:

| Name | Selection Rule |
|------|---------------|
| `roundrobin` | Rotate through subflows sequentially |
| `minrtt` | Always pick subflow with lowest measured RTT |
| `redundant` | Send every chunk on ALL subflows simultaneously |
| `weighted` | Distribute proportional to subflow bandwidth |
| `backup` | Use secondary subflows only when primary fails |

---

## 8. Sequence and Reassembly

### 8.1 Sequence Number Assignment

The **SequenceNumber** is a global, monotonically increasing counter
shared across all subflows of one connection — one counter per direction.

```
SendSequencer {
    next : uint64   // Starts at 0
}

procedure NextSequenceNumber():
    n = next
    next = next + 1
    return n
```

Each chunk sent in the **send direction** gets a unique sequence number,
regardless of which subflow carries it.

This mirrors kernel MPTCP's **Data Sequence Number (DSN)**,
which is also global across all subflows.

### 8.2 Reassembly Buffer

The receiver maintains one reassembly buffer per connection per direction.

```
ReassemblyBuffer {
    NextExpected : uint64              // Next sequence number to deliver
    Buffer       : map[uint64 → Chunk] // Out-of-order storage
    Output       : channel of []byte   // In-order delivery to application
}
```

#### Insert Algorithm

```
procedure Insert(seqNum, data):

    if seqNum == NextExpected:
        // In order — deliver immediately
        Output ← data
        NextExpected++

        // Deliver any consecutive buffered packets
        while Buffer[NextExpected] exists:
            Output ← Buffer[NextExpected]
            delete Buffer[NextExpected]
            NextExpected++

    else if seqNum > NextExpected:
        // Out of order — buffer it
        Buffer[seqNum] = data

    else:
        // Duplicate or old packet — discard
        discard
```

#### Example

```
NextExpected = 3

Receive SeqNum=4 → buffer {4: data4}          (out of order)
Receive SeqNum=5 → buffer {4: data4, 5: data5} (out of order)
Receive SeqNum=3 → deliver data3               (in order!)
                 → NextExpected=4
                 → buffer has 4? YES → deliver data4
                 → NextExpected=5
                 → buffer has 5? YES → deliver data5
                 → NextExpected=6
                 → buffer has 6? NO → stop

Application receives: data3, data4, data5 in order ✓
```

---

## 9. Flow Control

Each connection has a **receive window** — the maximum number of
unacknowledged bytes the receiver can hold in its reassembly buffer.

```
FlowControl {
    SendWindow  : uint32    // How many bytes we can send
    RecvWindow  : uint32    // How many bytes we will accept
}
```

### 9.1 Window Advertisements

The receiver advertises its available window in ACK packets:

```
ACK Packet {
    Type            : ACK
    AckedSeqNum     : uint64  // All packets up to this are received
    AdvertisedWindow: uint32  // Receiver can accept this many more bytes
}
```

### 9.2 Sender Behaviour

The sender blocks `Write()` when:

```
BytesInFlight > SendWindow
```

`BytesInFlight` = highest sent sequence number − highest acknowledged sequence number.

---

## 10. Failover

When a subflow TCP connection drops:

```
procedure OnSubflowFailure(failedSubflow):

    // 1. Mark subflow as inactive
    failedSubflow.State = CLOSED

    // 2. Find which packets were sent on this subflow but not ACKed
    lostPackets = unacknowledgedPackets(failedSubflow)

    // 3. Re-queue lost packets for retransmission
    for packet in lostPackets:
        sendQueue.prepend(packet)    // Priority re-send

    // 4. Update scheduler state
    //    Remove failed subflow from rotation
    //    (mirrors kernel removing a failed subflow from mptcp_sock)
    scheduler.RemoveSubflow(failedSubflow.Index)

    // 5. Continue sending on remaining subflows
    //    If no subflows remain, return error to application
    if len(activeSubflows) == 0:
        connection.Close()
        return ErrNoSubflows
```

---

## 11. Bidirectional Data Transfer

Each direction is **completely independent**:

```
Client → Server direction:
    Scheduler: client's RoundRobinState
    Sequencer: client's send sequence counter
    Reassembler: server's reassembly buffer

Server → Client direction:
    Scheduler: server's RoundRobinState
    Sequencer: server's send sequence counter
    Reassembler: client's reassembly buffer
```

Both directions use the **same subflows** (TCP connections are full-duplex).
But each direction has its own sequence counter and scheduler state.

### 11.1 Simultaneous Send and Receive

```
Connection (Client Side):

  Write("REQUEST") ──→ send scheduler ──→ subflow 0, 1, 2 (round-robin)
                                                   ↓
  Read()          ←── reassembly buffer ←── subflow 0, 1, 2 (all listening)

Connection (Server Side):

  Read()          ←── reassembly buffer ←── subflow 0, 1, 2 (all listening)
                                                   ↑
  Write("RESPONSE") ─→ send scheduler ──→ subflow 0, 1, 2 (round-robin)
```

Both sides send and receive simultaneously on the same TCP connections.
This is possible because TCP is full-duplex.

### 11.2 Response Chunking Example

```
REQUEST: "GET /file" (9 bytes, 2 subflows)

  Client scheduler sends:
    Chunk 0 [SeqNum=0] → Subflow 0  →→→→→→→ Server receives SeqNum=0
    Chunk 1 [SeqNum=1] → Subflow 1  →→→→→→→ Server receives SeqNum=1

  Server reassembles: "GET /file" ✓

RESPONSE: "FILE DATA..." (1MB, 2 subflows)

  Server scheduler sends:
    Chunk 0 [SeqNum=0] → Subflow 0  →→→→→→→ Client receives SeqNum=0
    Chunk 1 [SeqNum=1] → Subflow 1  →→→→→→→ Client receives SeqNum=1
    Chunk 2 [SeqNum=2] → Subflow 0  →→→→→→→ Client receives SeqNum=2
    ...

  Client reassembles: full response ✓
```

**Note**: Server's response sequence numbers start at 0 — they are
independent of the client's request sequence numbers.

---

## Summary: Protocol Invariants

These properties must hold at all times:

1. **Every packet has a unique sequence number within its direction**
2. **Sequence numbers are monotonically increasing, no gaps**
3. **The reassembly buffer delivers bytes in sequence-number order**
4. **Scheduler state is per-connection, never shared**
5. **A subflow carries packets from only one direction's scheduler**
6. **Round-robin wraps to subflow 0 after the last subflow**
7. **A failed subflow's unacknowledged packets are retransmitted on surviving subflows**
8. **Both sides can send simultaneously on the same subflow (TCP full-duplex)**
