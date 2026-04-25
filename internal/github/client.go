package github

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gh "github.com/google/go-github/v67/github"
)

const commitsPerPage = 100

// Client wraps go-github calls used by wrklogr.
type Client struct {
	api *gh.Client
}

// NewClient constructs a GitHub API client with optional token auth.
func NewClient(token string, httpClient *http.Client) *Client {
	var client *gh.Client
	if httpClient != nil {
		client = gh.NewClient(httpClient)
	} else {
		client = gh.NewClient(nil)
	}

	if token != "" {
		client = client.WithAuthToken(token)
	}

	return &Client{api: client}
}

// ListCommits fetches commits for owner/repo and follows pagination.
func (c *Client) ListCommits(
	ctx context.Context,
	owner string,
	repo string,
	since *time.Time,
	until *time.Time,
) ([]*gh.RepositoryCommit, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner and repo are required")
	}

	opts := &gh.CommitsListOptions{
		ListOptions: gh.ListOptions{
			PerPage: commitsPerPage,
		},
	}
	if since != nil {
		opts.Since = *since
	}
	if until != nil {
		opts.Until = *until
	}

	commits := make([]*gh.RepositoryCommit, 0, commitsPerPage)
	for {
		pageCommits, resp, err := c.api.Repositories.ListCommits(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list commits for %s/%s: %w", owner, repo, err)
		}

		commits = append(commits, pageCommits...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return commits, nil
}
