// Copyright 2017 The Kubernetes Authors All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package notes

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
)

const (
	CloseIssueKeywords = "Close|Closes|Closed|Fix|Fixes|Fixed|Resolve|Resolves|Resolved"
)

// ReleaseNote is the type that represents the total sum of all the information
// we've gathered about a single release note.
type ReleaseNote struct {
	// Commit is the SHA of the commit which is the source of this note. This is
	// also effectively a unique ID for release notes.
	Commit string `json:"commit"`

	// Text is the actual content of the release note
	Text string `json:"text"`

	// Markdown is the markdown formatted note
	Markdown string `json:"markdown"`

	// Author is the GitHub username of the commit author
	Author string `json:"author"`

	// AuthorUrl is the GitHub URL of the commit author
	AuthorUrl string `json:"author_url"`

	// PrUrl is a URL to the PR
	PrUrl string `json:"pr_url"`

	// PrNumber is the number of the PR
	PrNumber int `json:"pr_number"`

	// Areas is a list of the labels beginning with area/
	Areas []string `json:"areas,omitempty"`

	// Kinds is a list of the labels beginning with kind/
	Kinds []string `json:"kinds,omitempty"`

	// SIGs is a list of the labels beginning with sig/
	SIGs []string `json:"sigs,omitempty"`

	// Indicates whether or not a note will appear as a new feature
	Feature bool `json:"feature,omitempty"`

	// Indicates whether or not a note is duplicated across SIGs
	Duplicate bool `json:"duplicate,omitempty"`

	// ActionRequired indicates whether or not the release-note-action-required
	// label was set on the PR
	ActionRequired bool `json:"action_required,omitempty"`
}

// githubApiOption is a type which allows for the expression of API configuration
// via the "functional option" pattern.
// For more information on this pattern, see the following blog post:
// https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis
type githubApiOption func(*githubApiConfig)

// githubApiConfig is a configuration struct that is used to express optional
// configuration for GitHub API requests
type githubApiConfig struct {
	ctx    context.Context
	org    string
	repo   string
	branch string
}

// WithContext allows the caller to inject a context into GitHub API requests
func WithContext(ctx context.Context) githubApiOption {
	return func(c *githubApiConfig) {
		c.ctx = ctx
	}
}

// WithOrg allows the caller to override the GitHub organization for the API
// request.
func WithOrg(org string) githubApiOption {
	return func(c *githubApiConfig) {
		c.org = org
	}
}

// WithRepo allows the caller to override the GitHub repo for the API
// request.
func WithRepo(repo string) githubApiOption {
	return func(c *githubApiConfig) {
		c.repo = repo
	}
}

// WithBranch allows the caller to override the repo branch for the API
// request. By default, it is usually "master".
func WithBranch(branch string) githubApiOption {
	return func(c *githubApiConfig) {
		c.branch = branch
	}
}

// ListReleaseNotes produces a list of fully contextualized release notes
// starting from a given commit SHA and ending at starting a given commit SHA.
func ListReleaseNotes(
	client *github.Client,
	logger log.Logger,
	start,
	end string,
	opts ...githubApiOption,
) ([]*ReleaseNote, error) {
	commits, err := ListCommitsWithNotes(client, logger, start, end, opts...)
	if err != nil {
		return nil, err
	}

	dedupeCache := map[string]struct{}{}
	notes := []*ReleaseNote{}
	for _, commit := range commits {
		if commit.GetAuthor().GetLogin() == "netdatabot" {
			continue
		}

		note, err := ReleaseNoteFromCommit(commit, client, opts...)
		if err != nil {
			level.Error(logger).Log(
				"err", err,
				"msg", "error getting the release note from commit while listing release notes",
				"sha", commit.GetSHA(),
			)
			continue
		}

		if strings.TrimSpace(note.Text) == "NONE" {
			continue
		}

		if _, ok := dedupeCache[note.Text]; !ok {
			notes = append(notes, note)
			dedupeCache[note.Text] = struct{}{}
		}
	}

	return notes, nil
}

// NoteTextFromString returns the text of the release note given a string which
// may contain the commit message, the PR description, etc.
// This is generally the content inside the ```release-note ``` stanza.
func NoteTextFromString(s string) (string, error) {
	exps := []*regexp.Regexp{
		regexp.MustCompile("```release-note\\r\\n(?P<note>.+)"),
		regexp.MustCompile("```dev-release-note\\r\\n(?P<note>.+)"),
		regexp.MustCompile("```\\r\\n(?P<note>.+)\\r\\n```"),
		regexp.MustCompile("```release-note\n(?P<note>.+)\n```"),
	}

	for _, exp := range exps {
		match := exp.FindStringSubmatch(s)
		if len(match) == 0 {
			continue
		}
		result := map[string]string{}
		for i, name := range exp.SubexpNames() {
			if i != 0 && name != "" {
				result[name] = match[i]
			}
		}
		note := strings.TrimRight(result["note"], "\r")
		note = stripActionRequired(note)
		note = stripStar(note)
		return note, nil
	}

	return "", errors.New("no matches found when parsing note text from commit string")
}

// ReleaseNoteFromCommit produces a full contextualized release note given a
// GitHub commit API resource.
func ReleaseNoteFromCommit(commit *github.RepositoryCommit, client *github.Client, opts ...githubApiOption) (*ReleaseNote, error) {
	pr, err := PRFromCommit(client, commit, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "error parsing release note from commit %s", commit.GetSHA())
	}

	var issue *github.Issue

	issues, err := IssueNumbersFromCommit(commit)
	if err == nil {
		fmt.Fprintf(os.Stderr, "issues: %#v\n", issues)

		if len(issues) > 0 {
			issue, err = GetIssue(client, issues[0], opts...)
			if err != nil {
				return nil, errors.Wrapf(err, "error prasing release note from commit %s", commit.GetSHA())
			}
		}
		fmt.Fprintf(os.Stderr, "issue: #%v\n", issue)
	}

	/* XXX: Disabled for now since we don't add release notes to commits (yet)
	text, err := NoteTextFromString(pr.GetBody())
	if err != nil {
		return nil, err
	}
	*/

	scanner := bufio.NewScanner(strings.NewReader(commit.GetCommit().GetMessage()))
	scanner.Scan()
	text := scanner.Text()
	exp := regexp.MustCompile(`\(#(?P<number>\d+)\)`)
	text = exp.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)

	var (
		areas     []string
		isFeature bool
	)

	if HasString(StringsWithPrefix(GetPRLabels(pr), "kind/"), "feature") {
		isFeature = true
	} else if issue != nil && !HasString(GetIssueLabels(issue), "bug") {
		isFeature = true
	} else {
		isFeature = false
	}

	areas = StringsWithPrefix(GetPRLabels(pr), "area/")
	if issue != nil && len(areas) == 0 {
		areas = StringsWithPrefix(GetIssueLabels(issue), "area/")
	}

	author := pr.GetUser().GetLogin()
	authorUrl := fmt.Sprintf("https://github.com/%s", author)
	prUrl := fmt.Sprintf("https://github.com/netdata/netdata/pull/%d", pr.GetNumber())
	IsFeature := isFeature
	IsDuplicate := false
	sigsListPretty := prettifySigList(StringsWithPrefix(GetPRLabels(pr), "sig/"))
	noteSuffix := ""

	if IsActionRequired(pr) || IsFeature {
		if sigsListPretty != "" {
			noteSuffix = fmt.Sprintf("Courtesy of %s", sigsListPretty)
		}
	} else if len(StringsWithPrefix(GetPRLabels(pr), "sig/")) > 1 {
		IsDuplicate = true
	}
	markdown := fmt.Sprintf("%s ([#%d](%s), [@%s](%s))", text, pr.GetNumber(), prUrl, author, authorUrl)

	if noteSuffix != "" {
		markdown = fmt.Sprintf("%s %s", markdown, noteSuffix)
	}

	return &ReleaseNote{
		Commit:         commit.GetSHA(),
		Text:           text,
		Markdown:       markdown,
		Author:         author,
		AuthorUrl:      authorUrl,
		PrUrl:          prUrl,
		PrNumber:       pr.GetNumber(),
		SIGs:           StringsWithPrefix(GetPRLabels(pr), "sig/"),
		Kinds:          StringsWithPrefix(GetPRLabels(pr), "kind/"),
		Areas:          areas,
		Feature:        IsFeature,
		Duplicate:      IsDuplicate,
		ActionRequired: IsActionRequired(pr),
	}, nil
}

// ListCommits lists all commits starting from a given commit SHA and ending at
// a given commit SHA.
func ListCommits(client *github.Client, start, end string, opts ...githubApiOption) ([]*github.RepositoryCommit, error) {
	c := configFromOpts(opts...)

	startCommit, _, err := client.Git.GetCommit(c.ctx, c.org, c.repo, start)
	if err != nil {
		return nil, err
	}

	endCommit, _, err := client.Git.GetCommit(c.ctx, c.org, c.repo, end)
	if err != nil {
		return nil, err
	}

	clo := &github.CommitsListOptions{
		SHA:   c.branch,
		Since: *startCommit.Committer.Date,
		Until: *endCommit.Committer.Date,
		ListOptions: github.ListOptions{
			Page:    1,
			PerPage: 100,
		},
	}

	commits, resp, err := client.Repositories.ListCommits(c.ctx, c.org, c.repo, clo)
	if err != nil {
		return nil, err
	}
	clo.ListOptions.Page++

	for clo.ListOptions.Page <= resp.LastPage {
		commitPage, _, err := client.Repositories.ListCommits(c.ctx, c.org, c.repo, clo)
		if err != nil {
			return nil, err
		}
		for _, commit := range commitPage {
			commits = append(commits, commit)
		}
		clo.ListOptions.Page++
	}

	return commits, nil
}

// ListCommitsWithNotes list commits that have release notes starting from a
// given commit SHA and ending at a given commit SHA. This function is similar
// to ListCommits except that only commits with tagged release notes are
// returned.
func ListCommitsWithNotes(
	client *github.Client,
	logger log.Logger,
	start,
	end string,
	opts ...githubApiOption,
) ([]*github.RepositoryCommit, error) {
	filteredCommits := []*github.RepositoryCommit{}

	commits, err := ListCommits(client, start, end, opts...)
	fmt.Fprintf(os.Stderr, "no. of commits: %d\n", len(commits))
	if err != nil {
		return nil, err
	}

	for _, commit := range commits {
		pr, err := PRFromCommit(client, commit, opts...)
		if err != nil {
			if err.Error() == "no matches found when parsing PR from commit" {
				fmt.Fprintf(os.Stderr, "no PR found for %s\n", commit.GetCommit().GetMessage())
				continue
			}
		}

		// exclusionFilters is a list of regular expressions that match commits that
		// do NOT contain release notes. Notably, this is all of the variations of
		// "release note none" that appear in the commit log.
		exclusionFilters := []string{
			"```release-note\\r\\nNONE",
			"```release-note\\r\\n\\s+NONE",
			"```release-note\\r\\nNONE",
			"```release-note\\r\\n\"NONE\"",
			"```release-note\\r\\nNone",
			"```release-note\\r\\nnone",
			"```release-note\\r\\nN/A",
			"```release-note\\r\\n\\r\\n```",
			"```release-note\\r\\n```",
			"/release-note-none",
			"\\r\\n\\r\\nNONE",
			"```NONE\\r\\n```",
			"```release-note \\r\\nNONE\\r\\n```",
			"NONE\\r\\n```",
			"\\r\\nNone",
			"\\r\\nNONE\\r\\n",
		}

		excluded := false

		for _, filter := range exclusionFilters {
			match, err := regexp.MatchString(filter, pr.GetBody())
			if err != nil {
				return nil, err
			}
			if match {
				excluded = true
				break
			}
		}

		if excluded {
			fmt.Fprintf(os.Stderr, "excluding %s\n", commit.GetCommit().GetMessage())
			continue
		}

		// Similarly, now that the known not-release-notes are filtered out, we can
		// use some patterns to find actual release notes.
		inclusionFilters := []string{
			".*",
			"release-note",
			"Does this PR introduce a user-facing change?",
		}

		for _, filter := range inclusionFilters {
			match, err := regexp.MatchString(filter, pr.GetBody())
			if err != nil {
				return nil, err
			}
			if match {
				filteredCommits = append(filteredCommits, commit)
			}
		}
	}

	return filteredCommits, nil
}

// IssueNumbersFromCommit return slice of API Issue Request structs given a commit
// struct. This is useful for going from a commit log to the associated issues
// it either addresses, closes or fixes (which contains useful info such
// the type of issue the Comit/PR was fixing/closing as well as labels specific
// to the issue and not necessarily the pull request).
func IssueNumbersFromCommit(commit *github.RepositoryCommit) ([]int, error) {
	exp := regexp.MustCompile(`(?i)(` + CloseIssueKeywords + `) #?(?P<number>\d+)`)
	matches := exp.FindAllStringSubmatch(*commit.Commit.Message, -1)
	if len(matches) == 0 {
		return nil, errors.New("no matches found when parsing Issues from commit")
	}

	var issues []int
	for i, name := range exp.SubexpNames() {
		if i != 0 && name != "" {
			number, err := strconv.Atoi(matches[0][i])
			if err != nil {
				return nil, err
			}
			issues = append(issues, number)
		}
	}

	return issues, nil
}

// GetIssue return an API Issue struct given an issue number.
func GetIssue(client *github.Client, number int, opts ...githubApiOption) (*github.Issue, error) {
	c := configFromOpts(opts...)
	issue, _, err := client.Issues.Get(c.ctx, c.org, c.repo, number)
	return issue, err
}

// PRFromCommit return an API Pull Request struct given a commit struct. This is
// useful for going from a commit log to the PR (which contains useful info such
// as labels).
func PRFromCommit(client *github.Client, commit *github.RepositoryCommit, opts ...githubApiOption) (*github.PullRequest, error) {
	c := configFromOpts(opts...)

	// Thankfully k8s-merge-robot commits the PR number consistently. If this ever
	// stops being true, this definitely won't work anymore.
	exp := regexp.MustCompile(`\(#(?P<number>\d+)\)`)
	match := exp.FindStringSubmatch(*commit.Commit.Message)
	if len(match) == 0 {
		return nil, errors.New("no matches found when parsing PR from commit")
	}
	result := map[string]string{}
	for i, name := range exp.SubexpNames() {
		if i != 0 && name != "" {
			result[name] = match[i]
		}
	}
	number, err := strconv.Atoi(result["number"])
	if err != nil {
		return nil, err
	}

	// Given the PR number that we've now converted to an integer, get the PR from
	// the API
	pr, _, err := client.PullRequests.Get(c.ctx, c.org, c.repo, number)
	return pr, err
}

// GetIssueLabels is a helper for fetching all labels on an Issue
func GetIssueLabels(issue *github.Issue) []string {
	labels := []string{}
	for _, label := range issue.Labels {
		labels = append(labels, *label.Name)
	}
	return labels
}

// GetPRLabels is a helper for fetching all labels on a PR
func GetPRLabels(pr *github.PullRequest) []string {
	labels := []string{}
	for _, label := range pr.Labels {
		labels = append(labels, *label.Name)
	}
	return labels
}

// StringsWithPrefix is a helper for returning all strings that start with a
// prefix. This is useful for determining issue and pr labels and categorizing
// the type of issue or area an issue or pr covers.
func StringsWithPrefix(xs []string, prefix string) []string {
	ys := []string{}
	for _, x := range xs {
		if strings.HasPrefix(x, prefix) {
			ys = append(ys, strings.TrimPrefix(x, prefix))
		}
	}
	return ys
}

// IsActionRequired indicates whether or not the release-note-action-required
// label was set on the PR.
func IsActionRequired(pr *github.PullRequest) bool {
	for _, label := range pr.Labels {
		if *label.Name == "release-note-action-required" {
			return true
		}
	}
	return false
}

// filterCommits is a helper that allows you to filter a set of commits by
// applying a set of regular expressions over the commit messages. If include is
// true, only commits that match at least one expression are returned. If include
// is false, only commits that match 0 of the expressions are returned.
func filterCommits(
	client *github.Client,
	logger log.Logger,
	commits []*github.RepositoryCommit,
	filters []string,
	include bool,
	opts ...githubApiOption,
) ([]*github.RepositoryCommit, error) {
	filteredCommits := []*github.RepositoryCommit{}
	for _, commit := range commits {
		body := commit.GetCommit().GetMessage()
		if commit.GetAuthor().GetLogin() == "k8s-merge-robot" {
			pr, err := PRFromCommit(client, commit, opts...)
			if err != nil {
				level.Info(logger).Log(
					"msg", "error getting PR from k8s-merge-robot commit",
					"err", err,
					"sha", commit.GetSHA(),
				)
				continue
			}
			body = pr.GetBody()
		}

		skip := false
		for _, filter := range filters {
			match, err := regexp.MatchString(filter, body)
			if err != nil {
				return nil, err
			}
			if match && !include || !match && include {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		filteredCommits = append(filteredCommits, commit)
	}

	return filteredCommits, nil
}

// configFromOpts is an internal helper for turning a set of functional options
// into a populated *githubApiConfig struct with consistent defaults.
func configFromOpts(opts ...githubApiOption) *githubApiConfig {
	c := &githubApiConfig{
		ctx:    context.Background(),
		org:    "netdata",
		repo:   "netdata",
		branch: "master",
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

func stripActionRequired(note string) string {
	expressions := []string{
		`(?i)\[action required\]\s`,
		`(?i)action required:\s`,
	}

	for _, exp := range expressions {
		re := regexp.MustCompile(exp)
		note = re.ReplaceAllString(note, "")
	}

	return note
}

func stripStar(note string) string {
	re := regexp.MustCompile(`(?i)\*\s`)
	return re.ReplaceAllString(note, "")
}

func HasString(a []string, x string) bool {
	for _, n := range a {
		if x == n {
			return true
		}
	}
	return false
}
