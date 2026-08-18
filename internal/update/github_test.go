package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServer answers in place of the GitHub API. No test in this package
// touches the real API: a test that depends on the network is a test that
// breaks on its own, on a day when nobody touched the code.
type fakeServer struct {
	*httptest.Server
	mu        sync.Mutex
	paths     []string
	responses map[string]func(w http.ResponseWriter, r *http.Request)
}

func newServer(t *testing.T) *fakeServer {
	t.Helper()
	s := &fakeServer{responses: map[string]func(http.ResponseWriter, *http.Request){}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		h := s.responses[r.URL.Path]
		s.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *fakeServer) respond(path string, h func(http.ResponseWriter, *http.Request)) *fakeServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[path] = h
	return s
}

func (s *fakeServer) respondJSON(path string, body any) *fakeServer {
	return s.respond(path, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	})
}

func (s *fakeServer) respondBytes(path string, data []byte) *fakeServer {
	return s.respond(path, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	})
}

func (s *fakeServer) visited() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func (s *fakeServer) client() *Cliente {
	return &Cliente{
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		BaseURL: s.URL,
		Repo:    "s0beran0/ngx",
	}
}

func TestLatestStableUsesTheEndpointThatAlreadyFiltersPrereleases(t *testing.T) {
	s := newServer(t)
	s.respondJSON("/repos/s0beran0/ngx/releases/latest", Release{
		Version: "v0.2.0",
		Assets:  []Asset{{Name: "ngx_0.2.0_linux_amd64.tar.gz", URL: "http://x/a"}},
	})

	rel, err := s.client().Latest(context.Background(), ChannelStable)

	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", rel.Version)
	assert.False(t, rel.Prerelease)
	assert.Equal(t, []string{"/repos/s0beran0/ngx/releases/latest"}, s.visited())
}

func TestLatestBetaAcceptsPrerelease(t *testing.T) {
	s := newServer(t)
	s.respondJSON("/repos/s0beran0/ngx/releases", []Release{
		{Version: "v0.3.0-rc.1", Prerelease: true},
		{Version: "v0.2.0"},
	})

	rel, err := s.client().Latest(context.Background(), ChannelBeta)

	require.NoError(t, err)
	assert.Equal(t, "v0.3.0-rc.1", rel.Version)
	assert.True(t, rel.Prerelease)
	assert.Equal(t, "/repos/s0beran0/ngx/releases", s.visited()[0])
}

func TestLatestBetaSkipsDraft(t *testing.T) {
	s := newServer(t)
	s.respondJSON("/repos/s0beran0/ngx/releases", []Release{
		{Version: "v0.4.0-rc.1", Prerelease: true, Draft: true},
		{Version: "v0.3.0-rc.1", Prerelease: true},
	})

	rel, err := s.client().Latest(context.Background(), ChannelBeta)

	require.NoError(t, err)
	assert.Equal(t, "v0.3.0-rc.1", rel.Version)
}

func TestLatestBetaWithNoReleaseAtAll(t *testing.T) {
	s := newServer(t)
	s.respondJSON("/repos/s0beran0/ngx/releases", []Release{})

	_, err := s.client().Latest(context.Background(), ChannelBeta)

	assert.Equal(t, CodigoReleaseAusente, codeOf(t, err))
}

func TestLatestHandlesRateLimitWithItsOwnError(t *testing.T) {
	// It is the most likely failure in real use: 60 calls per hour per IP
	// without authentication. A generic "it failed" would send the person
	// investigating the network.
	s := newServer(t)
	reset := time.Now().Add(20 * time.Minute).Unix()
	s.respond("/repos/s0beran0/ngx/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", itoa(reset))
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := s.client().Latest(context.Background(), ChannelStable)

	assert.Equal(t, CodigoRateLimit, codeOf(t, err))
	assert.Contains(t, err.Error(), "rate limit")
	assert.Contains(t, err.Error(), "the window reopens at")
}

func TestLatest403WithoutRateLimitIsNotALimitError(t *testing.T) {
	s := newServer(t)
	s.respond("/repos/s0beran0/ngx/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := s.client().Latest(context.Background(), ChannelStable)

	assert.Equal(t, CodigoRede, codeOf(t, err))
}

func TestPorVersaoAcceptsWithOrWithoutV(t *testing.T) {
	s := newServer(t)
	s.respondJSON("/repos/s0beran0/ngx/releases/tags/v0.1.0", Release{Version: "v0.1.0"})

	rel, err := s.client().PorVersao(context.Background(), "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", rel.Version)

	rel, err = s.client().PorVersao(context.Background(), "v0.1.0")
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", rel.Version)
}

func TestPorVersaoNonexistent(t *testing.T) {
	s := newServer(t)

	_, err := s.client().PorVersao(context.Background(), "v9.9.9")

	assert.Equal(t, CodigoReleaseAusente, codeOf(t, err))
}

func TestReleaseSerializesAssetsAsEmptyList(t *testing.T) {
	s := newServer(t)
	s.respond("/repos/s0beran0/ngx/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
	})

	rel, err := s.client().Latest(context.Background(), ChannelStable)
	require.NoError(t, err)

	b, err := json.Marshal(rel)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"assets":[]`)
}

func TestRespectsCanceledContext(t *testing.T) {
	s := newServer(t)
	s.respond("/repos/s0beran0/ngx/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := s.client().Latest(ctx, ChannelStable)

	assert.Equal(t, CodigoRede, codeOf(t, err))
}

func TestAssetDaPlataformaChoosesBySuffix(t *testing.T) {
	rel := &Release{Version: "v1.0.0", Assets: []Asset{
		{Name: "ngx_1.0.0_linux_amd64.tar.gz"},
		{Name: "ngx_1.0.0_linux_arm64.tar.gz"},
		{Name: "ngx_1.0.0_windows_amd64.zip"},
		{Name: NomeChecksums},
	}}

	a, err := rel.AssetDaPlataforma("linux", "arm64")
	require.NoError(t, err)
	assert.Equal(t, "ngx_1.0.0_linux_arm64.tar.gz", a.Name)

	a, err = rel.AssetDaPlataforma("windows", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "ngx_1.0.0_windows_amd64.zip", a.Name)
}

func TestAssetDaPlataformaMissingListsWhatExists(t *testing.T) {
	rel := &Release{Version: "v1.0.0", Assets: []Asset{{Name: "ngx_1.0.0_linux_amd64.tar.gz"}}}

	_, err := rel.AssetDaPlataforma("darwin", "arm64")

	assert.Equal(t, CodigoAssetAusente, codeOf(t, err))
	assert.Contains(t, err.Error(), "ngx_1.0.0_linux_amd64.tar.gz")
	assert.Contains(t, err.Error(), "darwin/arm64")
}

func TestAssetPorNomeMissing(t *testing.T) {
	rel := &Release{Version: "v1.0.0", Assets: []Asset{}}

	_, err := rel.AssetPorNome(NomeAssinatura)

	assert.Equal(t, CodigoAssetAusente, codeOf(t, err))
	assert.Contains(t, err.Error(), NomeAssinatura)
}

func TestBaixarRefusesErrorStatus(t *testing.T) {
	s := newServer(t)
	s.respond("/artifact", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := s.client().Baixar(context.Background(), s.URL+"/artifact")

	assert.Equal(t, CodigoRede, codeOf(t, err))
	assert.Contains(t, err.Error(), "500")
}

func TestBaixarReturnsTheBytes(t *testing.T) {
	s := newServer(t)
	s.respondBytes("/artifact", []byte("binary content"))

	data, err := s.client().Baixar(context.Background(), s.URL+"/artifact")

	require.NoError(t, err)
	assert.Equal(t, "binary content", string(data))
}

func TestParseChannel(t *testing.T) {
	c, err := ParseChannel("")
	require.NoError(t, err)
	assert.Equal(t, ChannelStable, c)

	c, err = ParseChannel("BETA")
	require.NoError(t, err)
	assert.Equal(t, ChannelBeta, c)

	_, err = ParseChannel("nightly")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel")
}

func TestCanalDoAmbiente(t *testing.T) {
	// NGX_CHANNEL=beta is the only way into the prerelease channel without
	// passing the flag: changing channel is possible, never accidental.
	c, err := CanalDoAmbiente(func(string) string { return "beta" })
	require.NoError(t, err)
	assert.Equal(t, ChannelBeta, c)

	c, err = CanalDoAmbiente(func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, ChannelStable, c)

	_, err = CanalDoAmbiente(func(string) string { return "unstable" })
	assert.Error(t, err)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
