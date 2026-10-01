# AVM Intelligence

AVM Intelligence discovers Azure Verified Modules, analyzes approved source
revisions with the released [IaCLens](https://github.com/Ledermayer/iaclens)
CLI, and publishes reviewed intelligence. This repository currently contains the
fixture-only factory skeleton.

The current pin is IaCLens `v0.1.0`, report schema 2. The skeleton validates
that pin and normalizes reports without GitHub write credentials, publication,
or semantic analysis. See [docs/decisions.md](docs/decisions.md) for the approved
event scope and [docs/TODOS.md](docs/TODOS.md) for the remaining factory work.

```sh
go test ./...
go run ./cmd/factory --show-toolchain
```
