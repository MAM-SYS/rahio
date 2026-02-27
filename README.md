# Rahio

A userspace multipath transport library for Go. Rahio bonds multiple TCP connections — each on a different network interface — into a single `net.Conn`, enabling bandwidth aggregation and path redundancy without kernel-level MPTCP support.

> **Note:** This is an educational and research project. It is not intended for production use. The goal is to explore multipath transport concepts, protocol design, and Go networking internals.

---

## Architecture

```
Application
    │
    ▼
MultipathConn  (net.Conn)
    │
    ├── Subflow 0  (TCP / eth0)
    ├── Subflow 1  (TCP / wlan0)
    └── Subflow N  (TCP / ...)
```

Packets are assigned monotonic sequence numbers, distributed across subflows by a pluggable scheduler, and reassembled in order at the receiver.

## Status

| Layer | File(s) | Status |
|-------|---------|--------|
| Packet encoding | `packet.go` | ✅ Done |
| Scheduler interface + Round-robin | `scheduler/` | ✅ Done |
| Subflow state machine | `subflow.go` | ✅ Done |
| MultipathConn core | `conn.go` | ✅ Done |
| Handshake (dial + listen) | `dialer.go`, `listener.go` | ✅ Done |
| Flow control + Failover | `conn.go` additions | 🔲 Pending |

## Requirements

- Go 1.21+

## References

- Internal protocol specification: [`PROTOCOL-SPEC.md`](./PROTOCOL-SPEC.md)
- Inspired by [RFC 8684](https://www.rfc-editor.org/rfc/rfc8684) (MPTCP v1) and the Linux kernel MPTCP scheduler API

## License

MIT
