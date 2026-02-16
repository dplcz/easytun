# EasyTun

一个基于 Go 和 Wintun 的轻量级三层隧道程序，面向“把局域网/游戏流量在公网中转”，以及异地组网的场景。

[English](README.md) | 中文

[![Go Version](https://img.shields.io/badge/Go-1.25.6-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows%20Client%20%7C%20Linux%20Server-lightgrey)](#环境要求)
[![Protocol](https://img.shields.io/badge/Protocol-WebSocket%20(Control)%20%2B%20UDP%20(Data)-blue)](#工作原理简述)
[![Build Tags](https://img.shields.io/badge/Build%20Tags-client%20%7C%20server-orange)](#使用脚本编译)

- 客户端：Windows（基于 Wintun）
- 服务端：Linux/Windows（推荐 Linux）
- 控制面：WebSocket
- 数据面：UDP

## ✨ 功能特性

- 支持客户端接入后自动分配虚拟 IP（`10.0.6.0/24`）。
- 客户端已嵌入DLL和配置文件，开箱即用。
- 支持单播转发、广播转发、组播转发。
- 控制通道与数据通道分离，降低数据传输开销。
- 使用 `sync.Pool`、批量读写等方式减少内存分配和系统调用开销。
- 内置心跳与读超时机制。
- 服务端内置 pprof 入口（默认 `10021` 端口）。

## 🧱 项目结构

```text
.
├─ cmd
│  ├─ client            # 客户端入口
│  └─ server            # 服务端入口
├─ internal
│  ├─ config            # 配置加载
│  ├─ protocol          # 自定义协议封包/解包
│  ├─ transport         # 客户端与服务端传输逻辑
│  ├─ tun               # Windows Wintun 相关实现
│  └─ util              # 初始化、网络探测等工具
├─ assets
│  ├─ config            # 配置模板
│  ├─ amd64/wintun.dll  # 客户端内嵌 DLL
│  ├─ client.go         # client tag 下嵌入资源
│  └─ server.go         # server tag 下嵌入资源
└─ scripts              # 构建脚本
```

## 🔁 工作原理（简述）

1. 客户端启动后连接服务端 WebSocket 并握手。
2. 服务端为客户端分配虚拟 IP（`10.0.6.x`）。
3. 客户端从 Wintun 网卡读取 IP 包，封装为 EasyTun 协议后通过 UDP 发送。
4. 服务端根据目标 IP 路由并转发给对应客户端。
5. 接收方客户端解包后写回本地 Wintun 网卡。

协议头格式：

```text
[magic(2)][type(1)][src(4)][dst(4)][length(2)][payload]
```

## 🧰 环境要求

- Go：建议使用 `go.mod` 中声明版本（当前为 `1.25.6`）。
- 客户端系统：Windows（需管理员权限运行，创建/配置网卡）。
- 服务端系统：Linux 或 Windows。
- 网络：服务端监听端口需放行 TCP+UDP（同一端口）。

## ⚙️ 配置说明

### 1) 客户端配置

模板文件：`assets/config/config.json.example`

请复制并创建：`assets/config/config.json`

示例：

```json
{
  "server_ip": "你的服务器公网IP",
  "server_port": 10011,
  "device_name": "EasyTun",
  "read_timeout": 10,
  "ping_time": 1,
  "send_workers": 4,
  "recv_workers": 4
}
```

字段说明：

- `server_ip`：服务端地址。
- `server_port`：服务端控制面/数据面端口（TCP+UDP 同端口）。
- `device_name`：Wintun 网卡名。
- `read_timeout`：读超时（秒）。
- `ping_time`：心跳间隔（秒）。
- `send_workers`：客户端发包协程数。
- `recv_workers`：客户端收包协程数。

### 2) 服务端配置

模板文件：`assets/config/server_config.json.example`

请复制并创建：`assets/config/server_config.json`

示例：

```json
{
  "server_ip": "",
  "server_port": 10011,
  "device_name": "default",
  "read_timeout": 10,
  "ping_time": 1
}
```

说明：服务端主要使用 `server_port`、`read_timeout` 等参数。

## 📌 重要说明：配置是“编译时嵌入”

本项目使用 `go:embed` 将配置文件打进二进制。  
修改 `assets/config/*.json` 后，需要重新编译，运行中的二进制不会自动读取磁盘新配置。

## 🚀 快速开始

### 1) 启动服务端

```bash
go build -tags server -o dist/server ./cmd/server
./dist/server
```

服务端默认行为：

- WebSocket 监听：`/ws`，端口为 `server_port`
- UDP 监听：端口为 `server_port`
- pprof：`10021`（HTTP）

### 2) 启动客户端（Windows）

```powershell
go build -tags client -o dist/easytun.exe ./cmd/client
.\dist\easytun.exe
```

客户端参数：

- `-t`：测试模式（周期性发送广播包）。
- `-i`：测试模式广播间隔（秒，默认 `5`）。

## 🛠️ 使用脚本编译

### Windows 客户端脚本

```powershell
scripts\windows_client_build.bat
```

脚本会：

- 生成带管理员权限 manifest 的资源文件。
- 使用 `-tags client` 编译客户端到 `dist/easytun.exe`。

### Linux 服务端脚本

```powershell
scripts\linux_server_build.bat
```

脚本会：

- 交叉编译 Linux `amd64` 服务端。
- 尝试构建 Docker 镜像。

## 🐳 Docker 部署（服务端）

### 方式 A：手动构建（推荐）

1. 先生成 `dist/server`：

```bash
go build -tags server -ldflags "-s -w" -o dist/server ./cmd/server
```

2. 构建镜像：

```bash
docker build -t easytun-server:latest .
```

3. 运行容器：

```bash
docker run -d --name easytun-server \
  -p 10011:10011/tcp \
  -p 10011:10011/udp \
  -p 10021:10021/tcp \
  easytun-server:latest
```

### 方式 B：使用脚本

`scripts/linux_server_build.bat` 会自动执行编译与 `docker build`。

## 🩺 故障排查

- 编译报错 `pattern config/...: no matching files found`：
    - 说明缺少 `assets/config/config.json` 或 `assets/config/server_config.json`。
    - 请从 `.example` 复制生成正式文件后重新编译。
- 客户端无法创建网卡或设置 IP：
    - 请以管理员权限运行客户端。
- 客户端与服务端能连上 WS 但无数据：
    - 检查服务端端口是否同时放行 TCP 与 UDP。
- 客户端启动即退出：
    - 检查配置中的 `server_ip`、`server_port` 是否正确。

## ⚠️ 已知限制

- 当前客户端仅支持 Windows（通过Wintun）。
- 当前服务端使用的批读写功能仅支持 Linux（通过系统调用sendmmsg，recvmmsg）。
- 控制消息处理、部分工作池优化仍在迭代中。
- 暂无完善自动化测试。

## ✅ TODO List

- [ ] 完善控制面消息处理（客户端 `controlRecv` 逻辑）。、
- [ ] 实现内网穿透的 P2P 。
- [ ] 添加控制消息的 HTTPS 支持。
- [ ] 添加 UDP 数据加密支持。
- [ ] 将服务端 UDP 收发/转发改造为可配置工作池。
- [ ] 支持 Linux 客户端 TUN/TAP 实现。
- [ ] 完善配置热更新或外部配置加载能力（替代纯嵌入配置）。
- [ ] 增加可观测性指标（连接数、吞吐、丢包、转发延迟）。
- [ ] 补充 CI 流水线（构建、测试、发布制品）。

## 📄 License

MIT
