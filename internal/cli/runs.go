package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/trusch/ghenkins/internal/store"
)

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "Manage workflow runs",
}

var runsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workflow runs",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := configFromContext(cmd.Context())
		st, err := store.Open(cfg.Store.Path)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer st.Close()

		runs, err := st.ListRuns(cmd.Context(), 50)
		if err != nil {
			return fmt.Errorf("list runs: %w", err)
		}

		fmt.Printf("%-8s  %-20s  %-25s  %-8s  %-20s  %-10s  %s\n",
			"ID", "WATCH", "REPO", "SHA", "WORKFLOW", "STATUS", "DURATION")
		for _, r := range runs {
			dur := "running"
			if r.FinishedAt != nil {
				dur = r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
			}
			id := r.ID
			if len(id) > 8 {
				id = id[:8]
			}
			sha := r.SHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Printf("%-8s  %-20s  %-25s  %-8s  %-20s  %-10s  %s\n",
				id, r.WatchName, r.Repo, sha, r.WorkflowName, string(r.Status), dur)
		}
		return nil
	},
}

var runsRetryCmd = &cobra.Command{
	Use:   "retry",
	Short: "Retry a workflow run",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("not yet implemented")
		return nil
	},
}

var runsCancelCmd = &cobra.Command{
	Use:   "cancel",
	Short: "Cancel a workflow run",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("not yet implemented")
		return nil
	},
}

func init() {
	runsCmd.AddCommand(runsListCmd, runsRetryCmd, runsCancelCmd)
	rootCmd.AddCommand(runsCmd)
}
