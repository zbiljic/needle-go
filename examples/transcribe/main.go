// Transcribe a file of little-endian float32 mono samples recorded at 16000 Hz.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"math"
	"os"

	needle "github.com/zbiljic/needle-go"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: go run ./examples/transcribe clip.f32 (16000 Hz mono float32 PCM)")
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, needle.MaxSpeechSamples*4+1))
	if err != nil {
		log.Fatal(err)
	}
	if len(data) > needle.MaxSpeechSamples*4 || len(data)%4 != 0 {
		log.Fatal("PCM must contain whole float32 samples, at most 30 seconds")
	}
	pcm := make([]float32, len(data)/4)
	for i := range pcm {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	ctx := context.Background()
	speech, err := needle.NewWhistle(ctx, needle.WhistleConfig{})
	if err != nil {
		log.Fatal(err)
	}
	transcript, err := speech.Transcribe(ctx, pcm, needle.AudioOptions{WordTimestamps: true})
	if err != nil {
		log.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(transcript); err != nil {
		log.Fatal(err)
	}
}
