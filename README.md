# needle-go

`needle-go` is a Go library for running the
[Cactus Needle](https://github.com/cactus-compute/needle) on-device
tool-calling model.

It provides:

- CGO-free native engine loading
- Needle 2 and Needle 3, with Needle 3 as the default
- automatic, checksum-verified engine and base-weight downloads
- high-level and manual completion loops
- typed Go tool handlers
- structured response extraction

> This project is in early development and its API may change.

## Install

Requires Go 1.25 or newer.

```sh
go get github.com/zbiljic/needle-go
```

Engine builds are available for macOS, Linux (glibc and musl), and Windows on
amd64 and arm64.

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"

	needle "github.com/zbiljic/needle-go"
)

type weatherArguments struct {
	City string `json:"city" jsonschema:"description=City whose weather should be returned."`
}

func main() {
	ctx := context.Background()
	weather := needle.NewTool(
		"get_weather",
		"Get the current weather for a city.",
		func(_ context.Context, arguments weatherArguments) (string, error) {
			return "Clear in " + arguments.City, nil
		},
	)

	agent, err := needle.New(ctx, needle.Config{Tools: []needle.Tool{weather}})
	if err != nil {
		log.Fatal(err)
	}
	response, err := agent.Run(
		ctx,
		"What is the weather in Lagos?",
		needle.DefaultMaxSteps,
		needle.DefaultMaxNewTokens,
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Results)
}
```

By default, `needle.New` downloads and caches the pinned Needle 3 engine
(3.0.1) and its `needle3.cact` base weights (~35 MB) from the
[Needle 3 model repository](https://huggingface.co/Cactus-Compute/needle3).
Subsequent runs use the cached files. No Python installation is required.

## Model generations

Use `Generation: 2` for the embedded Needle 2 model (engine 2.0.4):

```go
agent, err := needle.New(ctx, needle.Config{
	Generation: 2,
	Tools:      tools,
})
```

`Generation: 0` and `Generation: 3` select Needle 3. A custom `WeightsPath`
selects the compatible engine from the `.cact` header, taking precedence over
`Generation`. Supplying `WeightsPath` makes `Confidence == nil`, even if the
file contains base weights. Automatically loaded base weights retain the
engine's confidence scores.

Both generations can run in the same process, with separate native runtimes.
Within one generation, switching agents reinitializes the active conversation.
After loading custom Needle 2 weights, returning to its embedded base model
requires a separate process. Needle 3 can reload its base archive.

Use `LibraryPath` for an existing, trusted engine matching the selected
generation. When `LibraryPath` is empty, environment overrides are
`NEEDLE2_LIB_PATH` and `NEEDLE3_LIB_PATH`; the legacy `NEEDLE_LIB_PATH` is a
fallback for Needle 2 only. A Needle 3 library override still downloads missing
base weights unless `WeightsPath` is supplied.

`FetchEngine` prepares the library and required base weights for offline use.
`FetchOptions.Generation` selects the generation. Default caches are under
`~/.cache/cactus-needle/<engine-version>`; `CacheDir` selects an exact directory.
V2 uses `libneedle.*`, while v3 uses `libneedle3.*`, so the generations can share
a custom cache directory for the same platform. For offline use, fetch for the
target platform and generation, preserve the `.sha256` marker files, and use
the same cache directory at runtime. `CachedEngine` checks only for the library.

## Native engine diagnostics

The optional `needlez` command can fetch the engine and verify that it loads:

```sh
go run github.com/zbiljic/needle-go/cmd/needlez@latest fetch
go run github.com/zbiljic/needle-go/cmd/needlez@latest doctor --smoke
go run github.com/zbiljic/needle-go/cmd/needlez@latest test
```

To run the checks against Needle 2 from a repository checkout:

```sh
go run ./cmd/needlez fetch --generation 2
go run ./cmd/needlez doctor --generation 2 --smoke
go run ./cmd/needlez test --generation 2
```

Responses preserve `suppressed_calls` for inspection. `Run` executes only
`function_calls`; it never executes suppressed calls automatically.

## Examples

- [Typed tool calling](examples/tool-call)
- [Structured extraction](examples/structured-extraction)
- [Manual completion loop](examples/manual-loop)
- [Engine downloads](examples/fetch-engine)

## License

Licensed under the [MIT License](LICENSE).
