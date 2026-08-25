# needle-go

`needle-go` is a Go library for running the
[Cactus Needle](https://github.com/cactus-compute/needle) on-device
tool-calling model.

It provides:

- CGO-free native engine loading
- automatic, checksum-verified engine downloads
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

`needle.New` downloads and caches the matching native engine from the
[Needle 2 model repository](https://huggingface.co/Cactus-Compute/needle2) when
one is not already available. Set `NEEDLE_LIB_PATH` to use an existing engine
library instead.

## Native engine diagnostics

The optional `needlez` command can fetch the engine and verify that it loads:

```sh
go run github.com/zbiljic/needle-go/cmd/needlez@latest fetch
go run github.com/zbiljic/needle-go/cmd/needlez@latest doctor --smoke
go run github.com/zbiljic/needle-go/cmd/needlez@latest test
```

## Examples

- [Typed tool calling](examples/tool-call)
- [Structured extraction](examples/structured-extraction)
- [Manual completion loop](examples/manual-loop)
- [Engine downloads](examples/fetch-engine)

## License

Licensed under the [MIT License](LICENSE).
