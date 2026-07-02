package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"pdbbot/internal/akane"
	"pdbbot/internal/llm"
	"pdbbot/internal/pdbapi"
	"pdbbot/internal/token"
)

func main() {
	statePath := flag.String("state", "state.json", "token state file")
	cfgPath := flag.String("config", "config.json", "config file")
	botStatePath := flag.String("bot-state", "akane_state.json", "bot state file")
	provider := flag.String("provider", "groq", "LLM provider (groq, openai)")
	keysDir := flag.String("keys-dir", "keys", "directory containing <provider>.key files")
	flag.Parse()

	cfg, err := akane.LoadConfig(*cfgPath)
	if err != nil {
		slog.Warn("config load failed, using defaults", "err", err)
		cfg = akane.DefaultConfig()
	}

	providerCfg, ok := cfg.Providers[*provider]
	if !ok {
		slog.Error("unknown provider", "provider", *provider, "available", fmt.Sprintf("%v", cfg.Providers))
		os.Exit(1)
	}

	keyFile := filepath.Join(*keysDir, *provider+".key")
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		slog.Error("api key load", "file", keyFile, "err", err)
		os.Exit(1)
	}
	apiKey := strings.TrimSpace(string(keyBytes))

	mgr, err := token.Load(*statePath, nil)
	if err != nil {
		slog.Error("token load", "err", err)
		os.Exit(1)
	}
	defer mgr.Close()

	apiClient := pdbapi.New(mgr)
	llmClient := llm.New(providerCfg.BaseURL, providerCfg.Model, apiKey, "")

	bot, err := akane.NewBot(cfg, apiClient, llmClient, *botStatePath)
	if err != nil {
		slog.Error("bot init", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := bot.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("bot exited", "err", err)
		os.Exit(1)
	}
}
