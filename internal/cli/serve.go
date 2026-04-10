package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/trusch/ghenkins/internal/daemon"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the ghenkins daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := configFromContext(cmd.Context())
		d, err := daemon.New(cfg, log.Logger)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return d.Run(ctx)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
