# Contributing

Thanks for helping. This page covers the development setup and how changes get in.

## Development setup

You need Go (the version in `go.mod`; the `go` command downloads it if yours is older) and `make`.

source-mihon is built with the SDK from [source-api](https://github.com/panelarr/source-api). Until source-api publishes tagged versions, `go.mod` points at a **sibling checkout** with a `replace` directive, so clone the repos next to each other:

```sh
git clone https://github.com/panelarr/source-api
git clone https://github.com/panelarr/mihon-source   # only for make e2e and the store protos
git clone https://github.com/panelarr/source-mihon
cd source-mihon
make setup   # installs pinned tools (buf, protoc plugins, golangci-lint, govulncheck) into ./bin
make check   # format check, vet, lint, the proto pin, govulncheck and tests: run this before every PR
make e2e     # end to end against a real mihon-source (needs a JDK 21)
make help    # lists every target
```

### mihon-source's protos

source-mihon calls mihon-source's store and extension admin API. Those protos are pinned in `third_party/mihon-source/` with the commit and checksums; `make pin-check` fails when they are edited. To take a newer version: `make sync-mihon-protos REF=…` then `make gen`, and commit both.

### No third-party extensions or stores

Never put real extension, store or site names in code, tests, fixtures or docs. Tests use made-up data; the end-to-end test uses mihon-source's own test extension and a made-up site.

## Working conventions

- **Tasks:** everything runs through `make`.
- **Branches:** `<type>/<scope>-<slug>`, e.g. `feat/stores-remove-unlisted`.
- **Commits and PR titles:** [Conventional Commits](https://www.conventionalcommits.org/): `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, `build`, `ci`. Add `!` for breaking changes. PRs are squash-merged, so the PR title becomes the commit message.
- **Reviews:** at least one approval and green checks before merge.
- **AI agents:** agents may open PRs and post reviews. Anything an agent posts from a person's account starts with a **⚠️ AI agent** line. Humans stay responsible for what they merge.
