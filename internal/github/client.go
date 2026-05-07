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

// ListOrgRepos returns all repository full names (owner/repo) for an org.
func (c *Client) ListOrgRepos(ctx context.Context, org string) ([]string, error) {
	var all []string
	opts := &gh.RepositoryListByOrgOptions{
		Type:        "all",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	for {
		repos, resp, err := c.api.Repositories.ListByOrg(ctx, org, opts)
		if err != nil {
			return nil, fmt.Errorf("list repos for org %s: %w", org, err)
		}
		for _, r := range repos {
			if r.GetFullName() != "" {
				all = append(all, r.GetFullName())
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// HasCommitsByAuthor checks if a repo has at least one commit from an author
// in the given time range. Returns the first matching commit or nil.
func (c *Client) HasCommitsByAuthor(
	ctx context.Context,
	owner, repo string,
	author string,
	since, until *time.Time,
) (*gh.RepositoryCommit, error) {
	opts := &gh.CommitsListOptions{
		Author:      author,
		ListOptions: gh.ListOptions{PerPage: 1},
	}
	if since != nil {
		opts.Since = *since
	}
	if until != nil {
		opts.Until = *until
	}
	commits, _, err := c.api.Repositories.ListCommits(ctx, owner, repo, opts)
	if err != nil {
		return nil, err
	}
	if len(commits) > 0 {
		return commits[0], nil
	}
	return nil, nil
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
