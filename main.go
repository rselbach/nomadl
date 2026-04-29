package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var Version = "dev"

func main() {
	if err := run(); err != nil {
		log.SetFlags(0)
		log.Fatalf("nomadl: %v", err)
	}
}

func run() error {
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "print version and exit")

	config := appConfig{}
	flag.StringVar(&config.addr, "addr", getenv("NOMAD_ADDR", "http://127.0.0.1:4646"), "Nomad HTTP API address")
	flag.StringVar(&config.token, "token", os.Getenv("NOMAD_TOKEN"), "Nomad ACL token")
	flag.StringVar(&config.namespace, "namespace", os.Getenv("NOMAD_NAMESPACE"), "Nomad namespace")
	flag.StringVar(&config.region, "region", os.Getenv("NOMAD_REGION"), "Nomad region")
	flag.StringVar(&config.logType, "type", "both", "log stream to show: stdout, stderr, or both")
	flag.Int64Var(&config.tailBytes, "tail-bytes", 8*1024, "bytes of recent logs to read before following; 0 means future-only")
	flag.IntVar(&config.maxLines, "max-lines", 20000, "maximum log lines kept in memory")
	flag.DurationVar(&config.refreshInterval, "refresh", 15*time.Second, "service list refresh interval")
	flag.Parse()

	if showVersion {
		fmt.Println("nomadl", Version)
		return nil
	}

	switch config.logType {
	case "stdout", "stderr", "both":
	default:
		return fmt.Errorf("-type must be stdout, stderr, or both")
	}

	client, err := newNomadClient(nomadConfig{
		addr:      config.addr,
		token:     config.token,
		namespace: config.namespace,
		region:    config.region,
	})
	if err != nil {
		return err
	}
	if config.maxLines < 1 {
		return fmt.Errorf("-max-lines must be greater than zero")
	}
	if config.tailBytes < 0 {
		return fmt.Errorf("-tail-bytes must be zero or greater")
	}

	program := tea.NewProgram(newApp(client, config), tea.WithAltScreen())
	_, err = program.Run()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func getenv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
