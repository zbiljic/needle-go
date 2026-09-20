package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	needle "github.com/zbiljic/needle-go"
)

func main() {
	generation := flag.Int("generation", needle.DefaultGeneration, "model generation (2 or 3)")
	platform := flag.String("platform", "", "target platform; empty selects the current platform")
	cacheDir := flag.String("cache", "", "engine cache directory")
	list := flag.Bool("list", false, "list supported platforms")
	flag.Parse()

	if *list {
		for _, supported := range needle.SupportedPlatforms() {
			fmt.Println(supported)
		}
		return
	}

	path, err := needle.FetchEngine(context.Background(), needle.FetchOptions{
		Generation: *generation,
		Platform:   needle.Platform(*platform),
		CacheDir:   *cacheDir,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(path)
}
