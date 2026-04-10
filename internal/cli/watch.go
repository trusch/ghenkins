package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Manage repository watches",
}

var watchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured watches",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := configFromContext(cmd.Context())
		if cfg == nil || len(cfg.Watches) == 0 {
			fmt.Println("no watches configured")
			return nil
		}
		fmt.Printf("%-20s  %s\n", "NAME", "REPO")
		for _, w := range cfg.Watches {
			fmt.Printf("%-20s  %s\n", w.Name, w.Repo)
		}
		return nil
	},
}

var watchAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a watch",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("not yet implemented")
		return nil
	},
}

var watchRmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Remove a watch",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("not yet implemented")
		return nil
	},
}

func init() {
	watchCmd.AddCommand(watchListCmd, watchAddCmd, watchRmCmd)
	rootCmd.AddCommand(watchCmd)
}
