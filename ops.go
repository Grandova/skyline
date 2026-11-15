package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	core "github.com/v2fly/v2ray-core/v5"

	"skyline/config"
	"skyline/panel"
	"skyline/transportext"
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

// runInitCommand generates a Skyline YAML config interactively or via CLI flags.
func runInitCommand(cfgPath, panelURL, token, nodeIDsStr string, singleNodeID, machineID int, apiVersion, listenIP, logLevel string, nonInteractive bool) error {
	if cfgPath == "" {
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			cfgPath = "/etc/skyline/config.yml"
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
		if logLevel == "" {
			logLevel = promptLine(reader, "6. 请输入日志级别 (debug / info / warning / error)", "info")
		}
		cfgPath = promptLine(reader, "7. 配置文件保存路径", cfgPath)
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

	if panelURL == "" || token == "" || (len(nodeIDs) == 0 && machineID <= 0) {
		return fmt.Errorf("参数不完整：必须提供 --panel、--token 以及 --node-id（或 --node-ids / --machine-id）")
	}

	var b strings.Builder
	b.WriteString("# =============================================================================\n")
	b.WriteString("# Skyline MX Protocol Backend Configuration\n")
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

	fmt.Printf("\n[OK] 配置文件已成功写入: %s\n", cfgPath)

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

// runCheckCommand performs a comprehensive preflight check against Xboard and local node environment.
func runCheckCommand(cfg *config.Config) error {
	fmt.Println("========================================================")
	fmt.Printf("  Skyline MX Backend v%s (M-core %s) - 节点自检诊断\n", Version, core.Version())
	fmt.Println("========================================================")
	fmt.Printf("  系统平台 : %s/%s (Go %s)\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Printf("  面板地址 : %s\n", cfg.PanelURL)
	fmt.Printf("  API 模式 : %s\n", cfg.APIVersion)
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
			fmt.Printf("[FAIL] 机器节点发现失败: %v\n", err)
			return err
		}
		fmt.Printf("[PASS] 机器发现成功 (%dms)，共发现 %d 个节点\n", time.Since(start).Milliseconds(), len(resp.Nodes))
		for _, n := range resp.Nodes {
			if strings.EqualFold(n.Type, "mx") {
				targets = append(targets, config.NodeTargetConfig{
					PanelURL:   cfg.PanelURL,
					Token:      cfg.Token,
					NodeID:     n.ID,
					NodeType:   "mx",
					MachineID:  cfg.MachineID,
					APIVersion: cfg.APIVersion,
					ListenIP:   cfg.ListenIP,
					CertFile:   cfg.CertFile,
					KeyFile:    cfg.KeyFile,
				})
			}
		}
		if len(targets) == 0 {
			fmt.Println("[WARN] 当前机器名下尚未绑定任何启用状态的 MX 节点")
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
			fmt.Printf("  [FAIL] 拉取节点配置失败 (%dms): %v\n", latency, err)
			fmt.Println("         排查建议: 1) 检查 panel_url 与 token 是否正确; 2) 确认 Xboard 后台已安装并启用 MxProtocol 插件; 3) 确认节点 ID 存在且为 mx 类型。")
			hasError = true
			continue
		}
		fmt.Printf("  [PASS] 面板通讯成功 (延迟: %dms)\n", latency)

		tlsDesc := "无 TLS (Plaintext)"
		switch nodeCfg.TLS {
		case 1:
			tlsDesc = fmt.Sprintf("TLS (SNI=%s)", nodeCfg.ServerName)
		case 2:
			tlsDesc = fmt.Sprintf("REALITY (SNI=%s, ShortID=%s)", nodeCfg.TLSSettings.ServerName, nodeCfg.TLSSettings.ShortID)
		}
		fmt.Printf("  [INFO] 节点配置摘要: 监听=%s:%d | 传输协议=%s | 安全模式=%s | 路由规则=%d条\n",
			nodeCfg.ListenIP, nodeCfg.ServerPort, nodeCfg.Network, tlsDesc, len(nodeCfg.Routes))

		// Validate TLS / REALITY config locally
		if nodeCfg.TLS == 2 {
			if _, err := transportext.BuildRealityConfig(
				nodeCfg.TLSSettings.ServerName,
				nodeCfg.TLSSettings.PrivateKey,
				nodeCfg.TLSSettings.PublicKey,
				nodeCfg.TLSSettings.ShortID,
				nodeCfg.TLSSettings.ServerPort,
			); err != nil {
				fmt.Printf("  [FAIL] REALITY 密钥校验失败: %v\n", err)
				hasError = true
			} else {
				fmt.Println("  [PASS] REALITY X25519 密钥对校验通过")
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
				fmt.Printf("  [FAIL] TLS 证书校验失败: %v\n", err)
				hasError = true
			} else {
				fmt.Println("  [PASS] TLS 证书配置就绪")
			}
		}

		// Pull active users
		users, _, err := client.GetUsers(ctx)
		if err != nil {
			fmt.Printf("  [FAIL] 拉取授权用户列表失败: %v\n", err)
			hasError = true
		} else {
			fmt.Printf("  [PASS] 授权用户列表拉取成功 (当前有效用户数: %d)\n", len(users))
		}

		// Check local port availability
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
			fmt.Printf("  [WARN] 本地端口 %s 已被占用或无权限绑定 (%v) — 若 Skyline 服务已在运行则属正常现象\n", addr, err)
		} else {
			_ = ln.Close()
			fmt.Printf("  [PASS] 本地监听端口 %s 可用\n", addr)
		}
	}

	fmt.Println("--------------------------------------------------------")
	if hasError {
		return fmt.Errorf("节点自检发现异常，请根据上方提示修正配置")
	}
	fmt.Println("[SUCCESS] 所有节点自检全部通过！可执行 `skyline run` 或 `skyline service start` 启动服务。")
	return nil
}

// runServiceCommand manages the Linux systemd service for Skyline.
func runServiceCommand(action string, cfgPath string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("`skyline service` 命令仅在 Linux 系统 (systemd) 下可用")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if cfgPath == "" {
		cfgPath = "/etc/skyline/config.yml"
	}
	absCfgPath, err := filepath.Abs(cfgPath)
	if err == nil {
		cfgPath = absCfgPath
	}

	const (
		targetBin   = "/usr/local/bin/skyline"
		serviceFile = "/etc/systemd/system/skyline.service"
	)

	switch action {
	case "install":
		if os.Geteuid() != 0 {
			return fmt.Errorf("安装 systemd 服务需要 root 权限，请使用 sudo skyline service install")
		}
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("获取当前可执行文件路径失败: %w", err)
		}
		exePath, _ = filepath.EvalSymlinks(exePath)
		if exePath != targetBin {
			if err := copyExecutable(exePath, targetBin); err != nil {
				return fmt.Errorf("复制二进制文件到 %s 失败: %w", targetBin, err)
			}
			fmt.Printf("[OK] 已安装二进制文件: %s\n", targetBin)
		}

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
`, targetBin, cfgPath)

		if err := os.MkdirAll("/etc/skyline", 0755); err != nil {
			return err
		}
		if err := os.WriteFile(serviceFile, []byte(unitContent), 0644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", serviceFile, err)
		}
		fmt.Printf("[OK] 已生成 systemd 单元文件: %s\n", serviceFile)

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
			fmt.Println("[OK] Skyline 服务已启动并设置为开机自启！")
			_ = runSystemctl("status", "--no-pager", "skyline")
		} else {
			fmt.Printf("[WARN] 尚未检测到配置文件 %s，请先运行 `skyline init` 生成配置后再执行 `skyline service start`。\n", cfgPath)
		}
		return nil

	case "uninstall", "remove":
		if os.Geteuid() != 0 {
			return fmt.Errorf("卸载 systemd 服务需要 root 权限")
		}
		_ = runSystemctl("stop", "skyline")
		_ = runSystemctl("disable", "skyline")
		_ = os.Remove(serviceFile)
		_ = runSystemctl("daemon-reload")
		fmt.Println("[OK] Skyline systemd 服务已卸载。")
		return nil

	case "start", "stop", "restart", "reload":
		if err := runSystemctl(action, "skyline"); err != nil {
			return err
		}
		fmt.Printf("[OK] skyline 服务已执行 %s\n", action)
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
		return fmt.Errorf("未知的 service 子命令 %q（可选: install | uninstall | start | stop | restart | status | log）", action)
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

用法 (Usage):
  skyline [命令] [参数]

核心命令 (Commands):
  run                启动 Skyline 节点后端守护进程（默认命令，可省略）
  init               交互式向导或命令行一键生成 config.yml 配置文件
  check / doctor     检测与 Xboard 面板连通性、核验密钥与节点 TLS/REALITY 配置
  service <action>   管理 Linux systemd 服务:
                       install   安装并启用开机自启服务
                       uninstall 停止并移除 systemd 服务
                       start     启动服务
                       stop      停止服务
                       restart   重启服务
                       status    查看服务运行状态
                       log       实时查看服务运行日志 (journalctl -f)
  version            显示版本与内核信息
  help               显示本帮助说明

常用参数 (Flags):
  -c, --config <path>       配置文件路径 (默认自动查找 ./config.yml 或 /etc/skyline/config.yml)
  --panel <url>             Xboard 面板地址 (例如 https://panel.example.com)
  --token <token>           Xboard 通讯密钥 (server_token 或 machine_token)
  --node-id <id>            单个 MX 节点 ID
  --node-ids <id1,id2,...>  多个 MX 节点 ID（逗号分隔，例如 1,2,3）
  --machine-id <id>         Xboard V2 机器 ID（开启机器自动发现模式）
  --api-version <v1|v2>     面板接口版本: v1 (默认 UniProxy) 或 v2
  --listen-ip <ip>          覆盖监听 IP (默认 0.0.0.0)
  --cert-file <path>        TLS 证书文件路径 (可选)
  --key-file <path>         TLS 私钥文件路径 (可选)
  --log-level <level>       日志级别: debug, info, warning, error, none
  -y, --yes                 在 init 命令中使用非交互模式

快速上手示例 (Examples):
  1. 交互式初始化配置并自检:
     skyline init

  2. 一行命令生成配置并安装启动 systemd 服务:
     skyline init --panel https://panel.example.com --token YOUR_TOKEN --node-id 1 -y
     skyline check
     sudo skyline service install

  3. 免配置文件直接通过命令行参数启动单/多节点:
     skyline run --panel https://panel.example.com --token YOUR_TOKEN --node-ids 1,2
`, Version, core.Version())
}
