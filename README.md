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

- Automatically assigns a virtual IP (`10.0.6.0/24`) to each connected client.
- Client binary embeds both DLL and config file for out-of-the-box usage.
- Supports unicast, broadcast, and multicast forwarding.
- Separates control and data channels to reduce data path overhead.
- Uses `sync.Pool` and batch I/O to reduce memory allocation and syscall overhead.
- Built-in heartbeat and read-timeout mechanism.
- Built-in pprof endpoint on server (default `10021`).

## 🧱 Project Structure

```text
.
├─ cmd
│  ├─ client            # client entrypoint
│  └─ server            # server entrypoint
├─ internal
│  ├─ config            # config loading
│  ├─ protocol          # custom protocol encode/decode
│  ├─ transport         # client/server transport logic
│  ├─ tun               # Windows Wintun implementation
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
2. The server assigns a virtual IP (`10.0.6.x`) to the client.
3. The client reads IP packets from the Wintun interface, encapsulates them into EasyTun packets, then sends them over UDP.
4. The server routes and forwards packets based on destination IP.
5. The receiving client decapsulates and writes packets back to its local Wintun interface.

Packet header format:

```text
[magic(2)][type(1)][src(4)][dst(4)][length(2)][payload]
```

## 🧰 Requirements

- Go: recommended to use the version declared in `go.mod` (currently `1.25.6`).
- Client OS: Windows (requires Administrator privilege to create/configure virtual NIC).
- Server OS: Linux or Windows.
- Network: server port must allow both TCP and UDP (same port).

## ⚙️ Configuration

### 1) Client Config

Template: `assets/config/config.json.example`

Create: `assets/config/config.json`

Example:

```json
{
  "server_ip": "your-public-server-ip",
  "server_port": 10011,
  "device_name": "EasyTun",
  "read_timeout": 10,
  "ping_time": 1,
  "send_workers": 4,
  "recv_workers": 4
}
```

Fields:

- `server_ip`: server address.
- `server_port`: control/data port on server (TCP+UDP, same port).
- `device_name`: Wintun adapter name.
- `read_timeout`: read timeout (seconds).
- `ping_time`: heartbeat interval (seconds).
- `send_workers`: number of client send workers.
- `recv_workers`: number of client receive workers.

### 2) Server Config

Template: `assets/config/server_config.json.example`

Create: `assets/config/server_config.json`

Example:

```json
{
  "server_ip": "",
  "server_port": 10011,
  "device_name": "default",
  "read_timeout": 10,
  "ping_time": 1
}
```

Note: server mainly uses `server_port` and `read_timeout`.

## 📌 Important: Config Is Embedded at Build Time

This project uses `go:embed` to bake config files into the binary.  
After changing `assets/config/*.json`, you must rebuild. Running binaries will not reload updated files from disk.

## 🚀 Quick Start

### 1) Start Server

```bash
go build -tags server -o dist/server ./cmd/server
./dist/server
```

Default server behavior:

- WebSocket endpoint: `/ws`, port = `server_port`
- UDP listener: port = `server_port`
- pprof: `10021` (HTTP)

### 2) Start Client (Windows)

```powershell
go build -tags client -o dist/easytun.exe ./cmd/client
.\dist\easytun.exe
```

Client flags:

- `-t`: test mode (periodically sends broadcast packets).
- `-i`: broadcast interval in test mode (seconds, default `5`).

## 🛠️ Build with Scripts

### Windows Client Script

```powershell
scripts\windows_client_build.bat
```

The script will:

- Generate resource file with admin manifest.
- Build client with `-tags client` to `dist/easytun.exe`.

### Linux Server Script

```powershell
scripts\linux_server_build.bat
```

The script will:

- Cross-build Linux `amd64` server binary.
- Attempt to build Docker image.

## 🐳 Docker Deployment (Server)

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
  -p 10021:10021/tcp \
  easytun-server:latest
```

### Option B: Build via Script

`scripts/linux_server_build.bat` can run build and `docker build` automatically.

## 🩺 Troubleshooting

- Build error `pattern config/...: no matching files found`:
  - Missing `assets/config/config.json` or `assets/config/server_config.json`.
  - Copy from `.example` and rebuild.
- Client cannot create adapter or set IP:
  - Run client with Administrator privilege.
- WS connected but no data forwarding:
  - Check server firewall/security group allows both TCP and UDP on the same port.
- Client exits right after startup:
  - Verify `server_ip` and `server_port`.

## ⚠️ Known Limitations

- Client currently supports Windows only (via Wintun).
- Current server batch I/O path is Linux-only (via `sendmmsg`/`recvmmsg` syscalls).
- Control message handling and worker-pool optimization are still in progress.
- No complete automated test suite yet.

## ✅ TODO List

- [ ] Complete control-plane message handling (client `controlRecv` logic).
- [ ] Implement P2P traversal for NAT penetration.
- [ ] Add HTTPS support for control messages.
- [ ] Add UDP data encryption support.
- [ ] Refactor server UDP RX/TX and forwarding into a configurable worker pool.
- [ ] Add Linux client support (TUN/TAP).
- [x] Support config hot reload or external config loading (instead of embed-only mode).
- [ ] Add observability metrics (connections, throughput, packet loss, forwarding latency).
- [ ] Add CI pipeline (build, test, artifact release).

## 📄 License

MIT
