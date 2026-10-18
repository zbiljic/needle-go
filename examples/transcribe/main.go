// Transcribe a file of little-endian float32 mono samples recorded at 16000 Hz,
// in one pass or, with -stream, live in one-second chunks.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math"
	"os"

	needle "github.com/zbiljic/needle-go"
)

// maxSamples bounds memory for this example; the engine itself accepts any length.
const maxSamples = 10 * 60 * needle.SpeechSampleRate

func main() {
	stream := flag.Bool("stream", false, "transcribe live in one-second chunks")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: go run ./examples/transcribe [-stream] clip.f32 (16000 Hz mono float32 PCM)")
	}
	file, err := os.Open(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSamples*4+1))
	if err != nil {
		log.Fatal(err)
	}
	if len(data) > maxSamples*4 || len(data)%4 != 0 {
		log.Fatal("PCM must contain whole float32 samples, at most ten minutes")
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
	encoder := json.NewEncoder(os.Stdout)
	if !*stream {
		transcript, err := speech.Transcribe(ctx, pcm, needle.AudioOptions{WordTimestamps: true})
		if err != nil {
			log.Fatal(err)
		}
		if err := encoder.Encode(transcript); err != nil {
			log.Fatal(err)
		}
		return
	}
	live, err := speech.Stream(needle.AudioOptions{})
	if err != nil {
		log.Fatal(err)
	}
	for start := 0; start < len(pcm); start += needle.SpeechSampleRate {
		step, err := live.Process(ctx, pcm[start:min(start+needle.SpeechSampleRate, len(pcm))])
		if err != nil {
			log.Fatal(err)
		}
		if err := encoder.Encode(step); err != nil {
			log.Fatal(err)
		}
	}
	tail, err := live.Stop(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := encoder.Encode(tail); err != nil {
		log.Fatal(err)
	}
}
