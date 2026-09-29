# Contributing to OwnGit

Contributions are welcome. Fork the repository on [GitHub](https://github.com/juliankang4/owngit), change the code to fix a bug you found or an open issue, and open a pull request that says what it fixes. GitHub runs the checks on your pull request; fix what they report if you can, or say so in the pull request and we will help. The maintainers take care of documentation, translations, and anything else before merging.

Please report security problems privately as described in [SECURITY.md](SECURITY.md), not in a pull request or a public issue.

## Build and run

To try your change on your own computer first (this is optional), you need Go 1.27 or newer and Git. In your copy of the repository, run:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open --state-dir /tmp/owngit-dev-state
go test ./...
```

On Windows, build `bin/owngit.exe` instead. `--state-dir` keeps this copy away from any real OwnGit installation.

The maintainers' own rules are in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
