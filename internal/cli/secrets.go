package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/trusch/ghenkins/internal/secrets"
	"golang.org/x/term"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage workflow secrets",
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <watch-name> <key>",
	Short: "Set a secret",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		watchName, key := args[0], args[1]
		fmt.Fprint(os.Stderr, "Enter secret value: ")
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		ks, err := secrets.New("ghenkins")
		if err != nil {
			return err
		}
		return ks.Set(cmd.Context(), watchName, key, string(value))
	},
}

var secretsGetCmd = &cobra.Command{
	Use:   "get <watch-name> <key>",
	Short: "Get a secret",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		watchName, key := args[0], args[1]
		ks, err := secrets.New("ghenkins")
		if err != nil {
			return err
		}
		value, err := ks.Get(cmd.Context(), watchName, key)
		if err != nil {
			return err
		}
		fmt.Println(value)
		return nil
	},
}

var secretsDeleteCmd = &cobra.Command{
	Use:   "delete <watch-name> <key>",
	Short: "Delete a secret",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		watchName, key := args[0], args[1]
		ks, err := secrets.New("ghenkins")
		if err != nil {
			return err
		}
		return ks.Delete(cmd.Context(), watchName, key)
	},
}

var secretsListCmd = &cobra.Command{
	Use:   "list <watch-name>",
	Short: "List secrets",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		watchName := args[0]
		ks, err := secrets.New("ghenkins")
		if err != nil {
			return err
		}
		keys, err := ks.List(cmd.Context(), watchName)
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Println(k)
		}
		return nil
	},
}

func init() {
	secretsCmd.AddCommand(secretsSetCmd, secretsGetCmd, secretsDeleteCmd, secretsListCmd)
	rootCmd.AddCommand(secretsCmd)
}
