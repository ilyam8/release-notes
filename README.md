# Netdata Release Notes Generator

This repo contains a tool called `release-notes` and a set of library utilities at which aim to provide a simple and extensible set of tools for fetching, contextualizing, and rendering release notes for the [Netdata](https://github.com/netdata/netdata) repository.

## Install

The simplest way to install the `release-notes` CLI is via `go get`:

```
$ go get github.com/prologic/release-notes
```

This will install `release-notes` to `$GOPATH/bin/release-notes`. If you're new to Go, `$GOPATH` default to `~/go`, so look for the binary at `~/go/bin/release-notes`.

## Usage

To generate release notes for a commit range, run:

```
$ release-notes -start-sha 1be9200ba8e11dc81a2101d85a2725137d43f766 -end-sha $(git rev-parse HEAD) -github-token $GITHUB_TOKEN
```

## Building From Source

To build the `release-notes` tool, check out this repo to your `$GOPATH`:

```
git clone git@github.com:prologic/release-notes.git
```

Run the following from the root of the repository to install dependencies:

```
go build ./cmd/release-notes/...
```

Use the `-h` flag for help:

```
./cmd/release-notes/release-notes -h
```

Install the binary into your path:

```
go install ./cmd/release-notes/...
```
