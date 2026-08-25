package needle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSupportedPlatforms(t *testing.T) {
	t.Parallel()

	want := []Platform{
		PlatformDarwinAMD64,
		PlatformDarwinARM64,
		PlatformLinuxAMD64,
		PlatformLinuxARM64,
		PlatformLinuxAMD64Musl,
		PlatformLinuxARM64Musl,
		PlatformWindowsAMD64,
		PlatformWindowsARM64,
	}
	if got := SupportedPlatforms(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedPlatforms() = %#v, want %#v", got, want)
	}
	for _, platform := range want {
		artifact, ok := artifacts[platform]
		if !ok {
			t.Fatalf("missing artifact for %s", platform)
		}
		if len(artifact.checksum) != sha256.Size*2 {
			t.Fatalf("checksum for %s has length %d", platform, len(artifact.checksum))
		}
	}
}

func TestFetchEngineRejectsUnknownPlatform(t *testing.T) {
	t.Parallel()

	_, err := FetchEngine(context.Background(), FetchOptions{Platform: "plan9-amd64"})
	if err == nil || !strings.Contains(err.Error(), ErrUnsupportedPlatform.Error()) {
		t.Fatalf("FetchEngine() error = %v", err)
	}
}

func TestCachedEngine(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	options := FetchOptions{Platform: PlatformDarwinARM64, CacheDir: cacheDir}
	if _, err := CachedEngine(options); !errors.Is(err, ErrEngineNotFound) {
		t.Fatalf("CachedEngine() missing error = %v", err)
	}
	path := filepath.Join(cacheDir, artifacts[PlatformDarwinARM64].libraryName)
	if err := os.WriteFile(path, []byte("library"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := CachedEngine(options)
	if err != nil {
		t.Fatalf("CachedEngine() error = %v", err)
	}
	if got != path {
		t.Fatalf("CachedEngine() = %q, want %q", got, path)
	}
}

func TestFetchArtifactDownloadsVerifiesAndCaches(t *testing.T) {
	t.Parallel()

	const archivePath = "needle/libneedle.test"
	const libraryContents = "native library"
	body := testWheel(t, archivePath, []byte(libraryContents))
	digest := sha256.Sum256(body)
	artifact := engineArtifact{
		checksum:    hex.EncodeToString(digest[:]),
		archivePath: archivePath,
		libraryName: "libneedle.test",
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = response.Write(body)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	path, err := fetchArtifact(context.Background(), server.Client(), cacheDir, artifact, server.URL)
	if err != nil {
		t.Fatalf("fetchArtifact() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != libraryContents {
		t.Fatalf("library = %q, want %q", data, libraryContents)
	}
	if _, err := fetchArtifact(context.Background(), server.Client(), cacheDir, artifact, server.URL); err != nil {
		t.Fatalf("cached fetchArtifact() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests.Load())
	}
	marker, err := os.ReadFile(path + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(marker)) != artifact.checksum {
		t.Fatalf("checksum marker = %q", marker)
	}
}

func TestDownloadAttemptRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "unexpected")
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(destination, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := downloadAttempt(context.Background(), server.Client(), server.URL, destination, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("downloadAttempt() error = %v", err)
	}
}

func TestDownloadArtifactRetriesTransientFailure(t *testing.T) {
	t.Parallel()

	body := []byte("artifact")
	digest := sha256.Sum256(body)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(response, "retry", http.StatusServiceUnavailable)
			return
		}
		_, _ = response.Write(body)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(destination, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := downloadArtifact(
		context.Background(), server.Client(), server.URL, destination, hex.EncodeToString(digest[:]),
	); err != nil {
		t.Fatalf("downloadArtifact() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTP requests = %d, want 2", requests.Load())
	}
}

func TestExtractLibraryRequiresExpectedMember(t *testing.T) {
	t.Parallel()

	body := testWheel(t, "needle/other", []byte("library"))
	wheelPath := filepath.Join(t.TempDir(), "engine.whl")
	if err := os.WriteFile(wheelPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	err := extractLibrary(wheelPath, t.TempDir(), filepath.Join(t.TempDir(), "lib"), "needle/missing")
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("extractLibrary() error = %v", err)
	}
}

func TestArtifactURLPinsRevision(t *testing.T) {
	t.Parallel()

	url := artifactURL(artifacts[PlatformDarwinARM64])
	if !strings.Contains(url, huggingFaceRevision) || strings.Contains(url, "/main/") {
		t.Fatalf("artifactURL() = %q", url)
	}
}

func testWheel(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	file, err := archive.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
