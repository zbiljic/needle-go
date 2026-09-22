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
	libraries := map[string]string{
		"darwin":  "libneedle3.dylib",
		"linux":   "libneedle3.so",
		"windows": "libneedle3.dll",
	}
	if got := SupportedPlatforms(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedPlatforms() = %#v, want %#v", got, want)
	}
	if len(artifacts) != len(want) {
		t.Fatalf("len(artifacts) = %d, want %d", len(artifacts), len(want))
	}
	for _, platform := range want {
		artifact, ok := artifacts[platform]
		if !ok {
			t.Fatalf("missing artifact for %s", platform)
		}
		checksum, err := hex.DecodeString(artifact.checksum)
		if err != nil || len(checksum) != sha256.Size {
			t.Fatalf("checksum for %s = %q, want %d-byte hex: %v", platform, artifact.checksum, sha256.Size, err)
		}
		if !strings.HasPrefix(artifact.filename, "cactus_needle-"+EngineVersion+"-") {
			t.Errorf("filename for %s = %q", platform, artifact.filename)
		}
		libraryName := libraries[strings.SplitN(string(platform), "-", 2)[0]]
		if artifact.libraryName != libraryName || artifact.archivePath != "needle/"+libraryName {
			t.Errorf("library for %s = %q / %q", platform, artifact.archivePath, artifact.libraryName)
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
	installedDigest := sha256.Sum256([]byte(libraryContents))
	if string(marker) != fmt.Sprintf("%s\n%x\n", artifact.checksum, installedDigest) {
		t.Fatalf("checksum marker = %q", marker)
	}
	for _, corruption := range []string{"file", "artifact hash", "installed hash"} {
		switch corruption {
		case "file":
			err = os.WriteFile(path, []byte("changed library"), 0o600)
		case "artifact hash":
			err = os.WriteFile(path+".sha256", []byte(fmt.Sprintf("%s\n%x\n", strings.Repeat("0", 64), installedDigest)), 0o600)
		case "installed hash":
			err = os.WriteFile(path+".sha256", []byte(artifact.checksum+"\n"+strings.Repeat("0", 64)+"\n"), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		before := requests.Load()
		if _, err := fetchArtifact(context.Background(), server.Client(), cacheDir, artifact, server.URL); err != nil {
			t.Fatal(err)
		}
		if requests.Load() != before+1 {
			t.Fatalf("%s did not trigger a fresh download", corruption)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != libraryContents {
			t.Fatalf("repaired library = %q, err = %v", got, err)
		}
	}
}

func TestVerifiedCacheReuse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	client := &http.Client{Transport: rejectCacheDownload{}}
	current, err := CurrentPlatform()
	if err != nil {
		t.Fatal(err)
	}
	for _, generation := range []int{2, 3} {
		release, _ := releaseFor(generation)
		for _, platform := range []Platform{PlatformDarwinARM64, PlatformDarwinAMD64, current, ""} {
			resolved := platform
			if resolved == "" {
				resolved = current
			}
			for _, override := range []string{"", t.TempDir()} {
				options := FetchOptions{Generation: generation, Platform: platform, CacheDir: override, Client: client}
				directory := override
				if directory == "" {
					directory = filepath.Join(home, ".cache", "cactus-needle", release.version, string(resolved))
				}
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatal(err)
				}
				library := release.artifacts[resolved]
				cached := []engineArtifact{library}
				if generation == 3 {
					cached = append(cached, baseWeights)
				}
				for _, artifact := range cached {
					contents := []byte("cached " + artifact.libraryName)
					path := filepath.Join(directory, artifact.libraryName)
					if err := os.WriteFile(path, contents, 0o600); err != nil {
						t.Fatal(err)
					}
					marker := fmt.Sprintf("%s\n%x\n", artifact.checksum, sha256.Sum256(contents))
					if err := os.WriteFile(path+".sha256", []byte(marker), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				want := filepath.Join(directory, library.libraryName)
				if got, err := FetchEngine(context.Background(), options); err != nil || got != want {
					t.Fatalf("FetchEngine(%+v) = %q, %v; want %q", options, got, err, want)
				}
				if got, err := CachedEngine(options); err != nil || got != want {
					t.Fatalf("CachedEngine(%+v) = %q, %v; want %q", options, got, err, want)
				}
			}
		}
	}
}

type rejectCacheDownload struct{}

func (rejectCacheDownload) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected download for shared cache")
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

	for _, generation := range []int{2, 3} {
		release, err := releaseFor(generation)
		if err != nil {
			t.Fatal(err)
		}
		for _, platform := range SupportedPlatforms() {
			artifact := release.artifacts[platform]
			want := fmt.Sprintf(
				"https://huggingface.co/%s/resolve/%s/python/%s?download=true",
				release.repo,
				release.revision,
				artifact.filename,
			)
			if got := artifactURL(release, artifact); got != want || strings.Contains(got, "/main/") {
				t.Errorf("artifactURL(%s) = %q, want %q", platform, got, want)
			}
		}
	}
}

func TestPinnedEngineArtifacts(t *testing.T) {
	if os.Getenv("NEEDLE_TEST_ARTIFACTS") != "1" {
		t.Skip("set NEEDLE_TEST_ARTIFACTS=1 to verify pinned release artifacts")
	}
	for _, generation := range []int{2, 3} {
		cacheDir := t.TempDir()
		release, _ := releaseFor(generation)
		for _, platform := range SupportedPlatforms() {
			t.Run(fmt.Sprintf("v%d/%s", generation, platform), func(t *testing.T) {
				artifact := release.artifacts[platform]
				path, err := FetchEngine(context.Background(), FetchOptions{
					Generation: generation,
					Platform:   platform,
					CacheDir:   cacheDir,
				})
				if err != nil {
					t.Fatal(err)
				}
				if filepath.Base(path) != artifact.libraryName {
					t.Fatalf("FetchEngine() path = %q, want library %q", path, artifact.libraryName)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !info.Mode().IsRegular() || info.Size() == 0 {
					t.Fatalf("FetchEngine() file mode = %v, size = %d", info.Mode(), info.Size())
				}
			})
		}
	}
}

func TestFetchWeightsAndGenerationCacheIsolation(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	body := []byte{0x84, 0x2a, 0xe1, 0x05, 1}
	digest := sha256.Sum256(body)
	artifact := engineArtifact{
		filename:    "needle3.cact",
		libraryName: "needle3.cact",
		checksum:    hex.EncodeToString(digest[:]),
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	for range 2 {
		path, err := fetchArtifact(context.Background(), server.Client(), cache, artifact, server.URL)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("weights=%v err=%v", got, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("downloads=%d", requests.Load())
	}
	// A failed checksum cannot replace the previously installed weights.
	bad := artifact
	bad.checksum = strings.Repeat("0", 64)
	if _, err := fetchArtifact(
		context.Background(), server.Client(), cache, bad, server.URL,
	); err == nil {
		t.Fatal("accepted a bad checksum")
	}
	got, err := os.ReadFile(filepath.Join(cache, artifact.libraryName))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("existing weights changed: %v, %v", got, err)
	}
	paths := make(map[int]string)
	for _, generation := range []int{2, 3} {
		release, _ := releaseFor(generation)
		library := release.artifacts[PlatformDarwinARM64]
		path := filepath.Join(cache, library.libraryName)
		if err := os.WriteFile(path, []byte{byte(generation)}, 0o600); err != nil {
			t.Fatal(err)
		}
		paths[generation] = path
	}
	for _, generation := range []int{2, 3} {
		path, err := CachedEngine(FetchOptions{
			Generation: generation,
			Platform:   PlatformDarwinARM64,
			CacheDir:   cache,
		})
		if err != nil || path != paths[generation] || paths[2] == paths[3] {
			t.Fatalf("cache paths=%v got=%s err=%v", paths, path, err)
		}
	}
	release, _ := releaseFor(3)
	if strings.Contains(artifactURL(release, baseWeights), "/python/") {
		t.Fatal("weights URL points inside python/")
	}
	for _, generation := range []int{-1, 1, 4} {
		if _, err := FetchEngine(context.Background(), FetchOptions{Generation: generation}); err == nil {
			t.Fatalf("accepted generation %d", generation)
		}
		if _, err := CachedEngine(FetchOptions{Generation: generation}); err == nil {
			t.Fatalf("accepted cached generation %d", generation)
		}
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
