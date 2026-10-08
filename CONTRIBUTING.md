# Contributing to OwnGit

To send a change, fork the repository on [GitHub](https://github.com/juliankang4/owngit), fix a bug or an open issue, and open a pull request that says what it fixes. GitHub runs checks on the pull request. Fix what they report, or say in the pull request that you need help. The maintainers handle documentation and translations before merging.

Report security problems privately as described in [SECURITY.md](SECURITY.md), not in a pull request or public issue.

## Build and run

To try a change locally (optional), you need Go 1.27 or newer and Git:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open --state-dir /tmp/owngit-dev-state
go test ./...
```

On Windows, build `bin/owngit.exe`. `--state-dir` keeps this copy apart from any real OwnGit installation.

Pull request CI rejects `time.Sleep` and `time.Since` in test files on every line the pull request adds or changes, including an existing call that is moved, reindented or realigned by gofmt: wait for an observed signal (a channel or callback), or use the code's own clock or option; use a generous context deadline to guard hangs instead of asserting elapsed time. For an intentional exception, add `//nolint:forbidigo // reason` on that line and replace `reason` with the test's need for the call.

The maintainers' rules are in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
