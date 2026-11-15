package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	core "github.com/v2fly/v2ray-core/v5"

	"skyline/config"
	"skyline/node"
)

const Version = "0.3.1"

func main() {
	args := os.Args[1:]

	// When invoked in terminal with no arguments (`skyline`), launch the interactive management panel.
	if len(args) == 0 {
		if err := runManageMenu(resolveDefaultConfigPath("")); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return
	}

	subcmd := "run"
	if !strings.HasPrefix(args[0], "-") {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
		args = args[1:]
	}

	switch subcmd {
	case "version", "-v", "--version":
		fmt.Printf("Skyline MX Backend v%s (M-core %s, %s/%s, %s)\n",
			Version, core.Version(), runtime.GOOS, runtime.GOARCH, runtime.Version())
		return

	case "help", "-h", "--help":
		printUsage()
		return

	case "menu", "manage", "panel", "ui":
		cfgPath := parseConfigFlagOnly(subcmd, args)
		if err := runManageMenu(resolveDefaultConfigPath(cfgPath)); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "reality", "x25519", "keygen":
		if err := runRealityKeygenCommand(); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "plugin":
		targetDir := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			targetDir = args[0]
		}
		if err := runPluginDownloadCommand(targetDir); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "update", "upgrade":
		if err := runSelfUpdateCommand(); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "config", "edit":
		cfgPath := parseConfigFlagOnly(subcmd, args)
		if err := runEditConfigCommand(resolveDefaultConfigPath(cfgPath)); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "pp", "proxy-protocol":
		if err := runProxyProtocolCLI(args); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "start", "stop", "restart", "reload", "status", "enable", "disable", "log", "logs", "install", "uninstall":
		cfgPath := parseConfigFlagOnly(subcmd, args)
		if err := runServiceCommand(subcmd, resolveDefaultConfigPath(cfgPath)); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return

	case "service", "svc":
		action := "status"
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			action = args[0]
			args = args[1:]
		}
		cfgPath := parseConfigFlagOnly("service", args)
		if err := runServiceCommand(action, resolveDefaultConfigPath(cfgPath)); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return
	}

	var (
		configPath          string
		panelURL            string
		token               string
		nodeID              int
		nodeIDsStr          string
		nodeType            string
		machineID           int
		apiVersion          string
		listenIP            string
		certFile            string
		keyFile             string
		proxyProtocol       string
		acceptProxyProtocol bool
		forwardAddress      string
		logLevel            string
		showVersion         bool
		nonInteractive      bool
	)

	fs := flag.NewFlagSet(subcmd, flag.ExitOnError)
	fs.Usage = printUsage
	fs.StringVar(&configPath, "c", "", "Path to config file (default: /etc/skyline/config.yml)")
	fs.StringVar(&configPath, "config", "", "Path to config file (default: /etc/skyline/config.yml)")
	fs.StringVar(&panelURL, "panel", "", "Xboard panel URL (e.g. https://panel.example.com)")
	fs.StringVar(&panelURL, "api-host", "", "Alias for --panel")
	fs.StringVar(&token, "token", "", "Xboard server token or machine token")
	fs.StringVar(&token, "api-key", "", "Alias for --token")
	fs.IntVar(&nodeID, "node-id", 0, "Xboard MX node ID")
	fs.StringVar(&nodeIDsStr, "node-ids", "", "Comma-separated Xboard MX node IDs (e.g. 1,2,3)")
	fs.StringVar(&nodeType, "node-type", "mx", "Node protocol type (default: mx)")
	fs.IntVar(&machineID, "machine-id", 0, "Xboard V2 machine ID (optional)")
	fs.StringVar(&apiVersion, "api-version", "", "Panel API version: v1, v2, or auto (default: v1)")
	fs.StringVar(&listenIP, "listen-ip", "", "Override listen IP address")
	fs.StringVar(&certFile, "cert-file", "", "TLS certificate PEM file path")
	fs.StringVar(&keyFile, "key-file", "", "TLS private key PEM file path")
	fs.StringVar(&proxyProtocol, "proxy-protocol", "", "Send Proxy Protocol header version: 0/off, 1/v1, or 2/v2")
	fs.BoolVar(&acceptProxyProtocol, "accept-proxy-protocol", false, "Accept inbound Proxy Protocol v1 & v2 header")
	fs.StringVar(&forwardAddress, "forward-address", "", "Optional outbound target address (host:port or IP)")
	fs.StringVar(&logLevel, "log-level", "", "Log level (debug, info, warning, error, none)")
	fs.BoolVar(&showVersion, "version", false, "Print version information and exit")
	fs.BoolVar(&nonInteractive, "y", false, "Non-interactive mode for init")
	fs.BoolVar(&nonInteractive, "yes", false, "Non-interactive mode for init")
	_ = fs.Parse(args)

	if showVersion {
		fmt.Printf("Skyline MX Backend v%s (M-core %s, %s/%s, %s)\n",
			Version, core.Version(), runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}

	if subcmd == "init" || subcmd == "setup" {
		if err := runInitCommand(configPath, panelURL, token, nodeIDsStr, nodeID, machineID, apiVersion, listenIP, proxyProtocol, acceptProxyProtocol, forwardAddress, logLevel, nonInteractive); err != nil {
			log.Fatalf("[Skyline] %v", err)
		}
		return
	}

	configPath = resolveDefaultConfigPath(configPath)

	cfg, err := loadAndMergeConfig(configPath, panelURL, token, nodeID, nodeIDsStr, nodeType, machineID, apiVersion, listenIP, certFile, keyFile, proxyProtocol, acceptProxyProtocol, forwardAddress, logLevel)
	if err != nil {
		log.Fatalf("[Skyline] %v\n提示: 直接输入 `skyline` 可进入交互式管理面板，或运行 `skyline init` 生成配置文件 (/etc/skyline/config.yml)。", err)
	}

	switch subcmd {
	case "check", "doctor", "test":
		if err := runCheckCommand(cfg); err != nil {
			os.Exit(1)
		}
		return
	case "run":
		// Proceed to daemon startup below
	default:
		log.Fatalf("[Skyline] 未知命令 %q，直接输入 `skyline` 可进入管理面板，或运行 `skyline help` 查看帮助。", subcmd)
	}

	logger := log.New(os.Stdout, "[Skyline] ", log.LstdFlags)
	logger.Printf("Starting Skyline MX Backend v%s (M-core %s)", Version, core.Version())

	mgr := node.NewManager(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		log.Fatalf("[Skyline] Failed to start node manager: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for {
		sig := <-sigCh
		if sig == syscall.SIGHUP {
			logger.Printf("Received SIGHUP, reloading configuration from %s...", configPath)
			newCfg, err := loadAndMergeConfig(configPath, panelURL, token, nodeID, nodeIDsStr, nodeType, machineID, apiVersion, listenIP, certFile, keyFile, proxyProtocol, acceptProxyProtocol, forwardAddress, logLevel)
			if err != nil {
				logger.Printf("Configuration reload rejected: %v", err)
				continue
			}
			_ = mgr.Stop()
			mgr = node.NewManager(newCfg, logger)
			if err := mgr.Start(ctx); err != nil {
				logger.Printf("Failed to restart manager after reload: %v", err)
			} else {
				logger.Printf("Configuration reloaded successfully.")
			}
			continue
		}

		logger.Printf("Received signal %v, shutting down gracefully...", sig)
		break
	}

	if err := mgr.Stop(); err != nil {
		logger.Printf("Shutdown error: %v", err)
	}
	logger.Printf("Skyline stopped.")
}

func parseConfigFlagOnly(name string, args []string) string {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cfgPath string
	fs.StringVar(&cfgPath, "c", "", "Path to config file")
	fs.StringVar(&cfgPath, "config", "", "Path to config file")
	_ = fs.Parse(args)
	return cfgPath
}

func resolveDefaultConfigPath(configPath string) string {
	if strings.TrimSpace(configPath) != "" {
		return configPath
	}
	for _, candidate := range []string{
		"/etc/skyline/config.yml",
		"/etc/skyline/config.yaml",
		"/etc/skyline/config.json",
		"config.yml",
		"config.yaml",
		"config.json",
	} {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	if runtime.GOOS == "linux" {
		return "/etc/skyline/config.yml"
	}
	return "config.yml"
}

func loadAndMergeConfig(
	configPath, panelURL, token string,
	nodeID int,
	nodeIDsStr, nodeType string,
	machineID int,
	apiVersion, listenIP, certFile, keyFile, proxyProtocol string,
	acceptProxyProtocol bool,
	forwardAddress, logLevel string,
) (*config.Config, error) {
	var cfg *config.Config
	if configPath != "" {
		if st, err := os.Stat(configPath); err == nil && !st.IsDir() {
			loaded, loadErr := config.Load(configPath)
			if loadErr != nil {
				return nil, fmt.Errorf("加载配置文件失败: %w", loadErr)
			}
			cfg = loaded
		} else if panelURL == "" && os.Getenv("SKYLINE_PANEL_URL") == "" {
			return nil, fmt.Errorf("未找到配置文件 %s", configPath)
		}
	}
	if cfg == nil {
		cfg = config.Default()
		cfg.ApplyEnv()
	}

	if panelURL != "" {
		cfg.PanelURL = panelURL
	}
	if token != "" {
		cfg.Token = token
	}
	if nodeID > 0 {
		cfg.NodeID = nodeID
	}
	if nodeIDsStr != "" {
		ids, err := parseNodeIDList(nodeIDsStr)
		if err != nil {
			return nil, err
		}
		cfg.Nodes = nil
		for _, id := range ids {
			cfg.Nodes = append(cfg.Nodes, config.NodeTargetConfig{
				NodeID:   id,
				NodeType: nodeType,
			})
		}
	}
	if nodeType != "" {
		cfg.NodeType = nodeType
	}
	if machineID > 0 {
		cfg.MachineID = machineID
	}
	if apiVersion != "" {
		cfg.APIVersion = apiVersion
	}
	if listenIP != "" {
		cfg.ListenIP = listenIP
	}
	if certFile != "" {
		cfg.CertFile = certFile
	}
	if keyFile != "" {
		cfg.KeyFile = keyFile
	}
	if proxyProtocol != "" {
		ver, err := config.ParseProxyProtocol(proxyProtocol)
		if err != nil {
			return nil, err
		}
		cfg.ProxyProtocol = ver
	}
	if acceptProxyProtocol {
		v := true
		cfg.AcceptProxyProtocol = &v
	}
	if forwardAddress != "" {
		cfg.ForwardAddress = forwardAddress
	}
	if logLevel != "" {
		cfg.LogLevel = logLevel
	}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验未通过: %w", err)
	}
	return cfg, nil
}
