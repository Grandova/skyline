#!/usr/bin/env bash
# ==============================================================================
# Skyline MX Backend - Linux 一键部署与运维管理脚本
# 程序与配置存放目录: /etc/skyline (/etc/skyline/skyline & /etc/skyline/config.yml)
# 仓库地址: https://github.com/Grandova/skyline
# ==============================================================================
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

REPO_URL="https://github.com/Grandova/skyline.git"
CONFIG_DIR="/etc/skyline"
INSTALL_BIN="/etc/skyline/skyline"
SYMLINK_BIN="/usr/local/bin/skyline"
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

link_binary() {
    chmod +x "$INSTALL_BIN"
    ln -sf "$INSTALL_BIN" "$SYMLINK_BIN"
}

install_binary() {
    check_root
    mkdir -p "$CONFIG_DIR"

    if [[ -f "./skyline" && -x "./skyline" ]]; then
        info "检测到当前目录已存在 Linux 二进制文件 ./skyline，正在安装到 $CONFIG_DIR..."
        if [[ "$(realpath ./skyline)" != "$INSTALL_BIN" ]]; then
            cp -f "./skyline" "$INSTALL_BIN"
        fi
        link_binary
        ok "程序已安装到 $INSTALL_BIN (软链接: $SYMLINK_BIN)"
        return 0
    fi

    if [[ -f "./main.go" && -f "./go.mod" ]]; then
        if command -v go >/dev/null 2>&1; then
            info "正在从当前源码目录编译 Skyline (Linux 静态二进制)..."
            CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o "$INSTALL_BIN" .
            link_binary
            ok "编译并安装完成: $INSTALL_BIN"
            return 0
        fi
    fi

    if ! command -v go >/dev/null 2>&1; then
        warn "未检测到 Go 编译环境，正在尝试拉取仓库预编译版本..."
    fi

    TMP_DIR="$(mktemp -d)"
    trap 'rm -rf "$TMP_DIR"' EXIT
    info "正在从 $REPO_URL 克隆源码..."
    git clone --depth=1 "$REPO_URL" "$TMP_DIR/skyline"
    cd "$TMP_DIR/skyline"

    if [[ -f "./skyline" ]]; then
        cp -f "./skyline" "$INSTALL_BIN"
        link_binary
        ok "已安装预编译二进制到 $INSTALL_BIN"
    elif command -v go >/dev/null 2>&1; then
        info "正在使用 Go 编译 Skyline..."
        CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o "$INSTALL_BIN" .
        link_binary
        ok "编译并安装完成: $INSTALL_BIN"
    else
        err "未找到预编译二进制且系统未安装 Go (1.22+)，请先安装 Go 或在仓库目录执行构建。"
        exit 1
    fi
}

# 如果传入了命令行参数（例如 --panel ... --token ...），则直接执行非交互式快速部署
if [[ $# -gt 0 ]]; then
    install_binary
    "$INSTALL_BIN" init "$@" -c "$CONFIG_FILE" -y
    "$INSTALL_BIN" check -c "$CONFIG_FILE" || warn "面板连通性自检未完全通过，请检查上方输出信息"
    "$INSTALL_BIN" service install -c "$CONFIG_FILE"
    exit 0
fi

show_menu() {
    clear || true
    echo -e "${CYAN}============================================================${NC}"
    echo -e "${CYAN}        Skyline MX 协议后端 - Linux 运维管理面板           ${NC}"
    echo -e "${CYAN}============================================================${NC}"
    if [[ -x "$INSTALL_BIN" ]]; then
        echo -e "  当前版本: $( "$INSTALL_BIN" version )"
    else
        echo -e "  当前状态: ${YELLOW}尚未安装${NC}"
    fi
    if systemctl is-active --quiet skyline 2>/dev/null; then
        echo -e "  运行状态: ${GREEN}运行中 (active)${NC}"
    else
        echo -e "  运行状态: ${YELLOW}未运行 (inactive)${NC}"
    fi
    echo -e "  安装目录: ${CONFIG_DIR} (程序: ${INSTALL_BIN} | 配置: ${CONFIG_FILE})"
    echo -e "${CYAN}------------------------------------------------------------${NC}"
    echo -e "  ${GREEN}1.${NC} 安装 / 更新 Skyline 后端至 /etc/skyline 并配置开机自启"
    echo -e "  ${GREEN}2.${NC} 交互式生成 / 修改节点对接配置 (skyline init)"
    echo -e "  ${GREEN}3.${NC} 直接编辑配置文件 (/etc/skyline/config.yml)"
    echo -e "  ${GREEN}4.${NC} 检测 Xboard 面板连通性与节点配置诊断 (skyline check)"
    echo -e "  ${GREEN}5.${NC} 启动 / 重启 Skyline 服务"
    echo -e "  ${GREEN}6.${NC} 停止 Skyline 服务"
    echo -e "  ${GREEN}7.${NC} 查看服务状态与实时日志"
    echo -e "  ${GREEN}8.${NC} 卸载 Skyline 服务"
    echo -e "  ${GREEN}0.${NC} 退出管理面板"
    echo -e "${CYAN}============================================================${NC}"
    read -rp "请输入操作编号 [0-8]: " choice
    case "$choice" in
        1)
            install_binary
            if [[ ! -f "$CONFIG_FILE" ]]; then
                "$INSTALL_BIN" init -c "$CONFIG_FILE"
            fi
            "$INSTALL_BIN" service install -c "$CONFIG_FILE"
            ;;
        2)
            check_root
            "$INSTALL_BIN" init -c "$CONFIG_FILE"
            read -rp "是否立即重启服务以应用新配置？(Y/n): " yn
            if [[ "${yn:-Y}" =~ ^[Yy]$ ]]; then
                "$INSTALL_BIN" service restart
            fi
            ;;
        3)
            check_root
            ${EDITOR:-vi} "$CONFIG_FILE"
            read -rp "配置文件已保存，是否立即重启/热重载服务？(Y/n): " yn
            if [[ "${yn:-Y}" =~ ^[Yy]$ ]]; then
                "$INSTALL_BIN" service restart
            fi
            ;;
        4)
            "$INSTALL_BIN" check -c "$CONFIG_FILE"
            ;;
        5)
            check_root
            "$INSTALL_BIN" service restart
            "$INSTALL_BIN" service status
            ;;
        6)
            check_root
            "$INSTALL_BIN" service stop
            ;;
        7)
            "$INSTALL_BIN" service log
            ;;
        8)
            check_root
            "$INSTALL_BIN" service uninstall
            rm -f "$INSTALL_BIN" "$SYMLINK_BIN"
            ok "已卸载 Skyline 服务（配置文件保留于 $CONFIG_FILE）"
            ;;
        0)
            exit 0
            ;;
        *)
            warn "无效的选项: $choice"
            ;;
    esac
}

show_menu
