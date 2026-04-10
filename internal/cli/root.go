package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/trusch/ghenkins/internal/config"
)

type contextKey string

const configKey contextKey = "config"

var (
	cfgPath  string
	logLevel string
)

var rootCmd = &cobra.Command{
	Use:   "ghenkins",
	Short: "GitHub Actions workflow runner and monitor",
	Long:  "ghenkins watches GitHub repositories and triggers workflow runs based on PR and branch events.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level, err := zerolog.ParseLevel(logLevel)
		if err != nil {
			return fmt.Errorf("invalid log level %q: %w", logLevel, err)
		}
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).Level(level)

		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		ctx := context.WithValue(cmd.Context(), configKey, cfg)
		cmd.SetContext(ctx)
		return nil
	},
}

func configFromContext(ctx context.Context) *config.Config {
	cfg, _ := ctx.Value(configKey).(*config.Config)
	return cfg
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to config file")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (trace, debug, info, warn, error)")
}
