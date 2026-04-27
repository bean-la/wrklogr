package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed wrklogr-monthly.yaml
var defaultWorkflowYAML []byte

func newInstallWorkflowCmd() *cobra.Command {
	var workflowPath string

	cmd := &cobra.Command{
		Use:   "install-workflow",
		Short: "Install the monthly worklog GitHub Actions workflow into a repo",
		Long: `Create or update .github/workflows/wrklogr-monthly.yaml so that GitHub
		Actions generates a monthly worklog report and publishes it to the wiki.

		The workflow runs daily at 23:59 UTC and continuously updates the current
		month's wiki page with the latest data. It collects all contributors in the
		month-to-date range, sections the report per author, and pushes the result
		as a single monthly wiki page (e.g. worklog-2025-03.md).

		Prerequisites:
		  - gh CLI installed and authenticated (gh auth login)
		  - The repository must have the wiki enabled (Settings > Features > Wikis)
		  - wrklogr.toml must list the repos to scan
		  - For cross-org repos, create a fine-grained PAT with "Contents: Read"
		    on those repos and save it as the WORKLOG_TOKEN secret.

		After installation, commit and push the workflow file, then test with:
		  gh workflow run wrklogr-monthly.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installWorkflow(cmd, workflowPath)
		},
	}

	cmd.Flags().StringVar(&workflowPath, "workflow-path", ".", "Path to the repository root where the workflow should be installed")

	return cmd
}

func installWorkflow(cmd *cobra.Command, repoRoot string) error {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", repoRoot, err)
	}

	// Check it's a git repo
	gitDir := filepath.Join(absRoot, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("%q is not a git repository (no .git directory)", absRoot)
	}

	// Verify gh is available and authenticated
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI is required but not found in PATH; install from https://cli.github.com/")
	}

	if out, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		return fmt.Errorf("gh CLI is not authenticated; run 'gh auth login' first:\n%s",
			strings.TrimSpace(string(out)))
	}

	// Ensure .github/workflows exists
	workflowDir := filepath.Join(absRoot, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", workflowDir, err)
	}

	workflowFile := filepath.Join(workflowDir, "wrklogr-monthly.yaml")

	// Check if already exists
	exists := false
	if info, err := os.Stat(workflowFile); err == nil && !info.IsDir() {
		exists = true
	}

	if err := os.WriteFile(workflowFile, defaultWorkflowYAML, 0644); err != nil {
		return fmt.Errorf("write %s: %w", workflowFile, err)
	}

	if exists {
		fmt.Fprintf(cmd.OutOrStdout(), "✅ Updated existing workflow at %s\n", workflowFile)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "✅ Created workflow at %s\n", workflowFile)
	}

	// Try to derive owner/repo from git remote for the instructions
	ownerRepo := ""
	if remoteOut, err := exec.Command("git", "-C", absRoot, "config", "--get", "remote.origin.url").Output(); err == nil {
		ownerRepo = parseGitHubRemote(strings.TrimSpace(string(remoteOut)))
	}

	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "─── Next steps ──────────────────────────────────────────────")
	fmt.Fprintln(cmd.OutOrStdout())

	if ownerRepo != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  1. Make sure wrklogr.toml lists the repos to scan.\n")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "  2. Enable the wiki if it's off:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "       https://github.com/%s/settings\n", ownerRepo)
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "  3. (Optional) For cross-org repo access, create a\n")
		fmt.Fprintf(cmd.OutOrStdout(), "     fine-grained PAT with \"Contents: Read\" on the target\n")
		fmt.Fprintf(cmd.OutOrStdout(), "     repos and save it as a secret:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "       https://github.com/%s/settings/secrets/actions\n", ownerRepo)
		fmt.Fprintf(cmd.OutOrStdout(), "       Secret name: WORKLOG_TOKEN\n")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "  4. Commit and push the workflow:\n")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "       git add .github/workflows/wrklogr-monthly.yaml\n")
		fmt.Fprintf(cmd.OutOrStdout(), "       git commit -m \"ci: add monthly worklog workflow\"\n")
		fmt.Fprintf(cmd.OutOrStdout(), "       git push\n")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "  5. Test it manually:\n")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "       gh workflow run wrklogr-monthly.yaml -R %s\n", ownerRepo)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  1. Commit and push the workflow file, then test:\n")
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintf(cmd.OutOrStdout(), "       gh workflow run wrklogr-monthly.yaml\n")
	}

	return nil
}

// parseGitHubRemote extracts "owner/repo" from common remote URL formats.
func parseGitHubRemote(remote string) string {
	remote = strings.TrimSuffix(remote, ".git")
	remote = strings.TrimPrefix(remote, "https://github.com/")
	remote = strings.TrimPrefix(remote, "git@github.com:")
	return remote
}
