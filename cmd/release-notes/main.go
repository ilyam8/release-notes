package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-github/github"
	"github.com/kolide/kit/env"
	"golang.org/x/oauth2"

	"github.com/prologic/release-notes/notes"
)

type options struct {
	githubToken string
	startSHA    string
	endSHA      string
}

func parseOptions(args []string) (*options, error) {
	flagset := flag.NewFlagSet("release-notes", flag.ExitOnError)
	var (
		// flGitHubToken contains a personal GitHub access token. This is used to
		// scrape the commits of the Kubernetes repo.
		flGitHubToken = flagset.String(
			"github-token",
			env.String("GITHUB_TOKEN", ""),
			"A personal GitHub access token (required)",
		)

		// flStartSHA contains the commit SHA where the release note generation
		// begins.
		flStartSHA = flagset.String(
			"start-sha",
			env.String("START_SHA", ""),
			"The commit hash to start at",
		)

		// flEndSHA contains the commit SHA where the release note generation ends.
		flEndSHA = flagset.String(
			"end-sha",
			env.String("END_SHA", ""),
			"The commit hash to end at",
		)
	)

	// Parse the args.
	if err := flagset.Parse(args); err != nil {
		return nil, err
	}

	// The GitHub Token is required.
	if *flGitHubToken == "" {
		return nil, errors.New("GitHub token must be set via -github-token or $GITHUB_TOKEN")
	}

	// The start SHA is required.
	if *flStartSHA == "" {
		return nil, errors.New("The starting commit hash must be set via -start-sha or $START_SHA")
	}

	// The end SHA is required.
	if *flEndSHA == "" {
		return nil, errors.New("The ending commit hash must be set via -end-sha or $END_SHA")
	}

	return &options{
		githubToken: *flGitHubToken,
		startSHA:    *flStartSHA,
		endSHA:      *flEndSHA,
	}, nil
}

func main() {
	// Use the go-kit structured logger for logging. To learn more about structured
	// logging see: https://github.com/go-kit/kit/tree/master/log#structured-logging
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = level.NewInjector(logger, level.DebugValue())

	// Parse the CLI options and enforce required defaults
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		level.Error(logger).Log("msg", "error parsing options", "err", err)
		os.Exit(1)
	}

	// Create the GitHub API client
	ctx := context.Background()
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: opts.githubToken},
	))
	githubClient := github.NewClient(httpClient)

	// Fetch a list of fully-contextualized release notes
	level.Info(logger).Log("msg", "fetching all commits. this might take a while...")
	releaseNotes, err := notes.ListReleaseNotes(
		githubClient, logger, opts.startSHA, opts.endSHA,
		notes.WithContext(ctx),
		notes.WithOrg("netdata"),
		notes.WithRepo("netdata"),
	)
	if err != nil {
		level.Error(logger).Log("msg", "error generating release notes", "err", err)
		os.Exit(1)
	}
	level.Info(logger).Log("msg", "got the commits, performing rendering")

	doc, err := notes.CreateDocument(releaseNotes)
	if err != nil {
		level.Error(logger).Log("msg", "error creating release note document", "err", err)
		os.Exit(1)
	}

	if err := notes.RenderMarkdown(doc, os.Stdout); err != nil {
		level.Error(logger).Log("msg", "error rendering release note document to markdown", "err", err)
		os.Exit(1)
	}
}
