package main

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/MegaGrindStone/go-mcp"
	mcpwebui "github.com/MegaGrindStone/mcp-web-ui"
	"github.com/MegaGrindStone/mcp-web-ui/internal/handlers"
	"github.com/MegaGrindStone/mcp-web-ui/internal/services"
	"gopkg.in/yaml.v3"
)

func main() {
	cfg, cfgDir := loadConfig()

	logger, logFile := initLogger(cfg, cfgDir)
	defer logFile.Close()

	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = "You are a helpful assistant."
	}
	llm, err := cfg.LLM.llm(sysPrompt, logger)
	if err != nil {
		panic(err)
	}
	titleGenPrompt := cfg.TitleGeneratorPrompt
	if titleGenPrompt == "" {
		titleGenPrompt = "Generate a title for this chat with only one sentence with maximum 5 words."
	}
	titleGen, err := cfg.GenTitleLLM.titleGen(titleGenPrompt, logger)
	if err != nil {
		panic(err)
	}

	dbPath := filepath.Join(cfgDir, "/mcpwebui/store.db")
	boltDB, err := services.NewBoltDB(dbPath)
	if err != nil {
		panic(err)
	}

	mcpClientInfo := mcp.Info{
		Name:    "mcp-web-ui",
		Version: "0.1.0",
	}

	mcpClients, stdIOCmds := populateMCPClients(cfg, mcpClientInfo, logger)

	for i, cli := range mcpClients {
		logger.Info("Connecting to MCP server", slog.Int("index", i))

		connectCtx, connectCancel := context.WithTimeout(context.Background(), 30*time.Second)

		if err := cli.Connect(connectCtx); err != nil {
			connectCancel()
			logger.Error("Error connecting to MCP server", slog.Int("index", i), slog.String("err", err.Error()))
			continue
		}
		connectCancel()

		mcpClients[i] = cli

		logger.Info("Connected to MCP server", slog.String("name", mcpClients[i].ServerInfo().Name))
	}

	m, err := handlers.NewMain(llm, titleGen, boltDB, mcpClients, logger)
	if err != nil {
		panic(err)
	}

	// Serve static files
	staticFS, err := fs.Sub(mcpwebui.StaticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(staticFS))

	// Create custom mux
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))
	mux.HandleFunc("/", m.HandleHome)
	mux.HandleFunc("/chats", m.HandleChats)
	mux.HandleFunc("/refresh-title", m.HandleRefreshTitle)
	mux.HandleFunc("/sse/messages", m.HandleSSE)
	mux.HandleFunc("/sse/chats", m.HandleSSE)

	// Create custom server
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	srv.RegisterOnShutdown(func() {
		for _, cli := range mcpClients {
			disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := cli.Disconnect(disconnectCtx); err != nil {
				logger.Error("Failed to disconnect from MCP server", slog.String("err", err.Error()))
			}
			disconnectCancel()
		}

		for _, cmd := range stdIOCmds {
			if err := cmd.Process.Kill(); err != nil {
				logger.Error("Failed to kill stdIO command", slog.String("err", err.Error()))
			}
			_ = cmd.Wait()
		}

		if err := m.Shutdown(context.Background()); err != nil {
			logger.Error("Failed to shutdown sse server", slog.String("err", err.Error()))
		}
	})

	// Channel to listen for errors coming from the listener
	serverErrors := make(chan error, 1)

	// Start server in goroutine
	go func() {
		logger.Info("Server starting on", slog.String("port", cfg.Port))
		serverErrors <- srv.ListenAndServe()
	}()

	// Channel to listen for interrupt/terminate signals
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Blocking select waiting for either interrupt or server error
	select {
	case err := <-serverErrors:
		logger.Error("Server error", slog.String("err", err.Error()))

	case sig := <-shutdown:
		logger.Info("Start shutdown", slog.String("signal", sig.String()))

		// Create context with timeout for shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Gracefully shutdown the server
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("Graceful shutdown failed", slog.String("err", err.Error()))
			logger.Info("Forcing server close")
			if err := srv.Close(); err != nil {
				logger.Error("Failed to forcing server close", slog.String("err", err.Error()))
			}
		}
	}
}

func loadConfig() (config, string) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(fmt.Errorf("error getting user config dir: %w", err))
	}
	cfgPath := filepath.Join(cfgDir, "/mcpwebui")
	if err := os.MkdirAll(cfgPath, 0755); err != nil {
		log.Fatal(fmt.Errorf("error creating config directory: %w", err))
	}

	cfgFilePath := filepath.Join(cfgDir, "/mcpwebui/config.yaml")
	cfgFile, err := os.Open(cfgFilePath)
	if err != nil {
		log.Fatal(fmt.Errorf("error opening config file: %w", err))
	}
	defer cfgFile.Close()

	cfg := config{}
	if err := yaml.NewDecoder(cfgFile).Decode(&cfg); err != nil {
		panic(fmt.Errorf("error decoding config file: %w", err))
	}
	return cfg, cfgDir
}

func initLogger(cfg config, cfgDir string) (*slog.Logger, *os.File) {
	logLevel := new(slog.LevelVar)
	switch cfg.LogLevel {
	case "debug":
		logLevel.Set(slog.LevelDebug)
	case "info":
		logLevel.Set(slog.LevelInfo)
	case "warn":
		logLevel.Set(slog.LevelWarn)
	case "error":
		logLevel.Set(slog.LevelError)
	default:
		log.Printf("Invalid log level '%s', defaulting to 'info'", cfg.LogLevel)
		logLevel.Set(slog.LevelInfo)
	}

	logFile, err := os.Create(filepath.Join(cfgDir, "mcpwebui/mcpwebui.log"))
	if err != nil {
		log.Fatalf("Error creating log file: %v", err)
	}

	var lg *slog.Logger
	switch cfg.LogMode {
	case "json":
		lg = slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: logLevel}))
	default:
		lg = slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: logLevel}))
	}

	// llmJSON, err := json.Marshal(cfg.LLM)
	// if err != nil {
	// 	log.Fatalf("Error marshaling LLM config: %v", err)
	// }
	//
	// titleGenJSON, err := json.Marshal(cfg.GenTitleLLM)
	// if err != nil {
	// 	log.Fatalf("Error marshaling title generator config: %v", err)
	// }

	logger := lg.With(
		slog.Group("config",
			slog.String("port", cfg.Port),
			slog.String("logLevel", cfg.LogLevel),
			slog.String("logMode", cfg.LogMode),

			// These two configuration can be very long, and would potenially fill up the log file.
			// slog.String("systemPrompt", cfg.SystemPrompt),
			// slog.String("titleGeneratorPrompt", cfg.TitleGeneratorPrompt),

			// These two configuration would leak the llm credentials in the log file.
			// slog.Any("llm", llmJSON),
			// slog.Any("genTitleLLM", titleGenJSON),

			slog.Any("mcpSSEServers", cfg.MCPSSEServers),
			slog.Any("mcpStdIOServers", cfg.MCPStdIOServers),
		),
	)

	return logger, logFile
}

func populateMCPClients(cfg config, mcpClientInfo mcp.Info, logger *slog.Logger) ([]*mcp.Client, []*exec.Cmd) {
	var mcpClients []*mcp.Client

	for _, mcpSSEServerConfig := range cfg.MCPSSEServers {
		sseClient := mcp.NewSSEClient(mcpSSEServerConfig.URL, nil,
			mcp.WithSSEClientMaxPayloadSize(mcpSSEServerConfig.MaxPayloadSize), mcp.WithSSEClientLogger(logger))
		cli := mcp.NewClient(mcpClientInfo, sseClient, mcp.WithClientLogger(logger))
		mcpClients = append(mcpClients, cli)
	}

	cmds := make([]*exec.Cmd, 0, len(cfg.MCPStdIOServers))
	for _, mcpStdIOServerConfig := range cfg.MCPStdIOServers {
		cmd := exec.Command(mcpStdIOServerConfig.Command, mcpStdIOServerConfig.Args...)
		cmds = append(cmds, cmd)

		in, err := cmd.StdinPipe()
		if err != nil {
			panic(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			panic(err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			panic(err)
		}

		if err := cmd.Start(); err != nil {
			panic(err)
		}

		// Listen for stderr output and log it
		go func() {
			errScanner := bufio.NewScanner(stderr)
			for errScanner.Scan() {
				logger.Error("StdIO error", slog.String("err", errScanner.Text()))
			}
		}()

		cliStdIO := mcp.NewStdIO(out, in, mcp.WithStdIOLogger(logger))

		cli := mcp.NewClient(mcpClientInfo, cliStdIO, mcp.WithClientLogger(logger))
		mcpClients = append(mcpClients, cli)
	}

	return mcpClients, cmds
}
