# Speech

`NewWhistle` loads the pinned ~16.9 MB `whistle.cact` archive from
[Cactus-Compute/whistle](https://huggingface.co/Cactus-Compute/whistle) and uses the
Needle 3.1 engine. Speech weights are fetched only when requested; standalone
transcription does not require text weights.

## Transcription

```go
speech, err := needle.NewWhistle(ctx, needle.WhistleConfig{})
if err != nil {
	log.Fatal(err)
}
transcript, err := speech.Transcribe(ctx, pcm, needle.AudioOptions{
	Language:       "en",
	Keywords:       []string{"Paris", "Ada"},
	WordTimestamps: true,
})
```

Supply mono `[]float32` PCM at 16000 Hz, with finite samples in `[-1, 1]`, up to
30 seconds. Empty clips and silence return empty text and language. `Language`
is empty for detection, or `en`, `de`, `fr`, `es`, `it`, `nl` or `pl`. Transcripts
include `TTFTMS`, `DecodeTPS`, and optional words with start/end times in seconds
and probabilities. File decoding, microphone capture and resampling belong to
the caller; the [PCM example](../examples/transcribe) accepts little-endian float32
samples.

## Audio tool calling

To transcribe and produce tool calls in one native turn, pass a Needle 3 agent
created by `New`:

```go
response, err := speech.Complete(ctx, agent, pcm, needle.AudioOptions{},
	needle.DefaultMaxNewTokens)
```

The response includes optional `AudioText`, `AudioLanguage`, `AudioWords`,
`AudioTTFTMS` and `AudioDecodeTPS` fields. This is a raw completion: validate it
with `ValidateResponse` before acting on calls, and never execute suppressed
calls automatically. Stateless agents reset once before the audio request;
stateful agents retain context for manual tool-result continuations.

## Runtime and custom weights

Text and speech share the same serialized Needle 3 runtime and library path.
Loading or switching speech models preserves the active text conversation;
switching text agents follows the existing reset rules. Audio options are
cleared after each completion. An in-progress native call cannot be interrupted
by context cancellation.

`WhistleConfig.WeightsPath` selects a trusted custom speech archive;
`LibraryPath` and `CacheDir` work like `Config`. The shared `.cact` format tag
alone does not distinguish speech from text. Both loading paths check the
recognized manifest layout and reject wrong-kind or unknown-kind archives
before loading them. The current speech classifier accepts the published
version-1 audio manifest; a future format requires a library update.

## Offline setup

`FetchWhistle(ctx, needle.FetchOptions{CacheDir: cacheDir})` prepares the engine
and speech weights for offline use, without fetching text weights. `Platform`
selects the target desktop platform; `Generation` must be zero or 3. Preserve the
`.sha256` markers and use the same cache directory with `NewWhistle`. For audio
tool calling, also prepare text weights with `FetchEngine`.
