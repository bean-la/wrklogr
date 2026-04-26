package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v67/github"
)

const commitsPerPage = 100

// Client wraps go-github calls used by wrklogr.
type Client struct {
	api *gh.Client
}

// ViewerIdentity is the authenticated GitHub user's identity for --me filtering.
type ViewerIdentity struct {
	Login  string
	Emails map[string]struct{}
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

// GetViewerIdentity fetches current user login and available emails.
func (c *Client) GetViewerIdentity(ctx context.Context) (*ViewerIdentity, error) {
	user, _, err := c.api.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}

	login := strings.ToLower(strings.TrimSpace(user.GetLogin()))
	if login == "" {
		return nil, fmt.Errorf("current user login is empty")
	}

	emailSet := map[string]struct{}{}
	emails, _, err := c.api.Users.ListEmails(ctx, nil)
	if err == nil {
		for _, e := range emails {
			addr := strings.ToLower(strings.TrimSpace(e.GetEmail()))
			if addr != "" {
				emailSet[addr] = struct{}{}
			}
		}
	}

	return &ViewerIdentity{
		Login:  login,
		Emails: emailSet,
	}, nil
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
