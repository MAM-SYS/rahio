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
| Flow control | `conn.go` | ✅ Done |
| Failover | `conn.go` additions | 🔲 Pending |

## Quick Start
Initiate the `Rahio` server by executing the following command in a terminal shell:
```shell
# --listen :9000: Start the server and listen on port 9000
# --chunk 32: Configure each request packet as 32 bytes
go run main.go server --listen :9000 --chunk 32
```

Launch the `Rahio` server in a separate terminal session by executing the following command:
```shell
# --proxy: Specifies a localhost socks5 proxy to pass machine traffic through.
# --server 127.0.0.01:9000: Specifies the `Rahio` server to connect to.
# --ifaces lo0,lo0: Specifies a comma-separated list of network interfaces. For ease of startup, we repeated the localhost loopback interface twice.
# --chunk 32: Configures each response packet as 32 bytes.
go run main.go client --proxy 127.0.0.1:1080 --server 127.0.0.1:9000 --ifaces lo0,lo0 --chunk 32
```

Initiate a request through the client's proxy by executing the following command:
```shell
curl --proxy socks5://127.0.0.1:1080 https://ifconfig.me
```

## Requirements

- Go 1.21+

## References

- Internal protocol specification: [`PROTOCOL-SPEC.md`](./PROTOCOL-SPEC.md)
- Inspired by [RFC 8684](https://www.rfc-editor.org/rfc/rfc8684) (MPTCP v1) and the Linux kernel MPTCP scheduler API

## License

MIT
