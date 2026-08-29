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

## How these documents are written

These rules govern the prose in this repository, not only its code. They were
being restated from scratch in each implementation plan under
`docs/superpowers/`, which are dated artifacts nobody may rewrite — so a clause
was lost that way once. They live here instead, where they can be corrected.

- **Never write a count into a comment or into prose.** Rewrite without the
  number: "three call sites" and "seven places" go stale the day someone adds
  the next one. Name the kind of thing instead. The one exception is a number
  naming a fixed pair the design itself defines.
- **Every documentation edit names the code fact it was checked against** — the
  function, file, or test that was read to confirm it, so a reader can check
  rather than trust.
- **A comment must be true of the code it sits next to**, including not
  describing behaviour a later change adds.
- **Dated artifacts are annotated, never rewritten.** `docs/goal-gap-analysis.md`
  and everything under `docs/superpowers/` record what was true on their date.
  When one of their statements stops being true, append a dated bracket beside
  it and leave the original sentence standing. `DECISIONS.md` is the exception
  and says so in its own preamble: it is corrected in place, with the amendment
  marked.

This repository also uses ordinary English words with narrower local meanings.
[`docs/glossary.md`](docs/glossary.md) lists them and names where each meaning
is fixed. Read it before arguing about one.

## Contributor License Agreement

Every contributor must sign the CLA before a pull request can be merged. A bot
will comment on your first pull request with a link. This is required so that
each release can carry the Apache 2.0 conversion grant described in
[`LICENSE.md`](LICENSE.md).

## Getting started

```bash
make build       # build the binary
make test        # unit tests
make tck         # run the official DSP TCK and the compliance gate
make quickstart  # run docs/quickstart.md end to end
make demo        # two connectors under Compose, including a resumed transfer
```

`make tck` and `make demo` need Docker. `make quickstart` does not — it runs
two connectors as native processes, and it is the same document a reader
follows, so a change that breaks it breaks their first hour. Edit
`docs/quickstart.md` rather than the script it generates.
