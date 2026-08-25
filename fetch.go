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
	"strings"
	"sync"
	"time"
)

const (
	// EngineVersion is the Needle native engine version used by this package.
	EngineVersion = "2.0.3"
	// EnvLibraryPath overrides native engine discovery.
	EnvLibraryPath = "NEEDLE_LIB_PATH"

	huggingFaceRepo     = "Cactus-Compute/needle2"
	huggingFaceRevision = "98fbd955b0347e78059be0c253cc1ffa09b87bc7"
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
	Platform Platform
	CacheDir string
	Client   *http.Client
}

type engineArtifact struct {
	filename    string
	checksum    string
	archivePath string
	libraryName string
}

var artifacts = map[Platform]engineArtifact{
	PlatformDarwinARM64: {
		filename:    "cactus_needle-2.0.3-py3-none-macosx_11_0_arm64.whl",
		checksum:    "17c2b9ff3c3f1238e0a26385cfda0780d120cda390594d7fc7e5b7f2a970ce95",
		archivePath: "needle/libneedle.dylib",
		libraryName: "libneedle.dylib",
	},
	PlatformDarwinAMD64: {
		filename:    "cactus_needle-2.0.3-py3-none-macosx_11_0_x86_64.whl",
		checksum:    "dc55a60b6803fbfd73fa50c09803df54bb47155dcdec74e5988c21838d5cc070",
		archivePath: "needle/libneedle.dylib",
		libraryName: "libneedle.dylib",
	},
	PlatformLinuxARM64: {
		filename:    "cactus_needle-2.0.3-py3-none-manylinux2014_aarch64.whl",
		checksum:    "0e6f0d04e42ac16f34661c7eaab027c87e1fdac294b3dbdb6ca5c9d0597398ab",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformLinuxAMD64: {
		filename:    "cactus_needle-2.0.3-py3-none-manylinux2014_x86_64.whl",
		checksum:    "d23df1d0babeb7323dcaf860dfaf833bbd7d2229b205f691c05c9cbc6d3d3653",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformLinuxARM64Musl: {
		filename:    "cactus_needle-2.0.3-py3-none-musllinux_1_2_aarch64.whl",
		checksum:    "89ae29fb3f3dabd46e374581bd87f71b7d044a95b9bd65ede2b42688ade632f0",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformLinuxAMD64Musl: {
		filename:    "cactus_needle-2.0.3-py3-none-musllinux_1_2_x86_64.whl",
		checksum:    "1a3558242d7f252255efff3258fc81b2d47ea74eace862fda60f16fe684caa53",
		archivePath: "needle/libneedle.so",
		libraryName: "libneedle.so",
	},
	PlatformWindowsAMD64: {
		filename:    "cactus_needle-2.0.3-py3-none-win_amd64.whl",
		checksum:    "3c012603a6bc5d7f36aa26da3d0819a8fa226dd40c7f242013b5e214a51168c7",
		archivePath: "needle/libneedle.dll",
		libraryName: "libneedle.dll",
	},
	PlatformWindowsARM64: {
		filename:    "cactus_needle-2.0.3-py3-none-win_arm64.whl",
		checksum:    "cadcd8ff7f18b47046c547cbc450dabe607c197db2855eb6497d615ff551db0f",
		archivePath: "needle/libneedle.dll",
		libraryName: "libneedle.dll",
	},
}

var fetchMu sync.Mutex

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

// FetchEngine downloads, verifies, and caches the shared library for one
// desktop platform. It returns the path to the extracted library.
func FetchEngine(ctx context.Context, options FetchOptions) (string, error) {
	platform := options.Platform
	if platform == "" {
		var err error
		platform, err = CurrentPlatform()
		if err != nil {
			return "", err
		}
	}
	artifact, ok := artifacts[platform]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	cacheDir := options.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = defaultCacheDir()
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
	return fetchArtifact(ctx, client, cacheDir, artifact, artifactURL(artifact))
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
	if err := extractLibrary(wheelPath, cacheDir, target, artifact.archivePath); err != nil {
		return "", err
	}
	if err := os.WriteFile(marker, []byte(artifact.checksum+"\n"), 0o644); err != nil {
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
	return fmt.Errorf("needle: download engine after 3 attempts: %w", lastErr)
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
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(marker)
	return err == nil && strings.TrimSpace(string(data)) == checksum
}

func artifactURL(artifact engineArtifact) string {
	return fmt.Sprintf(
		"https://huggingface.co/%s/resolve/%s/python/%s?download=true",
		huggingFaceRepo,
		huggingFaceRevision,
		artifact.filename,
	)
}

func defaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("needle: find home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "cactus-needle", EngineVersion), nil
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
