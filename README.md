# Skyline — Xboard 专用高性能 MX 协议 Linux 后端

[![Release](https://img.shields.io/badge/Release-v0.3.1-blue.svg)](https://github.com/Grandova/skyline/releases)
[![Platform](https://img.shields.io/badge/Platform-Linux%20(amd64%20%7C%20arm64)-success.svg)](https://github.com/Grandova/skyline/releases)
[![Protocol](https://img.shields.io/badge/Protocol-MX%20(M--master%20100%25%20Unified)-orange.svg)](https://github.com/Grandova/skyline)

**Skyline** 是专为 **Xboard** 面板（配合 `MxProtocol` 插件）打造的 Linux 原生高性能 **MX (MundoConnect)** 协议后端代理服务。
- **与官方 `M-master` 原版实现 100% 统一**：完整对齐帧格式、经典握手、`MundoConnect-Auth` / `MundoConnect-Response` 0-RTT 快速认证（FastAuth）、定向 UDP 与 PacketAddr 动态 UDP。
- **完整支持 Proxy Protocol v1 & v2 协议头（发送与接收）**：支持出站连接自动发送 v1（文本）或 v2（二进制）协议头透传真实客户端 IP:Port，并支持入站自动解析前端发来的 v1/v2 协议头。
- **统一 `/etc/skyline` 存放目录**：程序本体（`/etc/skyline/skyline`）与配置文件（`/etc/skyline/config.yml`）统一存放在 `/etc/skyline` 目录下。
- **终端输入 `skyline` 即可管理**：内置全功能中文交互式管理面板，安装后在终端直接输入 `skyline` 即可完成配网、修改配置、切换 Proxy Protocol、查看日志、自检诊断、生成 REALITY 密钥与在线更新。

---

## 一、快速开始（Linux 一键安装与管理）

### 1. 一行命令极速安装（自动识别 `amd64` / `arm64`）

无需安装 Go 编译环境，直接从 GitHub Release 拉取预编译静态二进制并安装至 `/etc/skyline`：

```bash
# 方式 A：交互式一键安装（安装后自动进入配置向导与 skyline 管理面板）
curl -fsSL https://raw.githubusercontent.com/Grandova/skyline/main/install.sh | sudo bash

# 方式 B：带参数全自动对接并启动开机自启服务（推荐用于批量部署）
curl -fsSL https://raw.githubusercontent.com/Grandova/skyline/main/install.sh | sudo bash -s -- \
  --panel https://your-xboard-panel.example.com \
  --token YOUR_SERVER_TOKEN \
  --node-id 1
```

> **多节点或开启 Proxy Protocol v2 一键部署示例**：
> ```bash
> curl -fsSL https://raw.githubusercontent.com/Grandova/skyline/main/install.sh | sudo bash -s -- \
>   --panel https://your-xboard-panel.example.com \
>   --token YOUR_SERVER_TOKEN \
>   --node-ids 1,2,3 \
>   --proxy-protocol v2
> ```

---

### 2. 在终端输入 `skyline` 进入交互式管理面板

安装完成后，任何时候在 Linux 终端直接输入 **`skyline`** 回车，即可打开内置管理面板：

```bash
skyline
```

```text
====================================================================
         Skyline MX 协议后端 - 终端交互式管理面板 v0.3.1
====================================================================
  程序路径 : /etc/skyline/skyline (内核: M-core 5.16.1)
  服务状态 : ● 运行中 (active) | 开机自启: 已启用 (enabled)
  配置文件 : /etc/skyline/config.yml
  对接面板 : https://panel.example.com (API: v1 | 节点: #1)
  协议头   : 发送 Proxy Protocol = v2 | 接收 Proxy Protocol = true
--------------------------------------------------------------------
  [服务控制]
    1. 启动服务 (start)              2. 停止服务 (stop)
    3. 重启服务 (restart)            4. 平滑热重载配置 (reload)
    5. 查看运行状态 (status)         6. 实时查看运行日志 (log)

  [配置与协议头管理 (/etc/skyline/config.yml)]
    7. 配置向导: 生成/修改面板与节点对接 (skyline init)
    8. 直接编辑配置文件 (/etc/skyline/config.yml)
    9. 快捷设置 Proxy Protocol v1/v2 协议头 (发送 / 接收 / 转发地址)
   10. 查看当前完整配置文件内容

  [诊断与工具箱]
   11. 节点连通性与配置自检诊断 (skyline check)
   12. 生成 REALITY X25519 密钥对与 ShortID (skyline reality)
   13. 下载 / 部署 Xboard MxProtocol 插件包 (skyline plugin)

  [系统与维护]
   14. 安装 / 重置 systemd 开机自启服务 (skyline install)
   15. 在线更新 Skyline 到最新版本 (skyline update)
   16. 卸载 Skyline 服务与程序 (skyline uninstall)
    0. 退出管理面板
====================================================================
```

### 3. 终端快捷命令速查表

除了直接输入 `skyline` 打开交互式菜单外，您也可以使用以下快捷指令：

| 终端命令 | 功能说明 |
| :--- | :--- |
| **`skyline`** | **打开交互式中文管理面板**（集中管理服务、配置文件、协议头与诊断工具） |
| **`skyline config`** | 直接调用编辑器修改 `/etc/skyline/config.yml`，保存后自动校验语法并提示重启生效 |
| **`skyline pp`** | 交互式或命令行快捷切换 **Proxy Protocol v1 / v2** 发送与接收（如 `skyline pp --send v2 --accept true`） |
| **`skyline check`** | 面板连通性、Token 核验、节点 TLS/REALITY 密钥、Proxy Protocol 状态与端口自检诊断 |
| **`skyline start`** / **`stop`** / **`restart`** | 启动 / 停止 / 重启 `skyline` 系统服务 |
| **`skyline reload`** | 平滑热重载 `/etc/skyline/config.yml` 配置（无需中断进程） |
| **`skyline status`** / **`skyline log`** | 查看服务运行状态 / 实时滚动查看运行日志 (`journalctl -u skyline -f`) |
| **`skyline reality`** | 一键生成 REALITY 所需的 `PrivateKey`、`PublicKey` (X25519) 与 `ShortID` |
| **`skyline plugin [目录]`** | 从 GitHub Release 一键下载并解压 Xboard 的 **`MxProtocol-0.3.1`** 插件包到指定目录 |
| **`skyline update`** | 在线从 GitHub Release 下载最新二进制更新 `/etc/skyline/skyline` 并自动重启服务 |

---

## 二、Xboard 面板安装 `MxProtocol` 插件与对接教程

本仓库的 **[GitHub Releases](https://github.com/Grandova/skyline/releases/latest)** 页面已同步打包发布 Xboard 专用的 **`MxProtocol-0.3.1.zip`** 与 **`MxProtocol-0.3.1.tar.gz`** 插件包。

### 第一步：在 Xboard 面板服务器安装并启用 `MxProtocol` 插件

在您的 **Xboard 面板服务器**上，执行以下任意一种方式将插件解压到 Xboard 的 `plugins/` 目录（假设 Xboard 根目录为 `/www/wwwroot/xboard`）：

```bash
# 方式 1：直接从 GitHub Release 下载解压到 Xboard 的 plugins 目录
cd /www/wwwroot/xboard/plugins
curl -fsSL https://github.com/Grandova/skyline/releases/latest/download/MxProtocol-0.3.1.tar.gz | tar -xz

# 方式 2：如果服务器上已安装 skyline，可直接执行内置命令：
skyline plugin /www/wwwroot/xboard/plugins
```

解压后确认目录结构为 `/www/wwwroot/xboard/plugins/MxProtocol/Plugin.php`，然后：
1. 登录 Xboard 管理员后台 -> **插件管理 (Plugins)**。
2. 找到 **MX 协议支持 (`MxProtocol`)**，点击 **安装** 并 **启用**。

### 第二步：获取 Xboard 通讯密钥 (`token`)

1. 登录 Xboard 管理员后台 -> **系统配置 (System Settings)** -> **节点服务端 (Server / Node)**。
2. 复制 **通讯密钥 (`server_token`)**。

### 第三步：在 Xboard 后台创建 MX 节点

进入 Xboard 后台 -> **节点管理** -> **新建节点**，选择 **`MX`** 协议类型，记录创建后的 **节点 ID (`node_id`)**。

#### 1. 支持的 6 种传输协议 (`network`)

| 传输协议 (`network`) | 适用场景 | 关键配置项 (`network_settings`) |
| :--- | :--- | :--- |
| **`tcp`** | 低延迟直连、中转或配合 TLS / REALITY | `acceptProxyProtocol`: 接收 Proxy Protocol v1/v2<br>`proxyProtocol`: 发送 Proxy Protocol (`"v1"` / `"v2"`) |
| **`ws`** | CDN 中转（如 Cloudflare）或反代 | `path`: WebSocket 路径（如 `/mx-ws`）<br>`host`: 伪装域名 Header<br>`maxEarlyData`: 可选 EarlyData 长度 |
| **`grpc`** | CDN gRPC 中转或多路复用 | `serviceName`: gRPC 服务名（如 `MxGunService`） |
| **`xhttp`** | HTTP 全双工流式传输 / 分段上下行 | `path`: 请求路径（如 `/xhttp`）<br>`host`: 伪装 Host<br>`mode`: 传输模式（如 `auto` / `stream` / `packet`） |
| **`mc1`** | MundoConnect 1 增强流隧道 | `path`: 路径（如 `/mc1`）<br>`cidrSegments`: 可选 CIDR 列表 |
| **`mundordp`** | RDP 远程桌面协议握手伪装 | `username`: RDP 握手 Cookie 用户名（默认 `MundoUser`）<br>`fakeTitle` / `fakeMessage`: 浏览器直接访问时展示的伪装页面标题与内容 |

#### 2. 支持的 3 种安全模式 (`tls`)

- **模式 `0`：无 TLS (Plaintext)** — 适用于内网中转、前置反代卸载 TLS 或明文传输。
- **模式 `1`：标准 TLS** — 可在 `/etc/skyline/config.yml` 配置 `cert_file` / `key_file`；若未指定本地证书且面板未下发 PEM，Skyline 会自动按 `server_name` 在内存中生成 ECDSA P-256 自签证书。
- **模式 `2`：REALITY** — 在终端运行 **`skyline reality`** 一键生成 `PrivateKey`、`PublicKey` 与 `ShortID`，填入 Xboard 节点的 `tls_settings` 即可。

---

## 三、`/etc/skyline` 目录规范与 Proxy Protocol v1 / v2 配置

### 1. `/etc/skyline` 统一目录结构

```text
/etc/skyline/
├── skyline        # Skyline Linux 静态可执行程序本体 (软链接: /usr/local/bin/skyline & /usr/bin/skyline)
├── config.yml     # 核心配置文件（可通过 `skyline config` 或菜单第 8/9 项随时修改）
└── certs/         # (可选) 存放节点自定义 TLS 证书与私钥
```

### 2. Proxy Protocol v1 & v2 协议头配置详解 (`/etc/skyline/config.yml`)

您可以在终端运行 **`skyline`** 选择菜单 **`9. 快捷设置 Proxy Protocol v1/v2 协议头`**，或直接修改 `/etc/skyline/config.yml`：

```yaml
log_level: info
panel_url: "https://your-xboard-panel.example.com"
token: "your-xboard-server-token"
node_id: 1
node_type: mx
api_version: v1          # 可选: v1 (/api/v1/server/UniProxy/*), v2 (/api/v2/server/*), auto
listen_ip: "0.0.0.0"
pull_interval: 60s
push_interval: 60s
http_timeout: 15s
device_ttl: 90s

# =============================================================================
# Proxy Protocol v1 & v2 协议头配置（支持发送与接收）
# =============================================================================
# 1. 发送 Proxy Protocol 协议头 (proxy_protocol):
#    开启后，节点在建立出站/转发 TCP 连接时会自动在首包发送 Proxy Protocol 协议头，
#    将客户端的真实源 IP:Port 和目标 IP:Port 传递给下游服务器或落地机。
#    可选值:
#      0 或 off : 关闭发送（默认）
#      1 或 v1  : 发送 Proxy Protocol v1 文本协议头 (PROXY TCP4/TCP6 ...)
#      2 或 v2  : 发送 Proxy Protocol v2 二进制协议头
proxy_protocol: v2

# 2. 接收并解析入站 Proxy Protocol 协议头 (accept_proxy_protocol):
#    开启后，入站监听器自动识别并解析前置 Nginx/HAProxy/中转机发来的 Proxy Protocol v1 或 v2 协议头，
#    还原客户端真实 IP 用于在线设备限制统计与下游协议头透传。
accept_proxy_protocol: true

# 3. 可选：固定下游转发地址 (forward_address)
#    若配置了 forward_address，节点会将出站连接统一转发至该地址并携带 Proxy Protocol v1/v2 协议头。
# forward_address: "127.0.0.1:8443"
```

> **多节点独立配置示例**：若在 `nodes:` 列表中同时托管多个节点，可为每个节点单独设置 `proxy_protocol: v1` 或 `proxy_protocol: v2`：
> ```yaml
> nodes:
>   - node_id: 1
>     node_type: mx
>     proxy_protocol: v1
>     accept_proxy_protocol: true
>   - node_id: 2
>     node_type: mx
>     proxy_protocol: v2
>     accept_proxy_protocol: true
> ```

---

## 四、从源码编译与测试（Linux）

```bash
git clone https://github.com/Grandova/skyline.git
cd skyline

# 编译 Linux amd64 与 arm64 双架构静态二进制
make build-all

# 运行完整单元测试与全协议 + Proxy Protocol v1/v2 端到端集成测试
make test
```
