# EasyTun

A lightweight Layer-3 tunneling project based on Go and Wintun, designed for forwarding LAN/game traffic over the public internet and for cross-site private networking scenarios.

English | [中文](README_zh.md)

[![Go Version](https://img.shields.io/badge/Go-1.25.6-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows%20Client%20%7C%20Linux%20Server-lightgrey)](#requirements)
[![Protocol](https://img.shields.io/badge/Protocol-WebSocket%20(Control)%20%2B%20UDP%20(Data)-blue)](#how-it-works-overview)
[![Build Tags](https://img.shields.io/badge/Build%20Tags-client%20%7C%20server-orange)](#build-with-scripts)

- Client: Windows (based on Wintun)
- Server: Linux/Windows (Linux recommended)
- Control Plane: WebSocket
- Data Plane: UDP

## ✨ Features

*   **Seamless Networking**:
    *   **Auto-Assignment**: Automatically assigns virtual IPs upon connection; supports unicast, broadcast, and multicast forwarding.
    *   **Internal DNS**: Built-in DNS server providing automatic hostname resolution within the `.et` domain.
    *   **Stateful Persistence**: Uses a unique `ClientID` (stored in `.client_id`) to maintain session continuity.
    *   **Out-of-the-Box**: Client embeds Wintun DLL and default config; supports both build-time embedding and external config loading.
    *   **Smart Reconnect**: Built-in exponential backoff reconnection.
*   **High-Performance Architecture**:
    *   **Lock-Free Design**: Dual-layer lock-free routing and forwarding using atomic snapshots and channel pipelines.
    *   **Resource Optimized**: Leverages `net/netip` for address management, `sync.Pool` for memory reuse, and batch I/O to minimize latency and overhead.
    *   **Data Compression**: Integrated LZ4 compression; significantly reduces bandwidth for compressible traffic (e.g., RDP, SSH) with smart probing to minimize CPU overhead.
    *   **Elastic Scaling**: Configurable worker pools for both server and client to handle various concurrent loads.
*   **Security & Traversal**:
    *   **Hardened Security**: Session establishment via Noise IK protocol; data plane fully encrypted and authenticated using ChaCha20-Poly1305.
    *   **Intelligent Traversal**: Experimental P2P hole punching (cone + symmetric NAT prediction) with seamless transition between relay and direct modes.
*   **Observability**:
    *   **Real-time Monitoring**: Optional terminal UI panel showing connection status, virtual IP, hostname, real-time throughput (Bytes), and system load.
    *   **Deep Diagnostics**: Built-in pprof endpoint on the server for performance profiling and bottleneck identification.

## 🧱 Project Structure

```text
.
├─ cmd
│  ├─ client            # client entrypoint
│  └─ server            # server entrypoint
├─ internal
│  ├─ config            # config loading
│  ├─ errorcode         # error definitions
│  ├─ protocol          # custom protocol encode/decode
│  ├─ stun              # NAT/P2P utilities
│  ├─ transport         # client/server transport logic
│  ├─ tun               # Windows Wintun implementation
│  ├─ ui                # CLI/UI helpers
│  └─ util              # initialization and network utilities
├─ assets
│  ├─ config            # config templates
│  ├─ amd64/wintun.dll  # embedded client DLL
│  ├─ client.go         # embedded resources for client build tag
│  └─ server.go         # embedded resources for server build tag
└─ scripts              # build scripts
```

## 🔁 How It Works (Overview)

1. The client starts and connects to the server via WebSocket for handshake.
2. The server assigns a virtual IP (`10.0.6.x`) and a unique hostname (e.g., `user-pc.et`) to the client.
3. The client reads IP packets from the Wintun interface, encapsulates them into EasyTun packets, then sends them over UDP.
4. The server routes and forwards packets based on destination IP or resolves hostnames via internal DNS.
5. If both peers are online, the server can coordinate P2P hole punching; once established, data can flow peer-to-peer.
6. The receiving client decapsulates and writes packets back to its local Wintun interface.

Packet header format:

```text
[magic(2)][type(1)][dst(4)][length(2)][nonce(12)][ciphertext]
```

Compression & Encryption notes:

- **Compression**: Integrated LZ4 compression, toggled via `enable_compress`. Smart probing: Only payloads > 128 bytes are considered; a 64-byte probe is compressed first to verify compressibility. The compression flag is stored in the most significant bit (`0x80`) of the `type` field.
- **Encryption**: Session bootstrap uses Noise IK (`25519 + ChaChaPoly + SHA256`). Data plane uses ChaCha20-Poly1305 AEAD after the Noise handshake completes.
- **Nonce**: `src(4)` + `counter(8)`; `src` is recovered from the nonce.
- **Length**: ciphertext length (payload + 16-byte tag). If compressed, payload is the LZ4-compressed data.
- **AAD**: header without nonce (`[magic][type][dst][length]`).

## 🧰 Requirements

- Go: recommended to use the version declared in `go.mod` (currently `1.25.6`).
- Client OS: Windows (requires Administrator privilege to create/configure virtual NIC).
- Server OS: Linux or Windows.
- Network: `server_port` must allow both TCP and UDP (same port), and `check_port` must allow UDP for P2P checks.

## ⚙️ Configuration

### 1) Client Config

Template: `assets/config/config.toml.example`

Create: `assets/config/config.toml`

Example:

```toml
[server]
# server address
server_ip = "your-public-server-ip"
# control/data port on server (TCP+UDP, same port)
server_port = 10011
# UDP check port for P2P (server listens on this port)
check_port = 10012

[device]
# Wintun adapter name
device_name = "EasyTun"

[performance]
# read timeout (supports unit: ms, s, m, h)
read_timeout = "10s"
# heartbeat interval (supports unit: ms, s, m, h)
ping_time = "1s"
# number of client send workers
send_workers = 4
# number of client receive workers
recv_workers = 4

[features]
# enable experimental P2P NAT traversal
enable_p2p = false
# enable the terminal monitoring panel
enable_ui = true
# enable LZ4 data compression
enable_compress = true
```

Fields:

- `server_ip`: server address.
- `server_port`: control/data port on server.
- `check_port`: UDP check port for P2P.
- `device_name`: Wintun adapter name.
- `read_timeout`: read timeout.
- `ping_time`: heartbeat interval.
- `send_workers`: number of client send workers.
- `recv_workers`: number of client receive workers.
- `enable_p2p`: enable experimental P2P NAT traversal.
- `enable_ui`: enable the terminal monitoring panel.
- `enable_compress`: enable LZ4 data compression.

### 2) Server Config

Template: `assets/config/server_config.toml.example`

Create: `assets/config/server_config.toml`

Example:

```toml
[server]
# server listen address
server_ip = ""
# control/data port on server
server_port = 10011
# UDP check port for P2P
check_port = 10012

[device]
# default device name
device_name = "default"
# duration to retain client state after disconnection
retention_time = "5m"

[performance]
# server read timeout
read_timeout = "10s"
# internal check interval
ping_time = "1s"
```

Note: server mainly uses `server_port`, `check_port`, `read_timeout`, and `retention_time`.

## 📌 Important: Config Is Embedded at Build Time

This project uses `go:embed` to bake config files into the binary.  
After changing `assets/config/*.toml`, you must rebuild. Running binaries will not reload updated files from disk.
The client can load a local configuration file through startup parameters.

## 🚀 Quick Start

### 1) Start Server

```bash
go build -tags server -o dist/server ./cmd/server
./dist/server
```

Default server behavior:

- WebSocket endpoint: `/ws`, port = `server_port`
- UDP listener: port = `server_port`
- UDP check listener: port = `check_port`
- pprof: port = `10021` (HTTP)

### 2) Start Client (Windows)

```powershell
go build -tags client -o dist/easytun.exe ./cmd/client
.\dist\easytun.exe
```

Client flags:

- `-t`: test mode (periodically sends broadcast packets).
- `-i`: broadcast interval in test mode (milliseconds, default `1000`).
- `-c`: Use external configuration file (configuration file path, default embedded configuration).

## 🛠️ Build

### Scripts

#### Windows Client Script

```powershell
scripts\windows_client_build.bat
```

The script will:

- Generate resource file with admin manifest.
- Build client with `-tags client` to `dist/easytun.exe`.

#### Linux Server Script

```powershell
scripts\linux_server_build.bat
```

The script will:

- Cross-build Linux `amd64` server binary.
- Attempt to build Docker image.
- Auto-generate version tag and optionally push to registry when `REGISTRY` is set.

### Makefile

```bash
make server
make client
make docker
make clean
```

Targets:

- `server`: build Linux `amd64` server binary into `dist/`.
- `client`: build Windows `amd64` client binary with manifest and icon.
- `docker`: build and tag Docker image (and push if `REGISTRY` is set).
- `clean`: remove build artifacts.

### Docker

Use the Makefile target `docker`, or manual build below.

#### Docker Deployment (Server)

### Option A: Manual Build (Recommended)

1. Build `dist/server` first:

```bash
go build -tags server -ldflags "-s -w" -o dist/server ./cmd/server
```

2. Build image:

```bash
docker build -t easytun-server:latest .
```

3. Run container:

```bash
docker run -d --name easytun-server \
  -p 10011:10011/tcp \
  -p 10011:10011/udp \
  -p 10012:10012/udp \
  -p 10021:10021/tcp \
  easytun-server:latest
```

### Option B: Build via Script

`scripts/linux_server_build.bat` can run build and `docker build` automatically.

## 🩺 Troubleshooting

- Build error `pattern config/...: no matching files found`:
  - Missing `assets/config/config.toml` or `assets/config/server_config.toml`.
  - Copy from `.example` and rebuild.
- Client cannot create adapter or set IP:
  - Run client with Administrator privilege.
- WS connected but no data forwarding:
  - Check server firewall/security group allows both TCP and UDP on the same port.
- P2P not establishing:
  - Ensure `check_port` UDP is open and client/server configs match.
- Client exits right after startup:
  - Verify `server_ip` and `server_port`.

## ⚠️ Known Limitations

- Client currently supports Windows only (via Wintun).
- Current server batch I/O path is Linux-only (via `sendmmsg`/`recvmmsg` syscalls).
- P2P is experimental; symmetric NAT success rate is best-effort.
- No complete automated test suite yet.

## ✅ TODO List

- [x] Complete control-plane message handling (client `controlRecv` logic).
- [x] Improve P2P traversal (stability, symmetric NAT strategy).
- [ ] Add HTTPS support for control messages.
- [x] Add UDP data encryption support.
- [x] Refactor server UDP RX/TX and forwarding into a configurable worker pool.
- [ ] Optimize server-side broadcast forwarding with broadcast-to-unicast conversion.
- [ ] Add Linux client support (TUN/TAP).
- [x] Support config hot reload or external config loading (instead of embed-only mode).
- [x] Add Internal DNS support with `.et` domain resolution.
- [x] Implement client persistence with `ClientID` and server-side state retention.
- [x] Add observability metrics (connections, throughput, packet loss, forwarding latency).
- [ ] Add CI pipeline (build, test, artifact release).

## 📄 License

MIT
).
- [x] Support config hot reload or external config loading (instead of embed-only mode).
- [ ] Add observability metrics (connections, throughput, packet loss, forwarding latency).
- [ ] Add CI pipeline (build, test, artifact release).

## 📄 License

MIT
