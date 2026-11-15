#!/usr/bin/env bash
# ==============================================================================
# Skyline MX Backend - Linux 一键安装与部署脚本
# 程序与配置存放目录: /etc/skyline (/etc/skyline/skyline & /etc/skyline/config.yml)
# 安装完成后，在终端直接输入 `skyline` 即可打开交互式管理面板！
# 仓库地址: https://github.com/Grandova/skyline
# ==============================================================================
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

REPO_URL="https://github.com/Grandova/skyline.git"
RELEASE_BASE_URL="https://github.com/Grandova/skyline/releases/latest/download"
CONFIG_DIR="/etc/skyline"
INSTALL_BIN="/etc/skyline/skyline"
SYMLINK_LOCAL="/usr/local/bin/skyline"
SYMLINK_USR="/usr/bin/skyline"
CONFIG_FILE="/etc/skyline/config.yml"

info()  { echo -e "${CYAN}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }

check_root() {
    if [[ "$(id -u)" -ne 0 ]]; then
        err "请使用 root 权限运行此脚本 (例如: sudo bash install.sh)"
        exit 1
    fi
}

detect_arch() {
    local machine
    machine="$(uname -m)"
    case "$machine" in
        x86_64|amd64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            echo "$machine"
            ;;
    esac
}

ensure_dependencies() {
    if ! command -v curl >/dev/null 2>&1; then
        info "正在安装基础依赖 (curl, ca-certificates)..."
        if command -v apt-get >/dev/null 2>&1; then
            apt-get update -y >/dev/null 2>&1 || true
            apt-get install -y curl ca-certificates tar unzip >/dev/null 2>&1 || true
        elif command -v yum >/dev/null 2>&1; then
            yum install -y curl ca-certificates tar unzip >/dev/null 2>&1 || true
        elif command -v dnf >/dev/null 2>&1; then
            dnf install -y curl ca-certificates tar unzip >/dev/null 2>&1 || true
        elif command -v apk >/dev/null 2>&1; then
            apk add --no-cache curl ca-certificates tar unzip >/dev/null 2>&1 || true
        fi
    fi
}

link_binary() {
    chmod +x "$INSTALL_BIN"
    ln -sf "$INSTALL_BIN" "$SYMLINK_LOCAL"
    ln -sf "$INSTALL_BIN" "$SYMLINK_USR" 2>/dev/null || true
}

install_binary() {
    check_root
    ensure_dependencies
    mkdir -p "$CONFIG_DIR"

    local arch
    arch="$(detect_arch)"

    # 1. 如果当前目录已存在编译好的 ./skyline 且非自身目标路径，则优先直接安装
    if [[ -f "./skyline" && -x "./skyline" ]]; then
        local src_real
        src_real="$(realpath ./skyline 2>/dev/null || echo ./skyline)"
        if [[ "$src_real" != "$INSTALL_BIN" ]]; then
            info "检测到本地二进制 ./skyline，正在安装至 $INSTALL_BIN ..."
            cp -f "./skyline" "$INSTALL_BIN"
        fi
        link_binary
        ok "程序已安装至 $INSTALL_BIN (全局命令: skyline)"
        return 0
    fi

    # 2. 优先从 GitHub Release 下载对应架构的预编译静态二进制 (amd64 / arm64)
    if [[ "$arch" == "amd64" || "$arch" == "arm64" ]]; then
        local bin_url="${RELEASE_BASE_URL}/skyline-linux-${arch}"
        info "正在从 GitHub Release 下载预编译二进制 (linux/${arch})..."
        if curl -fsSL --connect-timeout 15 --max-time 120 "$bin_url" -o "${INSTALL_BIN}.tmp"; then
            mv -f "${INSTALL_BIN}.tmp" "$INSTALL_BIN"
            link_binary
            ok "成功从 GitHub Release 下载并安装: $INSTALL_BIN"
            return 0
        else
            rm -f "${INSTALL_BIN}.tmp"
            warn "从 GitHub Release 直接下载未成功，正在尝试备用安装方式..."
        fi
    fi

    # 3. 如果当前在源码目录且安装了 Go，则本地编译
    if [[ -f "./main.go" && -f "./go.mod" ]] && command -v go >/dev/null 2>&1; then
        info "正在从当前源码目录编译 Skyline..."
        CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o "$INSTALL_BIN" .
        link_binary
        ok "编译并安装完成: $INSTALL_BIN"
        return 0
    fi

    # 4. 克隆仓库获取二进制或编译
    TMP_DIR="$(mktemp -d)"
    trap 'rm -rf "$TMP_DIR"' EXIT
    info "正在从 $REPO_URL 拉取仓库..."
    git clone --depth=1 "$REPO_URL" "$TMP_DIR/skyline"
    cd "$TMP_DIR/skyline"

    if [[ -f "./skyline" && "$arch" == "amd64" ]]; then
        cp -f "./skyline" "$INSTALL_BIN"
        link_binary
        ok "已安装预编译二进制到 $INSTALL_BIN"
    elif command -v go >/dev/null 2>&1; then
        info "正在使用 Go 编译 Skyline..."
        CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o "$INSTALL_BIN" .
        link_binary
        ok "编译并安装完成: $INSTALL_BIN"
    else
        err "未能获取适用于 ${arch} 的预编译二进制，且系统未安装 Go。"
        exit 1
    fi
}

# 支持一键下载 Xboard 插件: sudo bash install.sh plugin [/path/to/xboard/plugins]
if [[ "${1:-}" == "plugin" ]]; then
    install_binary
    shift
    exec "$INSTALL_BIN" plugin "${1:-.}"
fi

# 支持带参数一键无人值守安装对接:
# sudo bash install.sh --panel https://... --token ... --node-id 1 [--proxy-protocol v2]
if [[ $# -gt 0 ]]; then
    install_binary
    "$INSTALL_BIN" init "$@" -c "$CONFIG_FILE" -y
    "$INSTALL_BIN" check -c "$CONFIG_FILE" || warn "面板连通性自检未完全通过，请检查上方输出信息"
    "$INSTALL_BIN" install -c "$CONFIG_FILE"
    echo ""
    ok "=================================================================="
    ok "  Skyline 部署完成！程序与配置均存放在 /etc/skyline"
    ok "  后续只需在终端直接输入: skyline  即可进入交互式管理面板！"
    ok "=================================================================="
    exit 0
fi

# 无参数运行 install.sh 时：自动完成程序安装并直接打开内置的 `skyline` 终端管理面板
install_binary
if [[ ! -f "$CONFIG_FILE" ]]; then
    info "首次安装，正在启动配置生成向导 (/etc/skyline/config.yml)..."
    "$INSTALL_BIN" init -c "$CONFIG_FILE" || true
    if [[ -f "$CONFIG_FILE" ]]; then
        "$INSTALL_BIN" install -c "$CONFIG_FILE" || true
    fi
fi

echo ""
ok "安装就绪！以后任何时候在终端直接输入 ${YELLOW}skyline${GREEN} 即可管理服务与配置。"
sleep 1
exec "$INSTALL_BIN" menu -c "$CONFIG_FILE"
