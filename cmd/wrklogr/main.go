package main

import (
	"fmt"
	"os"

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
	root := &cobra.Command{
		Use:   "wrklogr",
		Short: "Build a worklog from GitHub commits across private repos",
		Long: `wrklogr fetches commits from configured GitHub repositories, clusters them
into work sessions, and emits Markdown (and optionally JSON) reports.`,
	}

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
