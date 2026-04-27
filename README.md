# wrklogr

A CLI tool for generating worklogs from GitHub commits. Fetches commits from configured repositories, clusters them into work sessions, and outputs summary reports.

## Features

- **GitHub API Mode**: Fetch commits from remote GitHub repositories
- **Local Git Mode**: Scan local git repositories without needing a GitHub token
- **Session Clustering**: Automatically groups commits into work sessions based on time gaps
- **Flexible Date Filtering**: Support for YYYY-MM-DD and RFC3339 date formats
- **Author Filtering**: Filter commits to only show your work
- **Multi-Repo Support**: Aggregate commits across multiple repositories
- **Timezone Aware**: Report work sessions in your local timezone
- **Submodule Discovery**: Automatically discover and scan git repositories in subdirectories (monorepo support)
- **Month Summaries**: View aggregated work hours by month with grand total
- **Commit Details**: Show individual commits (SHA + message) within each session with `--show-commits`
- **Flag Summary**: Shows all flags used to generate the report for reproducibility

## Installation

### Prerequisites

- Go 1.22 or later

### Build from Source

```bash
git clone https://github.com/bean/wrklogr.git
cd wrklogr
go build -o wrklogr cmd/wrklogr/main.go
```

### Install to PATH

```bash
# Build and install to ~/go/bin (ensure it's in your PATH)
go build -o ~/go/bin/wrklogr cmd/wrklogr/main.go

# Or install to /usr/local/bin (requires sudo)
sudo cp wrklogr /usr/local/bin/
```

### Using the Install Script

```bash
./install.sh
```

The install script will automatically detect the best installation directory:
- Prefers `~/go/bin` (standard Go setup)
- Falls back to `/usr/local/bin` (requires sudo)

## Configuration

Create a `wrklogr.toml` file in your working directory or specify one with `--config`:

```toml
# Repositories to scan (format: owner/repo)
repos = ["bean-la/wrklogr", "other-org/repo-name"]

# Time gap that splits commits into separate sessions (e.g., "2h", "90m")
session_gap = "2h"

# IANA timezone for day bucketing in reports
timezone = "America/New_York"
```

## Usage

### Basic Report (GitHub API)

```bash
# Requires GITHUB_TOKEN or GH_TOKEN environment variable
wrklogr report
```

### Local Git Mode

```bash
# Scan current directory
wrklogr report --local

# Scan specific local repositories
wrklogr report --local --local-path ~/dev/project1 --local-path ~/dev/project2

# Automatically discover git repositories in subdirectories (great for monorepos)
# Output will show which packages are included in each session
wrklogr report --local --local-path ~/dev/monorepo --discover-submodules

# Control discovery depth (default: 3)
wrklogr report --local --local-path ~/dev/monorepo --discover-submodules --max-depth 2
```

### Filter by Date Range

```bash
# Since a specific date
wrklogr report --since 2025-03-01

# Between two dates
wrklogr report --since 2025-03-01 --until 2025-03-31

# Using RFC3339 timestamps
wrklogr report --since "2025-03-01T09:00:00Z"
```

### Filter by Author

```bash
# Only show your commits (requires GitHub API)
wrklogr report --me

# Only your commits in local mode
wrklogr report --local --me
```

### Override Session Gap

```bash
# Split sessions after 90 minutes of inactivity
wrklogr report --session-gap 90m
```

### Specify Timezone

```bash
# Report in a specific timezone
wrklogr report --timezone "America/Los_Angeles"
```

## Commands

### `wrklogr report`

Fetch commits and generate a worklog report.

**Flags:**
- `--config string` - Path to wrklogr TOML config file
- `--discover-submodules` - Automatically discover git repositories in subdirectories
- `--local` - Use local git history instead of GitHub API
- `--local-path strings` - Local git repo path(s) to scan (with --local)
- `--max-depth int` - Maximum depth for submodule discovery (default: 3)
- `--me` - Filter commits to the authenticated GitHub user
- `--session-gap string` - Override session split gap (e.g., "2h", "90m")
- `--since string` - Start date/time (RFC3339 or YYYY-MM-DD)
- `--timezone string` - IANA timezone for day bucketing
- `--token string` - GitHub token (defaults to GITHUB_TOKEN or GH_TOKEN)
- `--until string` - End date/time (RFC3339 or YYYY-MM-DD)
- `--show-commits` - Show individual commits (SHA + message) within each session

### `wrklogr version`

Print the embedded build version.

```bash
wrklogr version
```

### `wrklogr completion`

Generate autocompletion scripts for your shell.

```bash
# For bash
wrklogr completion bash > ~/.bashrc

# For zsh
wrklogr completion zsh > ~/.zshrc
```

## How It Works

1. **Fetch Commits**: Retrieves commits from configured repositories (via GitHub API or local git)
2. **Normalize**: Standardizes commit data (timestamp, message, repository)
3. **Cluster**: Groups commits into sessions based on the `session_gap` configuration
   - Commits within the gap period belong to the same session
   - A gap longer than the period starts a new session
4. **Bucket by Day**: Groups sessions by calendar day in the specified timezone
5. **Report**: Outputs summary statistics for each day

## Authentication

### GitHub API Mode

Set your GitHub token as an environment variable:

```bash
export GITHUB_TOKEN=your_token_here
# or
export GH_TOKEN=your_token_here
```

Or pass it directly:

```bash
wrklogr report --token your_token_here
```

### Local Git Mode

No authentication required. Works with any local git repository.

## Examples

### Weekly Worklog

```bash
wrklogr report --since 2025-03-01 --until 2025-03-07
```

Output:
```
bean-la/wrklogr: 15 commits
other-org/repo-name: 23 commits
total: 38 commits
2025-03-01: 4h (2 sessions)
  session 1: 2h [bean-la/wrklogr]
  session 2: 2h [other-org/repo-name]
2025-03-02: 6h (3 sessions)
  session 1: 1h [bean-la/wrklogr]
  session 2: 3h [other-org/repo-name]
  session 3: 2h [bean-la/wrklogr, other-org/repo-name]
2025-03-03: 2h (1 sessions)
  session 1: 2h [other-org/repo-name]
2025-03-04: 0h (0 sessions)
2025-03-05: 5h (2 sessions)
  session 1: 2h [bean-la/wrklogr]
  session 2: 3h [other-org/repo-name]

2025-03: 17h (2.1 days)

grand total: 17h (2.1 days)

flags used:
  --since 2025-03-01
  --until 2025-03-31
```

### Personal Commits Only

```bash
wrklogr report --me --since 2025-01-01
```

### Multi-Repo Local Scan

```bash
wrklogr report --local \
  --local-path ~/dev/project1 \
  --local-path ~/dev/project2 \
  --local-path ~/dev/project3
```

### Monorepo with Submodule Discovery

```bash
# Automatically discover all packages in a monorepo
wrklogr report --local --local-path ~/dev/monorepo --discover-submodules

# Limit discovery depth for better performance
wrklogr report --local --local-path ~/dev/monorepo --discover-submodules --max-depth 2
```

### Show Commits Per Session

```bash
# Show individual commits within each work session
wrklogr report --show-commits
```

Example output:
```
bean-la/wrklogr: 15 commits
other-org/repo-name: 23 commits
total: 38 commits
2025-03-01: 4h (2 sessions)
  session 1: 2h [bean-la/wrklogr]
    commit 1: abc12345 Fix login redirect
    commit 2: def67890 Update README formatting
    commit 3: ghi90123 Add unit tests for auth
  session 2: 2h [other-org/repo-name]
    commit 4: jkl23456 Refactor API endpoints
    commit 5: mno78901 Add rate limiting middleware
2025-03-02: 3h (1 sessions)
  session 1: 3h [bean-la/wrklogr, other-org/repo-name]
    commit 1: pqr34567 Bump dependencies
    commit 2: stu89012 Fix CORS issue
    commit 3: vwx12345 Update deployment config
```

Duplicate commits (same message, different SHA — e.g. from amends or rebases) are collapsed into a single line with a count:
```
2025-03-05: 1h (1 sessions)
  session 1: 1h [rainbow-mono]
    commit 1: bde605e9 hot patch fix for response id in nameConvId (x2)
    commit 2: dc37e4c2 Enhance conversation ID validation in userHelpers (x2)
```


Example output showing which packages are included in each session:
```
packages/ui: 12 commits
packages/api: 18 commits
packages/auth: 8 commits
total: 38 commits
2025-03-01: 5h (2 sessions)
  session 1: 3h [packages/api]
  session 2: 2h [packages/ui, packages/auth]
2025-03-02: 4h (1 sessions)
  session 1: 4h [packages/api, packages/auth]

2025-03: 9h (1.1 days)

grand total: 9h (1.1 days)

flags used:
  --local
  --local-path ~/dev/monorepo
  --discover-submodules
```

## Development

### Build

```bash
go build -o wrklogr cmd/wrklogr/main.go
```

### Run Tests

```bash
go test ./...
```

### Set Version at Build Time

```bash
go build -ldflags "-X main.version=v0.1.0" -o wrklogr cmd/wrklogr/main.go
```

## License

[Specify your license here]

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.