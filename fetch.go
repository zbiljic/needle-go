package needle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	// EngineVersion is the default (Needle 3) native engine version.
	EngineVersion = "3.0.1"
	// EnvLibraryPath overrides Needle 2 engine discovery for legacy clients.
	EnvLibraryPath = "NEEDLE_LIB_PATH"

	huggingFaceRepo     = "Cactus-Compute/needle3"
	huggingFaceRevision = "9da75122d4ca11aa4a667281c9c8ba38a7eed679"
	maxArtifactSize     = 64 << 20
)

// Platform identifies a downloadable desktop engine build.
type Platform string

const (
	PlatformDarwinAMD64    Platform = "darwin-amd64"
	PlatformDarwinARM64    Platform = "darwin-arm64"
	PlatformLinuxAMD64     Platform = "linux-amd64"
	PlatformLinuxARM64     Platform = "linux-arm64"
	PlatformLinuxAMD64Musl Platform = "linux-amd64-musl"
	PlatformLinuxARM64Musl Platform = "linux-arm64-musl"
	PlatformWindowsAMD64   Platform = "windows-amd64"
	PlatformWindowsARM64   Platform = "windows-arm64"
)

// FetchOptions configures an engine download. An empty Platform selects the
// current process platform. CacheDir is the exact destination directory.
type FetchOptions struct {
	// Generation selects 2 or 3; zero defaults to 3.
	Generation int
	Platform   Platform
	CacheDir   string
	Client     *http.Client
}

type engineArtifact struct {
	filename    string
	checksum    string
	archivePath string
	libraryName string
}

var artifactsV2 = map[Platform]engineArtifact{
	PlatformDarwinARM64: {
		filename:    "cactus_needle-2.0.4-py3-none-macosx_11_0_arm64.whl",
		checksum:    "abae4cca0a4d84ec73da4bde18803b9be812a9209e48fb7fa372002ebaa60265",
		archivePath: "needle/libneedle.dylib",
		libraryName: "libneedle.dylib",
	},
	PlatformDarwinAMD64: {
		filename:    "cactus_needle-2.0.4-py3-none-macosx_11_0_x86_64.whl",
		checksum:    "071e93d996021b4f6b5bee055777cd81be9f6963d2565115ec461b9f71c7245e",
		archivePath: "needle/libneedle.dylib",
		libraryName: "libneedle.dylib",
	},
	PlatformLinuxARM64: {
		filename:    "cactus_needle-2.0.4-py3-none-manylinux2014_aarch64.whl",
		checksum:    "e655a13f9d3239e601936ff2d0f6acafd8f6020aaf7b1862ce4fceefacfd6556",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformLinuxAMD64: {
		filename:    "cactus_needle-2.0.4-py3-none-manylinux2014_x86_64.whl",
		checksum:    "13a84e6c73095fd175b11d46a30a984b62123d94421b769c107074aff7f65c2b",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformLinuxARM64Musl: {
		filename:    "cactus_needle-2.0.4-py3-none-musllinux_1_2_aarch64.whl",
		checksum:    "eaacaf7925e596fd7d18814bece02770b250039c0b92bbdd899967beb012c114",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformLinuxAMD64Musl: {
		filename:    "cactus_needle-2.0.4-py3-none-musllinux_1_2_x86_64.whl",
		checksum:    "f94633df433643a3b9b20b84ea19f12b423809d112ba2421d87aad8b07ca3277",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformWindowsAMD64: {
		filename:    "cactus_needle-2.0.4-py3-none-win_amd64.whl",
		checksum:    "b4803501a109af3782efe112be27515947c108ecec3bf85703ae6b97bb7210e1",
		archivePath: "needle/libneedle.dll",
		libraryName: "libneedle.dll",
	},
	PlatformWindowsARM64: {
		filename:    "cactus_needle-2.0.4-py3-none-win_arm64.whl",
		checksum:    "e03d1301f37dccd468a9b6619c31f82e2c428465ea7c055bf21214feff9b7869",
		archivePath: "needle/libneedle.dll",
		libraryName: "libneedle.dll",
	},
}

var artifacts = map[Platform]engineArtifact{
	PlatformDarwinARM64: {
		filename:    "cactus_needle-3.0.1-py3-none-macosx_11_0_arm64.whl",
		checksum:    "9d3ba55986ad664ddffac4aec680c0657dc82ad04f868898e8085f13755c025f",
		archivePath: "needle/libneedle3.dylib",
		libraryName: "libneedle3.dylib",
	},
	PlatformDarwinAMD64: {
		filename:    "cactus_needle-3.0.1-py3-none-macosx_11_0_x86_64.whl",
		checksum:    "22d2693ea23439c2934556c2da8d0aaf708d55546a8b9b68a0647333b42501eb",
		archivePath: "needle/libneedle3.dylib",
		libraryName: "libneedle3.dylib",
	},
	PlatformLinuxARM64: {
		filename:    "cactus_needle-3.0.1-py3-none-manylinux2014_aarch64.whl",
		checksum:    "a2196ca18bd4ec6ebcb1e9d0341f4c4764e5f61ebe313f4d00ff6f2426fc501d",
		archivePath: "needle/libneedle3.so",
		libraryName: "libneedle3.so",
	},
	PlatformLinuxAMD64: {
		filename:    "cactus_needle-3.0.1-py3-none-manylinux2014_x86_64.whl",
		checksum:    "01370dc7ab28fb4f7cf98dc240cf3dc8a11a29916a404f6d4ed621f496d667ee",
		archivePath: "needle/libneedle3.so",
		libraryName: "libneedle3.so",
	},
	PlatformLinuxARM64Musl: {
		filename:    "cactus_needle-3.0.1-py3-none-musllinux_1_2_aarch64.whl",
		checksum:    "14aef80edefc2709dbbe2f0cd1adcdc99b2213d2f2810650d38f13c9b2daad1e",
		archivePath: "needle/libneedle3.so",
		libraryName: "libneedle3.so",
	},
	PlatformLinuxAMD64Musl: {
		filename:    "cactus_needle-3.0.1-py3-none-musllinux_1_2_x86_64.whl",
		checksum:    "89995bb2b2559ba7859e4775bc60de2f608bef91b29d8ea91e22ac6eec16c39f",
		archivePath: "needle/libneedle3.so",
		libraryName: "libneedle3.so",
	},
	PlatformWindowsAMD64: {
		filename:    "cactus_needle-3.0.1-py3-none-win_amd64.whl",
		checksum:    "b8c56b881221569a2bd7e5e7e9b202b9e9183e2a5dded3c68b543152a1de975f",
		archivePath: "needle/libneedle3.dll",
		libraryName: "libneedle3.dll",
	},
	PlatformWindowsARM64: {
		filename:    "cactus_needle-3.0.1-py3-none-win_arm64.whl",
		checksum:    "6705699b30daccd7e12e3692766eae7c5841379e5a9878ba2816c91fa86f50c8",
		archivePath: "needle/libneedle3.dll",
		libraryName: "libneedle3.dll",
	},
}

var baseWeights = engineArtifact{
	filename:    "needle3.cact",
	libraryName: "needle3.cact",
	checksum:    "c9d915eca282ed42d1a09b143b592adb4cc6744ffe2d294adf5cfc5548170c38",
}

type engineRelease struct {
	generation              int
	version, repo, revision string
	artifacts               map[Platform]engineArtifact
}

func releaseFor(generation int) (engineRelease, error) {
	switch generation {
	case 2:
		return engineRelease{
			generation: 2,
			version:    "2.0.4",
			repo:       "Cactus-Compute/needle2",
			revision:   "32e9e3a93b205f786929697446ae669cf0a84579",
			artifacts:  artifactsV2,
		}, nil
	case 0, 3:
		return engineRelease{
			generation: 3,
			version:    EngineVersion,
			repo:       huggingFaceRepo,
			revision:   huggingFaceRevision,
			artifacts:  artifacts,
		}, nil
	default:
		return engineRelease{}, fmt.Errorf("needle: unsupported generation %d; want 2 or 3", generation)
	}
}

// EngineVersionFor returns the pinned engine version for a generation (zero means 3).
func EngineVersionFor(generation int) (string, error) {
	release, err := releaseFor(generation)
	return release.version, err
}

var fetchMu sync.Mutex

// ErrEngineNotFound indicates that a cached engine library is unavailable.
var ErrEngineNotFound = errors.New("needle: engine library not found")

// SupportedPlatforms returns the desktop engine builds known to this version.
func SupportedPlatforms() []Platform {
	return []Platform{
		PlatformDarwinAMD64,
		PlatformDarwinARM64,
		PlatformLinuxAMD64,
		PlatformLinuxARM64,
		PlatformLinuxAMD64Musl,
		PlatformLinuxARM64Musl,
		PlatformWindowsAMD64,
		PlatformWindowsARM64,
	}
}

// CurrentPlatform returns the engine build matching the running process.
func CurrentPlatform() (Platform, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		return PlatformDarwinAMD64, nil
	case "darwin/arm64":
		return PlatformDarwinARM64, nil
	case "linux/amd64":
		if isMusl() {
			return PlatformLinuxAMD64Musl, nil
		}
		return PlatformLinuxAMD64, nil
	case "linux/arm64":
		if isMusl() {
			return PlatformLinuxARM64Musl, nil
		}
		return PlatformLinuxARM64, nil
	case "windows/amd64":
		return PlatformWindowsAMD64, nil
	case "windows/arm64":
		return PlatformWindowsARM64, nil
	default:
		return "", fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
	}
}

// CachedEngine returns the expected cached library path without downloading
// anything. It accepts libraries installed by needle-go or another client.
func CachedEngine(options FetchOptions) (string, error) {
	release, err := releaseFor(options.Generation)
	if err != nil {
		return "", err
	}
	platform := options.Platform
	if platform == "" {
		var err error
		platform, err = CurrentPlatform()
		if err != nil {
			return "", err
		}
	}
	artifact, ok := release.artifacts[platform]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	cacheDir := options.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = defaultCacheDir(release, platform)
		if err != nil {
			return "", err
		}
	}
	path := filepath.Join(cacheDir, artifact.libraryName)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrEngineNotFound, path)
		}
		return "", fmt.Errorf("needle: inspect cached engine: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is not a regular file", ErrEngineNotFound, path)
	}
	return path, nil
}

// FetchEngine downloads, verifies, and caches the shared library and required
// base weights for one desktop platform. It returns the library path.
func FetchEngine(ctx context.Context, options FetchOptions) (string, error) {
	path, err := fetchEngineLibrary(ctx, options)
	if err != nil {
		return "", err
	}
	if options.Generation == 0 || options.Generation == 3 {
		if _, err := fetchBaseWeights(ctx, options); err != nil {
			return "", err
		}
	}
	return path, nil
}

func fetchEngineLibrary(ctx context.Context, options FetchOptions) (string, error) {
	release, err := releaseFor(options.Generation)
	if err != nil {
		return "", err
	}
	platform := options.Platform
	if platform == "" {
		var err error
		platform, err = CurrentPlatform()
		if err != nil {
			return "", err
		}
	}
	artifact, ok := release.artifacts[platform]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	return fetchWithOptions(ctx, options, release, artifact)
}

func fetchBaseWeights(ctx context.Context, options FetchOptions) (string, error) {
	release, _ := releaseFor(3)
	return fetchWithOptions(ctx, options, release, baseWeights)
}

func fetchWithOptions(
	ctx context.Context,
	options FetchOptions,
	release engineRelease,
	artifact engineArtifact,
) (string, error) {
	cacheDir := options.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = defaultCacheDir(release, options.Platform)
		if err != nil {
			return "", err
		}
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	fetchMu.Lock()
	defer fetchMu.Unlock()
	return fetchArtifact(ctx, client, cacheDir, artifact, artifactURL(release, artifact))
}

func fetchArtifact(
	ctx context.Context,
	client *http.Client,
	cacheDir string,
	artifact engineArtifact,
	url string,
) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("needle: create engine cache: %w", err)
	}
	target := filepath.Join(cacheDir, artifact.libraryName)
	marker := target + ".sha256"
	if cachedArtifact(target, marker, artifact.checksum) {
		return target, nil
	}

	wheel, err := os.CreateTemp(cacheDir, ".needle-*.whl")
	if err != nil {
		return "", fmt.Errorf("needle: create temporary download: %w", err)
	}
	wheelPath := wheel.Name()
	if err := wheel.Close(); err != nil {
		return "", fmt.Errorf("needle: close temporary download: %w", err)
	}
	defer os.Remove(wheelPath)

	if err := downloadArtifact(ctx, client, url, wheelPath, artifact.checksum); err != nil {
		return "", err
	}
	if artifact.archivePath == "" {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("needle: replace cached weights: %w", err)
		}
		if err := os.Rename(wheelPath, target); err != nil {
			return "", fmt.Errorf("needle: install weights: %w", err)
		}
	} else {
		if err := extractLibrary(wheelPath, cacheDir, target, artifact.archivePath); err != nil {
			return "", err
		}
	}
	installedHash, err := installedChecksum(target)
	if err != nil {
		return "", fmt.Errorf("needle: hash installed artifact: %w", err)
	}
	if err := os.WriteFile(marker, []byte(artifact.checksum+"\n"+installedHash+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("needle: write engine checksum marker: %w", err)
	}
	return target, nil
}

func downloadArtifact(ctx context.Context, client *http.Client, url, destination, checksum string) error {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			delay := time.Duration(1<<(attempt-1)) * 250 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		lastErr = downloadAttempt(ctx, client, url, destination, checksum)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return lastErr
		}
	}
	return fmt.Errorf("needle: download artifact after 3 attempts: %w", lastErr)
}

func downloadAttempt(ctx context.Context, client *http.Client, url, destination, checksum string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "needle-go/"+EngineVersion)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("download returned %s", response.Status)
	}

	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxArtifactSize+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxArtifactSize {
		return fmt.Errorf("artifact exceeds %d bytes", maxArtifactSize)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != checksum {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actual, checksum)
	}
	return nil
}

func extractLibrary(wheelPath, cacheDir, target, archivePath string) error {
	archive, err := zip.OpenReader(wheelPath)
	if err != nil {
		return fmt.Errorf("needle: open engine archive: %w", err)
	}
	defer archive.Close()

	var source *zip.File
	for _, file := range archive.File {
		if file.Name == archivePath {
			source = file
			break
		}
	}
	if source == nil {
		return fmt.Errorf("needle: engine archive does not contain %s", archivePath)
	}
	reader, err := source.Open()
	if err != nil {
		return fmt.Errorf("needle: open engine library: %w", err)
	}
	defer reader.Close()

	temporary, err := os.CreateTemp(cacheDir, ".libneedle-*")
	if err != nil {
		return fmt.Errorf("needle: create temporary library: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, copyErr := io.Copy(temporary, io.LimitReader(reader, maxArtifactSize+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("needle: extract engine library: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("needle: close engine library: %w", closeErr)
	}
	if written > maxArtifactSize {
		return fmt.Errorf("needle: engine library exceeds %d bytes", maxArtifactSize)
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return fmt.Errorf("needle: set engine library permissions: %w", err)
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("needle: replace cached engine: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("needle: install engine library: %w", err)
	}
	return nil
}

func cachedArtifact(target, marker, checksum string) bool {
	installedHash, err := installedChecksum(target)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(marker)
	return err == nil && string(data) == checksum+"\n"+installedHash+"\n"
}

func installedChecksum(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxArtifactSize {
		return "", errors.New("invalid cached artifact size or type")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxArtifactSize+1))
	if err != nil {
		return "", err
	}
	if written == 0 || written > maxArtifactSize {
		return "", errors.New("invalid cached artifact size")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func artifactURL(release engineRelease, artifact engineArtifact) string {
	name := artifact.filename
	if artifact.archivePath != "" {
		name = "python/" + name
	}
	return fmt.Sprintf(
		"https://huggingface.co/%s/resolve/%s/%s?download=true",
		release.repo,
		release.revision,
		name,
	)
}

func defaultCacheDir(release engineRelease, platform Platform) (string, error) {
	if platform == "" {
		var err error
		platform, err = CurrentPlatform()
		if err != nil {
			return "", err
		}
	}
	if _, ok := release.artifacts[platform]; !ok {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("needle: find home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "cactus-needle", release.version, string(platform)), nil
}

func isMusl() bool {
	if data, err := os.ReadFile("/proc/self/maps"); err == nil && bytes.Contains(data, []byte("musl")) {
		return true
	}
	for _, pattern := range []string{"/lib/ld-musl-*.so.1", "/usr/lib/ld-musl-*.so.1"} {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return true
		}
	}
	return false
}
