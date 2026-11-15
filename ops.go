package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"

	core "github.com/v2fly/v2ray-core/v5"

	"skyline/config"
	"skyline/panel"
	"skyline/transportext"
)

const (
	githubRepo       = "Grandova/skyline"
	githubReleaseURL = "https://github.com/Grandova/skyline/releases/latest/download"
	defaultDir       = "/etc/skyline"
	defaultBinPath   = "/etc/skyline/skyline"
	defaultCfgPath   = "/etc/skyline/config.yml"
	symlinkBinPath   = "/usr/local/bin/skyline"
	usrBinPath       = "/usr/bin/skyline"
	systemdUnitPath  = "/etc/systemd/system/skyline.service"

	ansiReset  = "\033[0m"
	ansiRed    = "\033[0;31m"
	ansiGreen  = "\033[0;32m"
	ansiYellow = "\033[1;33m"
	ansiCyan   = "\033[0;36m"
	ansiBold   = "\033[1m"
)

func parseNodeIDList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	})
	var ids []int
	seen := make(map[int]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.Atoi(p)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("无效的节点 ID %q（必须为正整数）", p)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func promptLine(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// ============================================================================
// Interactive Terminal Management Panel (`skyline` / `skyline menu`)
// ============================================================================

func runManageMenu(cfgPath string) error {
	if cfgPath == "" {
		cfgPath = resolveDefaultConfigPath("")
	}
	reader := bufio.NewReader(os.Stdin)

	for {
		printDashboardHeader(cfgPath)
		fmt.Printf("  %s[服务控制]%s\n", ansiBold, ansiReset)
		fmt.Printf("    %s1.%s 启动服务 (start)              %s2.%s 停止服务 (stop)\n", ansiGreen, ansiReset, ansiGreen, ansiReset)
		fmt.Printf("    %s3.%s 重启服务 (restart)            %s4.%s 平滑热重载配置 (reload)\n", ansiGreen, ansiReset, ansiGreen, ansiReset)
		fmt.Printf("    %s5.%s 查看运行状态 (status)         %s6.%s 实时查看运行日志 (log)\n\n", ansiGreen, ansiReset, ansiGreen, ansiReset)

		fmt.Printf("  %s[配置与协议头管理 (/etc/skyline/config.yml)]%s\n", ansiBold, ansiReset)
		fmt.Printf("    %s7.%s 配置向导: 生成/修改面板与节点对接 (skyline init)\n", ansiGreen, ansiReset)
		fmt.Printf("    %s8.%s 直接编辑配置文件 (%s)\n", ansiGreen, ansiReset, cfgPath)
		fmt.Printf("    %s9.%s 快捷设置 Proxy Protocol v1/v2 协议头 (发送 / 接收 / 转发地址)\n", ansiGreen, ansiReset)
		fmt.Printf("   %s10.%s 查看当前完整配置文件内容\n\n", ansiGreen, ansiReset)

		fmt.Printf("  %s[诊断与工具箱]%s\n", ansiBold, ansiReset)
		fmt.Printf("   %s11.%s 节点连通性与配置自检诊断 (skyline check)\n", ansiGreen, ansiReset)
		fmt.Printf("   %s12.%s 生成 REALITY X25519 密钥对与 ShortID (skyline reality)\n", ansiGreen, ansiReset)
		fmt.Printf("   %s13.%s 下载 / 部署 Xboard MxProtocol 插件包 (skyline plugin)\n\n", ansiGreen, ansiReset)

		fmt.Printf("  %s[系统与维护]%s\n", ansiBold, ansiReset)
		fmt.Printf("   %s14.%s 安装 / 重置 systemd 开机自启服务 (skyline install)\n", ansiGreen, ansiReset)
		fmt.Printf("   %s15.%s 在线更新 Skyline 到最新版本 (skyline update)\n", ansiGreen, ansiReset)
		fmt.Printf("   %s16.%s 卸载 Skyline 服务与程序 (skyline uninstall)\n", ansiGreen, ansiReset)
		fmt.Printf("    %s0.%s 退出管理面板\n", ansiGreen, ansiReset)
		fmt.Printf("%s====================================================================%s\n", ansiCyan, ansiReset)
		fmt.Print("请输入操作编号 [0-16]: ")

		choice, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return nil
		}
		choice = strings.TrimSpace(choice)
		fmt.Println()

		switch choice {
		case "1":
			_ = runServiceCommand("start", cfgPath)
		case "2":
			_ = runServiceCommand("stop", cfgPath)
		case "3":
			_ = runServiceCommand("restart", cfgPath)
		case "4":
			_ = runServiceCommand("reload", cfgPath)
		case "5":
			_ = runServiceCommand("status", cfgPath)
		case "6":
			fmt.Println("正在打开实时日志 (按 Ctrl+C 退出日志查看)...")
			_ = runServiceCommand("log", cfgPath)
		case "7":
			if err := runInitCommand(cfgPath, "", "", "", 0, 0, "", "", "", false, "", "", false); err != nil {
				fmt.Printf("%s[ERROR]%s %v\n", ansiRed, ansiReset, err)
			} else if isServiceActive() {
				ans := promptLine(reader, "是否立即重启 Skyline 服务以应用新配置？(Y/n)", "Y")
				if strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
					_ = runServiceCommand("restart", cfgPath)
				}
			}
		case "8":
			if err := runEditConfigCommand(cfgPath); err != nil {
				fmt.Printf("%s[ERROR]%s %v\n", ansiRed, ansiReset, err)
			}
		case "9":
			if err := runInteractiveProxyProtocolSetup(reader, cfgPath); err != nil {
				fmt.Printf("%s[ERROR]%s %v\n", ansiRed, ansiReset, err)
			}
		case "10":
			raw, rErr := os.ReadFile(cfgPath)
			if rErr != nil {
				fmt.Printf("%s[WARN]%s 读取配置文件 %s 失败: %v\n", ansiYellow, ansiReset, cfgPath, rErr)
			} else {
				fmt.Printf("--- %s ---\n%s\n-------------------\n", cfgPath, string(raw))
			}
		case "11":
			cfg, cErr := config.Load(cfgPath)
			if cErr != nil {
				fmt.Printf("%s[ERROR]%s 加载配置文件 %s 失败: %v\n", ansiRed, ansiReset, cfgPath, cErr)
			} else if vErr := cfg.Validate(); vErr != nil {
				fmt.Printf("%s[ERROR]%s 配置校验失败: %v\n", ansiRed, ansiReset, vErr)
			} else {
				_ = runCheckCommand(cfg)
			}
		case "12":
			_ = runRealityKeygenCommand()
		case "13":
			targetDir := promptLine(reader, "请输入 Xboard 插件解压目录 (例如 /www/wwwroot/xboard/plugins 或留空解压到当前目录)", ".")
			if err := runPluginDownloadCommand(targetDir); err != nil {
				fmt.Printf("%s[ERROR]%s %v\n", ansiRed, ansiReset, err)
			}
		case "14":
			if err := runServiceCommand("install", cfgPath); err != nil {
				fmt.Printf("%s[ERROR]%s %v\n", ansiRed, ansiReset, err)
			}
		case "15":
			if err := runSelfUpdateCommand(); err != nil {
				fmt.Printf("%s[ERROR]%s %v\n", ansiRed, ansiReset, err)
			}
		case "16":
			confirm := promptLine(reader, "确认要卸载 Skyline 服务吗？(y/N)", "N")
			if strings.EqualFold(confirm, "y") || strings.EqualFold(confirm, "yes") {
				_ = runServiceCommand("uninstall", cfgPath)
			}
		case "0", "q", "quit", "exit":
			return nil
		default:
			fmt.Printf("%s[WARN]%s 无效的选项 %q，请输入 0-16 之间的数字。\n", ansiYellow, ansiReset, choice)
		}

		fmt.Printf("\n%s按回车键返回 Skyline 管理主菜单...%s", ansiCyan, ansiReset)
		_, _ = reader.ReadString('\n')
	}
}

func printDashboardHeader(cfgPath string) {
	fmt.Print("\033[H\033[2J")
	fmt.Printf("%s====================================================================%s\n", ansiCyan, ansiReset)
	fmt.Printf("%s         Skyline MX 协议后端 - 终端交互式管理面板 v%s           %s\n", ansiCyan, Version, ansiReset)
	fmt.Printf("%s====================================================================%s\n", ansiCyan, ansiReset)

	binStatus := fmt.Sprintf("%s未安装%s", ansiYellow, ansiReset)
	if st, err := os.Stat(defaultBinPath); err == nil && !st.IsDir() {
		binStatus = fmt.Sprintf("%s%s%s", ansiGreen, defaultBinPath, ansiReset)
	} else if exe, err := os.Executable(); err == nil {
		binStatus = exe
	}

	svcStatus := fmt.Sprintf("%s● 未运行 (inactive)%s", ansiYellow, ansiReset)
	if isServiceActive() {
		svcStatus = fmt.Sprintf("%s● 运行中 (active)%s", ansiGreen, ansiReset)
	}
	autoStatus := fmt.Sprintf("%s未启用%s", ansiYellow, ansiReset)
	if isServiceEnabled() {
		autoStatus = fmt.Sprintf("%s已启用 (enabled)%s", ansiGreen, ansiReset)
	}

	fmt.Printf("  程序路径 : %s (内核: M-core %s)\n", binStatus, core.Version())
	fmt.Printf("  服务状态 : %s | 开机自启: %s\n", svcStatus, autoStatus)

	if cfg, err := config.Load(cfgPath); err == nil {
		var nodeDesc string
		if cfg.MachineID > 0 && len(cfg.Nodes) == 0 {
			nodeDesc = fmt.Sprintf("机器发现模式 (Machine #%d)", cfg.MachineID)
		} else if len(cfg.Nodes) > 0 {
			ids := make([]string, 0, len(cfg.Nodes))
			for _, n := range cfg.Nodes {
				ids = append(ids, fmt.Sprintf("#%d", n.NodeID))
			}
			nodeDesc = strings.Join(ids, ", ")
		} else {
			nodeDesc = "未配置节点"
		}
		acceptPP := false
		if cfg.AcceptProxyProtocol != nil && *cfg.AcceptProxyProtocol {
			acceptPP = true
		}
		ppSend := transportext.FormatProxyProtocolVersion(cfg.ProxyProtocol.Int())
		fmt.Printf("  配置文件 : %s%s%s\n", ansiGreen, cfgPath, ansiReset)
		fmt.Printf("  对接面板 : %s (API: %s | 节点: %s)\n", cfg.PanelURL, cfg.APIVersion, nodeDesc)
		fmt.Printf("  协议头   : 发送 Proxy Protocol = %s%s%s | 接收 Proxy Protocol = %s%v%s\n",
			ansiCyan, ppSend, ansiReset, ansiCyan, acceptPP, ansiReset)
	} else {
		fmt.Printf("  配置文件 : %s%s (尚未生成，请按 7 运行配置向导)%s\n", ansiYellow, cfgPath, ansiReset)
	}
	fmt.Printf("%s--------------------------------------------------------------------%s\n", ansiCyan, ansiReset)
}

func isServiceActive() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	err := exec.Command("systemctl", "is-active", "--quiet", "skyline").Run()
	return err == nil
}

func isServiceEnabled() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	err := exec.Command("systemctl", "is-enabled", "--quiet", "skyline").Run()
	return err == nil
}

// ============================================================================
// Direct Config Editing & Quick Proxy Protocol v1/v2 Switcher
// ============================================================================

func runEditConfigCommand(cfgPath string) error {
	if cfgPath == "" {
		cfgPath = defaultCfgPath
	}
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Printf("[INFO] 配置文件 %s 尚不存在，正在启动初始化向导...\n", cfgPath)
		return runInitCommand(cfgPath, "", "", "", 0, 0, "", "", "", false, "", "", false)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		for _, candidate := range []string{"nano", "vim", "vi"} {
			if _, err := exec.LookPath(candidate); err == nil {
				editor = candidate
				break
			}
		}
	}
	if editor == "" {
		return fmt.Errorf("未找到可用的文本编辑器 (nano / vim / vi)，请直接修改 %s", cfgPath)
	}

	cmd := exec.Command(editor, cfgPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("编辑器退出异常: %w", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("配置文件语法校验失败，请检查格式: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置文件内容校验未通过: %w", err)
	}
	fmt.Printf("%s[OK]%s 配置文件 %s 语法与参数校验通过！\n", ansiGreen, ansiReset, cfgPath)

	if isServiceActive() {
		reader := bufio.NewReader(os.Stdin)
		ans := promptLine(reader, "是否立即热重载/重启 Skyline 服务以应用修改？(Y/n)", "Y")
		if strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
			return runServiceCommand("restart", cfgPath)
		}
	}
	return nil
}

func runProxyProtocolCLI(args []string) error {
	fs := flag.NewFlagSet("pp", flag.ContinueOnError)
	var (
		cfgPath     string
		sendVerStr  string
		acceptStr   string
		forwardAddr string
	)
	fs.StringVar(&cfgPath, "c", "", "配置文件路径")
	fs.StringVar(&cfgPath, "config", "", "配置文件路径")
	fs.StringVar(&sendVerStr, "send", "", "发送 Proxy Protocol 版本: 0/off, 1/v1, 2/v2")
	fs.StringVar(&acceptStr, "accept", "", "接收并解析入站 Proxy Protocol v1/v2: true 或 false")
	fs.StringVar(&forwardAddr, "forward", "", "固定出站转发目标地址 (如 127.0.0.1:8443)")
	_ = fs.Parse(args)

	cfgPath = resolveDefaultConfigPath(cfgPath)
	if sendVerStr == "" && acceptStr == "" && forwardAddr == "" {
		return runInteractiveProxyProtocolSetup(bufio.NewReader(os.Stdin), cfgPath)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置文件 %s 失败: %w", cfgPath, err)
	}
	sendVer := cfg.ProxyProtocol
	if sendVerStr != "" {
		v, pErr := config.ParseProxyProtocol(sendVerStr)
		if pErr != nil {
			return pErr
		}
		sendVer = v
	}
	acceptPP := false
	if cfg.AcceptProxyProtocol != nil {
		acceptPP = *cfg.AcceptProxyProtocol
	}
	if acceptStr != "" {
		acceptPP = strings.EqualFold(acceptStr, "true") || acceptStr == "1" || strings.EqualFold(acceptStr, "yes") || strings.EqualFold(acceptStr, "on")
	}
	fw := cfg.ForwardAddress
	if forwardAddr != "" {
		if forwardAddr == "off" || forwardAddr == "none" {
			fw = ""
		} else {
			fw = forwardAddr
		}
	}

	if err := applyProxyProtocolToConfigFile(cfgPath, sendVer, acceptPP, fw); err != nil {
		return err
	}
	fmt.Printf("%s[OK]%s 已更新 %s: proxy_protocol=%s, accept_proxy_protocol=%v, forward_address=%q\n",
		ansiGreen, ansiReset, cfgPath, transportext.FormatProxyProtocolVersion(sendVer.Int()), acceptPP, fw)
	if isServiceActive() {
		_ = runServiceCommand("restart", cfgPath)
	}
	return nil
}

func runInteractiveProxyProtocolSetup(reader *bufio.Reader, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("读取配置文件 %s 失败（请先执行配置向导生成配置）: %w", cfgPath, err)
	}

	curSend := transportext.FormatProxyProtocolVersion(cfg.ProxyProtocol.Int())
	curAccept := "N"
	if cfg.AcceptProxyProtocol != nil && *cfg.AcceptProxyProtocol {
		curAccept = "Y"
	}

	fmt.Println("--------------------------------------------------------")
	fmt.Println("  快捷配置 Proxy Protocol v1 / v2 协议头")
	fmt.Println("--------------------------------------------------------")
	fmt.Println("  说明:")
	fmt.Println("    - 发送协议头 (proxy_protocol): 节点向后端/落地转发连接时在首包携带真实客户端 IP:Port")
	fmt.Println("      可选: 0 (关闭) | v1 (发送 Proxy Protocol v1 文本头) | v2 (发送 Proxy Protocol v2 二进制头)")
	fmt.Println("    - 接收协议头 (accept_proxy_protocol): 节点入站自动识别并解析前置中转发来的 v1/v2 协议头")
	fmt.Println("--------------------------------------------------------")

	sendInput := promptLine(reader, "1. 请选择出站发送 Proxy Protocol 版本 (0=关闭, v1=版本1, v2=版本2)", curSend)
	sendVer, err := config.ParseProxyProtocol(sendInput)
	if err != nil {
		return err
	}

	accInput := promptLine(reader, "2. 是否开启入站接收并解析 Proxy Protocol v1/v2 协议头？(y/N)", curAccept)
	acceptPP := strings.EqualFold(accInput, "y") || strings.EqualFold(accInput, "yes") || accInput == "1" || strings.EqualFold(accInput, "true")

	fwInput := promptLine(reader, "3. 可选：固定出站转发下游地址 (如 127.0.0.1:8443，留空或输入 none 表示直连原目标)", cfg.ForwardAddress)
	if strings.EqualFold(fwInput, "none") || strings.EqualFold(fwInput, "off") {
		fwInput = ""
	}

	if err := applyProxyProtocolToConfigFile(cfgPath, sendVer, acceptPP, fwInput); err != nil {
		return err
	}
	fmt.Printf("\n%s[OK]%s 已保存 Proxy Protocol 设置至 %s:\n", ansiGreen, ansiReset, cfgPath)
	fmt.Printf("     发送协议头 (proxy_protocol)        : %s\n", transportext.FormatProxyProtocolVersion(sendVer.Int()))
	fmt.Printf("     接收协议头 (accept_proxy_protocol) : %v\n", acceptPP)
	if fwInput != "" {
		fmt.Printf("     固定转发地址 (forward_address)     : %s\n", fwInput)
	}

	if isServiceActive() {
		ans := promptLine(reader, "是否立即重启 Skyline 服务使协议头配置生效？(Y/n)", "Y")
		if strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
			return runServiceCommand("restart", cfgPath)
		}
	}
	return nil
}

var (
	reProxyProto       = regexp.MustCompile(`(?m)^(\s*)proxy_protocol\s*:.*$`)
	reAcceptProxyProto = regexp.MustCompile(`(?m)^(\s*)accept_proxy_protocol\s*:.*$`)
	reForwardAddress   = regexp.MustCompile(`(?m)^(\s*)#?\s*forward_address\s*:.*$`)
)

func applyProxyProtocolToConfigFile(cfgPath string, sendVer config.ProxyProtocolVersion, acceptPP bool, forwardAddr string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	content := string(raw)

	ppVal := "0"
	if sendVer > 0 {
		ppVal = fmt.Sprintf("v%d", sendVer.Int())
	}

	if reProxyProto.MatchString(content) {
		content = reProxyProto.ReplaceAllString(content, fmt.Sprintf("${1}proxy_protocol: %s", ppVal))
	} else {
		content += fmt.Sprintf("\nproxy_protocol: %s\n", ppVal)
	}

	if reAcceptProxyProto.MatchString(content) {
		content = reAcceptProxyProto.ReplaceAllString(content, fmt.Sprintf("${1}accept_proxy_protocol: %v", acceptPP))
	} else {
		content += fmt.Sprintf("accept_proxy_protocol: %v\n", acceptPP)
	}

	forwardAddr = strings.TrimSpace(forwardAddr)
	if forwardAddr != "" {
		if reForwardAddress.MatchString(content) {
			content = reForwardAddress.ReplaceAllString(content, fmt.Sprintf("${1}forward_address: %q", forwardAddr))
		} else {
			content += fmt.Sprintf("forward_address: %q\n", forwardAddr)
		}
	} else if reForwardAddress.MatchString(content) {
		content = reForwardAddress.ReplaceAllString(content, `${1}# forward_address: "127.0.0.1:8443"`)
	}

	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		return err
	}
	_, err = config.Load(cfgPath)
	return err
}

// ============================================================================
// REALITY X25519 Keypair & ShortID Generator (`skyline reality`)
// ============================================================================

func runRealityKeygenCommand() error {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return fmt.Errorf("生成随机数失败: %w", err)
	}
	// Clamp X25519 private scalar per RFC 7748
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return fmt.Errorf("计算 X25519 公钥失败: %w", err)
	}

	shortBytes := make([]byte, 4)
	_, _ = rand.Read(shortBytes)

	privB64 := base64.RawURLEncoding.EncodeToString(priv)
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	shortID := hex.EncodeToString(shortBytes)

	fmt.Println("========================================================")
	fmt.Println("  Skyline REALITY (X25519) 密钥对与 ShortID 生成结果")
	fmt.Println("========================================================")
	fmt.Printf("  PrivateKey (服务端私钥) : %s%s%s\n", ansiGreen, privB64, ansiReset)
	fmt.Printf("  PublicKey  (客户端公钥) : %s%s%s\n", ansiCyan, pubB64, ansiReset)
	fmt.Printf("  ShortID    (简短标识)   : %s%s%s\n", ansiYellow, shortID, ansiReset)
	fmt.Println("--------------------------------------------------------")
	fmt.Println("  提示: 请将以上参数填入 Xboard 后台 MX 节点的 REALITY (tls=2) 配置中。")
	return nil
}

// ============================================================================
// Xboard MxProtocol Plugin Downloader (`skyline plugin`)
// ============================================================================

func runPluginDownloadCommand(targetDir string) error {
	if strings.TrimSpace(targetDir) == "" {
		targetDir = "."
	}
	pluginURL := githubReleaseURL + "/MxProtocol-0.3.1.zip"
	fmt.Printf("[*] 正在从 GitHub Release 下载 Xboard 插件包: %s ...\n", pluginURL)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(pluginURL)
	if err != nil {
		return fmt.Errorf("下载插件包失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载插件包返回 HTTP %d (请访问 https://github.com/%s/releases 手动下载)", resp.StatusCode, githubRepo)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取插件包数据失败: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("解析 ZIP 插件包失败: %w", err)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("创建目标目录 %s 失败: %w", targetDir, err)
	}

	for _, f := range zr.File {
		destPath := filepath.Join(targetDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(targetDir)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(destPath, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, _ = io.Copy(outFile, rc)
		_ = outFile.Close()
		_ = rc.Close()
	}

	fmt.Printf("%s[OK]%s Xboard MxProtocol 插件已成功解压至: %s\n", ansiGreen, ansiReset, filepath.Join(targetDir, "MxProtocol"))
	fmt.Println("     请将该 MxProtocol 文件夹放入 Xboard 源码的 plugins/ 目录下并在后台「插件管理」中启用。")
	return nil
}

// ============================================================================
// Online Self-Update (`skyline update`)
// ============================================================================

func runSelfUpdateCommand() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("在线更新命令仅支持 Linux 系统")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("更新程序需要 root 权限，请使用 sudo skyline update")
	}

	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("当前架构 %s 暂不支持预编译包自动更新，请从源码编译", arch)
	}

	downloadURL := fmt.Sprintf("%s/skyline-linux-%s", githubReleaseURL, arch)
	fmt.Printf("[*] 正在从 GitHub Release 下载最新 Linux (%s) 版本: %s ...\n", arch, downloadURL)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(defaultDir, 0755); err != nil {
		return err
	}
	tmpPath := defaultBinPath + ".new"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	_ = out.Close()

	if err := os.Rename(tmpPath, defaultBinPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	ensureGlobalSymlinks(defaultBinPath)

	fmt.Printf("%s[OK]%s 程序已更新至 %s\n", ansiGreen, ansiReset, defaultBinPath)
	if isServiceActive() {
		fmt.Println("[*] 正在重启 Skyline 服务以应用新版本...")
		return runServiceCommand("restart", defaultCfgPath)
	}
	return nil
}

// ============================================================================
// Config Generator Wizard (`skyline init`)
// ============================================================================

func runInitCommand(
	cfgPath, panelURL, token, nodeIDsStr string,
	singleNodeID, machineID int,
	apiVersion, listenIP, proxyProtocol string,
	acceptProxyProtocol bool,
	forwardAddress, logLevel string,
	nonInteractive bool,
) error {
	if cfgPath == "" {
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			cfgPath = defaultCfgPath
		} else {
			cfgPath = "config.yml"
		}
	}

	var nodeIDs []int
	if singleNodeID > 0 {
		nodeIDs = append(nodeIDs, singleNodeID)
	}
	if parsed, err := parseNodeIDList(nodeIDsStr); err == nil && len(parsed) > 0 {
		nodeIDs = parsed
	}

	needInteractive := !nonInteractive && (panelURL == "" || token == "" || (len(nodeIDs) == 0 && machineID <= 0))
	reader := bufio.NewReader(os.Stdin)

	if needInteractive {
		fmt.Println("========================================================")
		fmt.Printf("  Skyline MX Backend v%s - 交互式配置生成向导\n", Version)
		fmt.Println("========================================================")
		for strings.TrimSpace(panelURL) == "" {
			panelURL = promptLine(reader, "1. 请输入 Xboard 面板地址 (例如 https://panel.example.com)", "")
		}
		for strings.TrimSpace(token) == "" {
			token = promptLine(reader, "2. 请输入 Xboard 通讯密钥 (server_token / machine_token)", "")
		}
		if apiVersion == "" {
			mode := promptLine(reader, "3. 请选择对接模式 (1=V1 节点对接[推荐], 2=V2 节点对接, 3=V2 机器ID自动发现)", "1")
			switch mode {
			case "2":
				apiVersion = "v2"
			case "3":
				apiVersion = "v2"
				for machineID <= 0 {
					mStr := promptLine(reader, "4. 请输入 Xboard 机器 ID (machine_id)", "1")
					machineID, _ = strconv.Atoi(mStr)
				}
			default:
				apiVersion = "v1"
			}
		}
		if machineID <= 0 && len(nodeIDs) == 0 {
			for len(nodeIDs) == 0 {
				rawIDs := promptLine(reader, "4. 请输入 Xboard MX 节点 ID (支持单个如 1，或多个用逗号分隔如 1,2,3)", "1")
				parsed, err := parseNodeIDList(rawIDs)
				if err != nil {
					fmt.Printf("   [!] %v\n", err)
					continue
				}
				nodeIDs = parsed
			}
		}
		if listenIP == "" {
			listenIP = promptLine(reader, "5. 请输入监听 IP 地址", "0.0.0.0")
		}
		if proxyProtocol == "" {
			proxyProtocol = promptLine(reader, "6. 是否开启发送 Proxy Protocol 协议头 (0=关闭, v1=Proxy Protocol v1, v2=Proxy Protocol v2)", "0")
		}
		accStr := promptLine(reader, "7. 是否开启接收解析入站 Proxy Protocol v1/v2 协议头 (y/N)", "N")
		if strings.EqualFold(accStr, "y") || strings.EqualFold(accStr, "yes") || accStr == "1" || strings.EqualFold(accStr, "true") {
			acceptProxyProtocol = true
		}
		if logLevel == "" {
			logLevel = promptLine(reader, "8. 请输入日志级别 (debug / info / warning / error)", "info")
		}
		cfgPath = promptLine(reader, "9. 配置文件保存路径", cfgPath)
	}

	panelURL = strings.TrimRight(strings.TrimSpace(panelURL), "/")
	token = strings.TrimSpace(token)
	if apiVersion == "" {
		if machineID > 0 {
			apiVersion = "v2"
		} else {
			apiVersion = "v1"
		}
	}
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	if logLevel == "" {
		logLevel = "info"
	}

	ppVer, err := config.ParseProxyProtocol(proxyProtocol)
	if err != nil {
		return err
	}

	if panelURL == "" || token == "" || (len(nodeIDs) == 0 && machineID <= 0) {
		return fmt.Errorf("参数不完整：必须提供 --panel、--token 以及 --node-id（或 --node-ids / --machine-id）")
	}

	var b strings.Builder
	b.WriteString("# =============================================================================\n")
	b.WriteString("# Skyline MX Protocol Backend Configuration (/etc/skyline/config.yml)\n")
	b.WriteString("# 程序路径: /etc/skyline/skyline | 管理命令: 在终端直接输入 skyline\n")
	b.WriteString(fmt.Sprintf("# Generated at: %s\n", time.Now().Format(time.RFC3339)))
	b.WriteString("# =============================================================================\n\n")
	b.WriteString(fmt.Sprintf("log_level: %s\n", logLevel))
	b.WriteString(fmt.Sprintf("panel_url: %q\n", panelURL))
	b.WriteString(fmt.Sprintf("token: %q\n", token))
	b.WriteString(fmt.Sprintf("api_version: %s\n", apiVersion))
	b.WriteString(fmt.Sprintf("listen_ip: %q\n", listenIP))
	b.WriteString("pull_interval: 60s\n")
	b.WriteString("push_interval: 60s\n")
	b.WriteString("http_timeout: 15s\n")
	b.WriteString("device_ttl: 90s\n\n")

	b.WriteString("# Proxy Protocol 协议头配置 (可直接在此修改并执行 `skyline reload` 或在 `skyline` 菜单第 9 项切换)\n")
	b.WriteString("# proxy_protocol: 控制出站连接是否发送 Proxy Protocol 协议头，可选值: 0 (关闭), v1 (或 1), v2 (或 2)\n")
	if ppVer > 0 {
		b.WriteString(fmt.Sprintf("proxy_protocol: v%d\n", ppVer.Int()))
	} else {
		b.WriteString("proxy_protocol: 0\n")
	}
	b.WriteString("# accept_proxy_protocol: 控制入站监听是否接收并解析前端转发的 Proxy Protocol v1/v2 协议头\n")
	b.WriteString(fmt.Sprintf("accept_proxy_protocol: %v\n", acceptProxyProtocol))
	if strings.TrimSpace(forwardAddress) != "" {
		b.WriteString(fmt.Sprintf("forward_address: %q\n\n", strings.TrimSpace(forwardAddress)))
	} else {
		b.WriteString("# forward_address: \"127.0.0.1:8443\"  # 可选：将所有出站连接固定转发至指定下游地址（配合 proxy_protocol 发送真实客户端 IP）\n\n")
	}

	if machineID > 0 && len(nodeIDs) == 0 {
		b.WriteString(fmt.Sprintf("machine_id: %d\n", machineID))
	} else if len(nodeIDs) == 1 {
		b.WriteString(fmt.Sprintf("node_id: %d\n", nodeIDs[0]))
		b.WriteString("node_type: mx\n")
	} else {
		b.WriteString("nodes:\n")
		for _, id := range nodeIDs {
			b.WriteString(fmt.Sprintf("  - node_id: %d\n    node_type: mx\n", id))
		}
	}

	if dir := filepath.Dir(cfgPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
		}
	}
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0600); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", cfgPath, err)
	}

	fmt.Printf("\n%s[OK]%s 配置文件已成功写入: %s\n", ansiGreen, ansiReset, cfgPath)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("验证生成的配置文件失败: %w", err)
	}

	if needInteractive {
		ans := promptLine(reader, "是否立即检测与 Xboard 面板的连通性？(Y/n)", "Y")
		if strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
			return runCheckCommand(cfg)
		}
		return nil
	}
	return nil
}

// ============================================================================
// Preflight Diagnostic (`skyline check`)
// ============================================================================

func runCheckCommand(cfg *config.Config) error {
	fmt.Println("========================================================")
	fmt.Printf("  Skyline MX Backend v%s (M-core %s) - 节点自检诊断\n", Version, core.Version())
	fmt.Println("========================================================")
	fmt.Printf("  系统平台 : %s/%s (Go %s)\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Printf("  面板地址 : %s\n", cfg.PanelURL)
	fmt.Printf("  API 模式 : %s\n", cfg.APIVersion)
	fmt.Printf("  发送协议头 (Proxy Protocol) : %s\n", transportext.FormatProxyProtocolVersion(cfg.ProxyProtocol.Int()))
	fmt.Println("--------------------------------------------------------")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	targets := cfg.Nodes
	if len(targets) == 0 && cfg.MachineID > 0 {
		fmt.Printf("[*] 正在通过机器 ID #%d 拉取关联的 MX 节点列表...\n", cfg.MachineID)
		mc := panel.NewClient(panel.ClientOptions{
			PanelURL:   cfg.PanelURL,
			Token:      cfg.Token,
			MachineID:  cfg.MachineID,
			APIVersion: "v2",
			Timeout:    cfg.HTTPTimeout,
		})
		start := time.Now()
		resp, err := mc.GetMachineNodes(ctx)
		if err != nil {
			fmt.Printf("%s[FAIL]%s 机器节点发现失败: %v\n", ansiRed, ansiReset, err)
			return err
		}
		fmt.Printf("%s[PASS]%s 机器发现成功 (%dms)，共发现 %d 个节点\n", ansiGreen, ansiReset, time.Since(start).Milliseconds(), len(resp.Nodes))
		for _, n := range resp.Nodes {
			if strings.EqualFold(n.Type, "mx") {
				targets = append(targets, config.NodeTargetConfig{
					PanelURL:            cfg.PanelURL,
					Token:               cfg.Token,
					NodeID:              n.ID,
					NodeType:            "mx",
					MachineID:           cfg.MachineID,
					APIVersion:          cfg.APIVersion,
					ListenIP:            cfg.ListenIP,
					CertFile:            cfg.CertFile,
					KeyFile:             cfg.KeyFile,
					ProxyProtocol:       cfg.ProxyProtocol,
					AcceptProxyProtocol: cfg.AcceptProxyProtocol,
					ForwardAddress:      cfg.ForwardAddress,
				})
			}
		}
		if len(targets) == 0 {
			fmt.Printf("%s[WARN]%s 当前机器名下尚未绑定任何启用状态的 MX 节点\n", ansiYellow, ansiReset)
			return nil
		}
	}

	hasError := false
	for _, t := range targets {
		fmt.Printf("\n>>> 正在检测节点 #%d (type=%s, api=%s)\n", t.NodeID, t.NodeType, t.APIVersion)
		client := panel.NewClient(panel.ClientOptions{
			PanelURL:   t.PanelURL,
			Token:      t.Token,
			NodeID:     t.NodeID,
			NodeType:   t.NodeType,
			MachineID:  t.MachineID,
			APIVersion: t.APIVersion,
			Timeout:    t.HTTPTimeout,
		})

		t0 := time.Now()
		nodeCfg, _, err := client.GetNodeConfig(ctx)
		latency := time.Since(t0).Milliseconds()
		if err != nil {
			fmt.Printf("  %s[FAIL]%s 拉取节点配置失败 (%dms): %v\n", ansiRed, ansiReset, latency, err)
			fmt.Println("         排查建议: 1) 检查 panel_url 与 token 是否正确; 2) 确认 Xboard 后台已安装并启用 MxProtocol 插件; 3) 确认节点 ID 存在且为 mx 类型。")
			hasError = true
			continue
		}
		fmt.Printf("  %s[PASS]%s 面板通讯成功 (延迟: %dms)\n", ansiGreen, ansiReset, latency)

		tlsDesc := "无 TLS (Plaintext)"
		switch nodeCfg.TLS {
		case 1:
			tlsDesc = fmt.Sprintf("TLS (SNI=%s)", nodeCfg.ServerName)
		case 2:
			tlsDesc = fmt.Sprintf("REALITY (SNI=%s, ShortID=%s)", nodeCfg.TLSSettings.ServerName, nodeCfg.TLSSettings.ShortID)
		}
		sendPP := t.ProxyProtocol.Int()
		if sendPP == 0 {
			sendPP = nodeCfg.NetworkSettings.ProxyProtocol.Int()
		}
		acceptPP := nodeCfg.NetworkSettings.AcceptProxyProtocol.Bool()
		if t.AcceptProxyProtocol != nil && *t.AcceptProxyProtocol {
			acceptPP = true
		}
		fmt.Printf("  [INFO] 节点配置摘要: 监听=%s:%d | 传输协议=%s | 安全模式=%s | 发送PP=%s | 接收PP=%v | 路由规则=%d条\n",
			nodeCfg.ListenIP, nodeCfg.ServerPort, nodeCfg.Network, tlsDesc,
			transportext.FormatProxyProtocolVersion(sendPP), acceptPP, len(nodeCfg.Routes))

		if nodeCfg.TLS == 2 {
			if _, err := transportext.BuildRealityConfig(
				nodeCfg.TLSSettings.ServerName,
				nodeCfg.TLSSettings.PrivateKey,
				nodeCfg.TLSSettings.PublicKey,
				nodeCfg.TLSSettings.ShortID,
				nodeCfg.TLSSettings.ServerPort,
			); err != nil {
				fmt.Printf("  %s[FAIL]%s REALITY 密钥校验失败: %v\n", ansiRed, ansiReset, err)
				hasError = true
			} else {
				fmt.Printf("  %s[PASS]%s REALITY X25519 密钥对校验通过\n", ansiGreen, ansiReset)
			}
		} else if nodeCfg.TLS == 1 {
			certFile := t.CertFile
			if certFile == "" {
				certFile = nodeCfg.CertConfig.CertFile
			}
			keyFile := t.KeyFile
			if keyFile == "" {
				keyFile = nodeCfg.CertConfig.KeyFile
			}
			if _, err := transportext.BuildV2TLSConfig(
				nodeCfg.ServerName,
				nodeCfg.Host,
				certFile,
				keyFile,
				nodeCfg.CertConfig.CertPem,
				nodeCfg.CertConfig.KeyPem,
				nodeCfg.TLSSettings.AllowInsecure.Bool(),
			); err != nil {
				fmt.Printf("  %s[FAIL]%s TLS 证书校验失败: %v\n", ansiRed, ansiReset, err)
				hasError = true
			} else {
				fmt.Printf("  %s[PASS]%s TLS 证书配置就绪\n", ansiGreen, ansiReset)
			}
		}

		users, _, err := client.GetUsers(ctx)
		if err != nil {
			fmt.Printf("  %s[FAIL]%s 拉取授权用户列表失败: %v\n", ansiRed, ansiReset, err)
			hasError = true
		} else {
			fmt.Printf("  %s[PASS]%s 授权用户列表拉取成功 (当前有效用户数: %d)\n", ansiGreen, ansiReset, len(users))
		}

		bindIP := t.ListenIP
		if bindIP == "" {
			bindIP = nodeCfg.ListenIP
		}
		if bindIP == "" {
			bindIP = "0.0.0.0"
		}
		addr := fmt.Sprintf("%s:%d", bindIP, nodeCfg.ServerPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Printf("  %s[WARN]%s 本地端口 %s 已被占用或无权限绑定 (%v) — 若 Skyline 服务已在运行则属正常现象\n", ansiYellow, ansiReset, addr, err)
		} else {
			_ = ln.Close()
			fmt.Printf("  %s[PASS]%s 本地监听端口 %s 可用\n", ansiGreen, ansiReset, addr)
		}
	}

	fmt.Println("--------------------------------------------------------")
	if hasError {
		return fmt.Errorf("节点自检发现异常，请根据上方提示修正配置")
	}
	fmt.Printf("%s[SUCCESS]%s 所有节点自检全部通过！直接输入 `skyline` 或 `skyline start` 即可管理与启动服务。\n", ansiGreen, ansiReset)
	return nil
}

// ============================================================================
// Systemd Service Management (`skyline service <action>` / `skyline <action>`)
// ============================================================================

func ensureGlobalSymlinks(targetBin string) {
	for _, linkPath := range []string{symlinkBinPath, usrBinPath} {
		_ = os.Remove(linkPath)
		if err := os.Symlink(targetBin, linkPath); err != nil {
			_ = copyExecutable(targetBin, linkPath)
		}
	}
}

func runServiceCommand(action string, cfgPath string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("`skyline %s` 服务管理命令仅在 Linux 系统 (systemd) 下可用", action)
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if cfgPath == "" {
		cfgPath = defaultCfgPath
	}
	absCfgPath, err := filepath.Abs(cfgPath)
	if err == nil {
		cfgPath = absCfgPath
	}

	switch action {
	case "install":
		if os.Geteuid() != 0 {
			return fmt.Errorf("安装 systemd 服务需要 root 权限，请使用 sudo skyline install")
		}
		if err := os.MkdirAll(defaultDir, 0755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", defaultDir, err)
		}
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("获取当前可执行文件路径失败: %w", err)
		}
		exePath, _ = filepath.EvalSymlinks(exePath)
		if exePath != defaultBinPath {
			if err := copyExecutable(exePath, defaultBinPath); err != nil {
				return fmt.Errorf("复制二进制文件到 %s 失败: %w", defaultBinPath, err)
			}
			fmt.Printf("%s[OK]%s 已安装程序到目录: %s\n", ansiGreen, ansiReset, defaultBinPath)
		}
		ensureGlobalSymlinks(defaultBinPath)
		fmt.Printf("%s[OK]%s 已注册全局终端命令: `skyline` (软链接: %s -> %s)\n", ansiGreen, ansiReset, symlinkBinPath, defaultBinPath)

		unitContent := fmt.Sprintf(`[Unit]
Description=Skyline MX Protocol Backend for Xboard
Documentation=https://github.com/Grandova/skyline
After=network.target network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/etc/skyline
ExecStart=%s run -c %s
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s
LimitNOFILE=1048576
LimitNPROC=65535
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, defaultBinPath, cfgPath)

		if err := os.WriteFile(systemdUnitPath, []byte(unitContent), 0644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", systemdUnitPath, err)
		}
		fmt.Printf("%s[OK]%s 已生成 systemd 单元文件: %s\n", ansiGreen, ansiReset, systemdUnitPath)

		if err := runSystemctl("daemon-reload"); err != nil {
			return err
		}
		if err := runSystemctl("enable", "skyline"); err != nil {
			return err
		}
		if _, err := os.Stat(cfgPath); err == nil {
			if err := runSystemctl("restart", "skyline"); err != nil {
				return err
			}
			fmt.Printf("%s[OK]%s Skyline 服务已启动并设置为开机自启！（任何时候在终端输入 `skyline` 即可打开管理面板）\n", ansiGreen, ansiReset)
			_ = runSystemctl("status", "--no-pager", "skyline")
		} else {
			fmt.Printf("%s[WARN]%s 尚未检测到配置文件 %s，请运行 `skyline init` 或直接输入 `skyline` 进入管理面板生成配置。\n", ansiYellow, ansiReset, cfgPath)
		}
		return nil

	case "uninstall", "remove":
		if os.Geteuid() != 0 {
			return fmt.Errorf("卸载 systemd 服务需要 root 权限")
		}
		_ = runSystemctl("stop", "skyline")
		_ = runSystemctl("disable", "skyline")
		_ = os.Remove(systemdUnitPath)
		_ = runSystemctl("daemon-reload")
		_ = os.Remove(symlinkBinPath)
		_ = os.Remove(usrBinPath)
		_ = os.Remove(defaultBinPath)
		fmt.Printf("%s[OK]%s Skyline 服务与程序已卸载（配置文件保留在 %s）。\n", ansiGreen, ansiReset, cfgPath)
		return nil

	case "start", "stop", "restart", "reload", "enable", "disable":
		if err := runSystemctl(action, "skyline"); err != nil {
			return err
		}
		fmt.Printf("%s[OK]%s skyline 服务已执行 %s\n", ansiGreen, ansiReset, action)
		return nil

	case "status":
		return runSystemctl("status", "--no-pager", "skyline")

	case "log", "logs":
		cmd := exec.Command("journalctl", "-u", "skyline", "-f", "-n", "100", "--no-hostname")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()

	default:
		return fmt.Errorf("未知的 service 子命令 %q（可选: install | uninstall | start | stop | restart | reload | status | log）", action)
	}
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func printUsage() {
	fmt.Printf(`Skyline MX Protocol Backend v%s (M-core %s)
专为 Xboard (MxProtocol 插件) 打造的 Linux 高性能 MX 协议后端代理程序
程序与配置文件统一存放目录: /etc/skyline (/etc/skyline/skyline & /etc/skyline/config.yml)

用法 (Usage):
  skyline                    直接进入交互式终端管理面板 (推荐！)
  skyline [命令] [参数]

快捷管理命令 (Management Commands):
  (无参数) / menu            打开交互式中文管理面板（启停服务、修改配置、切换 Proxy Protocol、诊断等）
  init                       交互式向导或命令行一键生成 /etc/skyline/config.yml
  config / edit              直接调用编辑器修改 /etc/skyline/config.yml 并在保存后自动校验与热重载
  pp                         快捷配置 Proxy Protocol v1/v2 协议头 (发送 / 接收 / 转发地址)
  check / doctor             检测与 Xboard 面板连通性、核验密钥、TLS/REALITY 及 Proxy Protocol 状态
  start / stop / restart     启动 / 停止 / 重启 Skyline systemd 服务
  reload                     平滑热重载 /etc/skyline/config.yml 配置文件
  status                     查看 Skyline 服务运行状态
  log / logs                 实时滚动查看服务运行日志 (journalctl -u skyline -f)
  reality                    一键生成 REALITY X25519 公私钥对与 ShortID
  plugin [dir]               从 GitHub Release 下载并解压 Xboard MxProtocol 插件包
  install / uninstall        安装或卸载 Linux systemd 开机自启服务
  update                     在线更新 Skyline 到 GitHub Release 最新版本
  run                        在前台运行 Skyline 节点守护进程 (供 systemd 或容器调用)
  version / help             查看版本信息或本帮助说明

常用参数 (Flags):
  -c, --config <path>           配置文件路径 (默认: /etc/skyline/config.yml)
  --panel <url>                 Xboard 面板地址 (例如 https://panel.example.com)
  --token <token>               Xboard 通讯密钥 (server_token 或 machine_token)
  --node-id <id>                单个 MX 节点 ID
  --node-ids <id1,id2,...>      多个 MX 节点 ID（逗号分隔，例如 1,2,3）
  --machine-id <id>             Xboard V2 机器 ID（开启机器自动发现模式）
  --api-version <v1|v2>         面板接口版本: v1 (默认 UniProxy) 或 v2
  --listen-ip <ip>              覆盖监听 IP (默认 0.0.0.0)
  --proxy-protocol <0|v1|v2>    发送 Proxy Protocol 协议头版本: 0(关闭), v1(或1), v2(或2)
  --accept-proxy-protocol       开启接收并解析入站 Proxy Protocol v1 & v2 协议头
  --forward-address <addr>      可选：固定出站转发目标地址 (如 127.0.0.1:8443)
  --cert-file <path>            TLS 证书文件路径 (可选)
  --key-file <path>             TLS 私钥文件路径 (可选)
  --log-level <level>           日志级别: debug, info, warning, error, none
  -y, --yes                     在 init 命令中使用非交互模式
`, Version, core.Version())
}
