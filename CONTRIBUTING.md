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

The maintainers' rules are in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
