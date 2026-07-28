# Contributing

Thank you for considering a contribution.

## Before you write code

Read [`DECISIONS.md`](DECISIONS.md). It records what this project deliberately
does not do and why. A pull request that adds a plugin system, a general policy
engine, or TLS handling will be declined on those grounds no matter how well it
is written — please open an issue first if you want to argue a decision should
change.

There is no extension API by design. Extension happens by fork or by pull
request.

## Ground rules

- The default answer to "which dependency?" is the standard library. Adding one
  needs its own discussion in an issue first.
- Compliance is owed to the DSP 2025-1 specification and verified by the
  official TCK. When behavior is unclear, the spec and the TCK decide.
- English for code, comments, documentation, and commit messages.
- Every change needs a test. `go test ./...` and `make tck` must both pass.

## Contributor License Agreement

Every contributor must sign the CLA before a pull request can be merged. A bot
will comment on your first pull request with a link. This is required so that
each release can carry the Apache 2.0 conversion grant described in
[`LICENSE.md`](LICENSE.md).

## Getting started

```bash
make build   # build the binary
make test    # unit tests
make tck     # run the official DSP TCK and the compliance gate
```

`make tck` needs Docker.
