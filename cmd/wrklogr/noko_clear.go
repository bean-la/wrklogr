package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bean-la/wrklogr/internal/noko"
	"github.com/spf13/cobra"
)

func newNokoClearCmd() *cobra.Command {
	var sinceInput string
	var untilInput string
	var nokoTokenInput string
	var allUsers bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "noko-clear",
		Short: "Delete Noko entries for a date range (defaults to current user only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			token := strings.TrimSpace(nokoTokenInput)
			if token == "" {
				token = strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
			}
			if token == "" {
				return fmt.Errorf("noko token required: set --noko-token or NOKO_TOKEN")
			}

			since, err := parseDateBound(sinceInput, false)
			if err != nil {
				return fmt.Errorf("parse --since: %w", err)
			}
			until, err := parseDateBound(untilInput, false)
			if err != nil {
				return fmt.Errorf("parse --until: %w", err)
			}
			if since == nil {
				return fmt.Errorf("--since is required")
			}

			fromStr := since.Format("2006-01-02")
			toStr := ""
			if until != nil {
				toStr = until.Format("2006-01-02")
			} else {
				toStr = fromStr
			}

			client := noko.NewClient(token, nil)
			ctx := context.Background()

			var userIDs []int
			if !allUsers {
				me, err := client.GetCurrentUser(ctx)
				if err != nil {
					return fmt.Errorf("get current user: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "filtering to user: %s (id=%d)\n", me.Login, me.ID)
				userIDs = []int{me.ID}
			}

			entries, err := client.ListEntries(ctx, fromStr, toStr, userIDs)
			if err != nil {
				return fmt.Errorf("list entries: %w", err)
			}

			if len(entries) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no entries found for %s – %s\n", fromStr, toStr)
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "found %d entries (%s – %s)\n", len(entries), fromStr, toStr)

			if dryRun {
				for _, e := range entries {
					fmt.Fprintf(cmd.OutOrStdout(), "  would delete id=%d  %s  %dm\n", e.ID, e.Date, e.Minutes)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dry run — nothing deleted\n")
				return nil
			}

			deleted := 0
			for _, e := range entries {
				if err := client.DeleteEntry(ctx, e.ID); err != nil {
					fmt.Fprintf(os.Stderr, "delete id=%d: %v\n", e.ID, err)
					continue
				}
				deleted++
				fmt.Fprintf(cmd.OutOrStdout(), "deleted id=%d  %s  %dm\n", e.ID, e.Date, e.Minutes)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %d/%d entries\n", deleted, len(entries))
			return nil
		},
	}

	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date (YYYY-MM-DD), required")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date (YYYY-MM-DD), inclusive; defaults to --since")
	cmd.Flags().StringVar(&nokoTokenInput, "noko-token", "", "Noko API token (defaults to NOKO_TOKEN env var)")
	cmd.Flags().BoolVar(&allUsers, "all-users", false, "Delete entries for all users, not just the current user")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be deleted without actually deleting")

	return cmd
}
