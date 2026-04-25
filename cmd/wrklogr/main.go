package main

import (
	"fmt"
	"os"

	"github.com/bean/wrklogr/internal/config"
	"github.com/spf13/cobra"
)

// version is set at link time via:
//
//	go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:   "wrklogr",
		Short: "Build a worklog from GitHub commits across private repos",
		Long: `wrklogr fetches commits from configured GitHub repositories, clusters them
into work sessions, and emits Markdown (and optionally JSON) reports.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "version" {
				return nil
			}
			_, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to wrklogr TOML config")
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the embedded build version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}
