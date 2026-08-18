package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseSimples(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "simples.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseProduzUmArquivoComFonte(t *testing.T) {
	tree := parseSimples(t)

	require.Len(t, tree.Files, 1)
	require.NotEmpty(t, tree.Files[0].Source, "the original source has to be kept around for the spans")
	require.Contains(t, tree.Files[0].Path, "simples.conf")
}

func TestParsePreservaComentarios(t *testing.T) {
	tree := parseSimples(t)

	var comentarios int
	tree.Walk(func(n *config.Node) bool {
		if n.IsComment() {
			comentarios++
			require.NotNil(t, n.Comment)
			require.Contains(t, *n.Comment, "example configuration")
		}
		return true
	})

	require.Equal(t, 1, comentarios)
}

func TestParseMonstaBlocosAninhados(t *testing.T) {
	tree := parseSimples(t)

	var http *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})

	require.NotNil(t, http)
	require.True(t, http.HasBlock())

	var servers, upstreams int
	for _, filho := range http.Block {
		switch filho.Directive {
		case "server":
			servers++
		case "upstream":
			upstreams++
		}
	}
	require.Equal(t, 1, servers)
	require.Equal(t, 1, upstreams)
}

func TestParseGuardaArgumentosEArquivo(t *testing.T) {
	tree := parseSimples(t)

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})

	require.NotNil(t, listen)
	require.Equal(t, []string{"443", "ssl"}, listen.Args)
	require.Contains(t, listen.File, "simples.conf")
}

func TestParseArquivoInexistenteVirarErro(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{Path: "testdata/nao-existe.conf"})

	require.Error(t, err)
}

// Redaction happens on output: the in-memory tree keeps the real value,
// otherwise fmt would write *** into the user's .conf.
func TestArvoreEmMemoriaNaoEhRedigida(t *testing.T) {
	tree := parseSimples(t)

	var achou bool
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "ssl_certificate_key" {
			achou = true
			require.Equal(t, []string{"/etc/ssl/private/api.key"}, n.Args)
		}
		return true
	})
	require.True(t, achou)
}

// Crossplane does not abort on a syntax error: it records the problem in
// payload.Errors/cfg.Errors and returns err == nil. Without this handling,
// TestParseErroDeSintaxeViraParseErrors would fail because config.Parse would
// return a *Tree with Source but zero Nodes, and no error at all.
func TestParseErroDeSintaxeViraParseErrors(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "erro_sintaxe.conf"),
	})

	require.Error(t, err)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "the returned error has to be (or wrap) config.ParseErrors")
	require.NotEmpty(t, problemas)
	require.NotEmpty(t, problemas[0].File)
	require.NotZero(t, problemas[0].Line)
}

// An include pointing at a missing file is the same kind of silent defect:
// crossplane flags the problem in the file that does the include, generating
// no Config at all for the absent file and returning no error through the
// normal channel.
func TestParseIncludeQuebradoViraParseErrors(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "include_quebrado.conf"),
	})

	require.Error(t, err)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "the returned error has to be (or wrap) config.ParseErrors")
	require.NotEmpty(t, problemas)
	require.NotEmpty(t, problemas[0].File)
	require.NotZero(t, problemas[0].Line)
}

// ParseOptions.Open is the only diskless test hook of this package: it has to
// be exercised against an in-memory filesystem, and it has to be the only
// source of reads -- a path missing from the in-memory FS has to fail even
// when it really does exist on disk.
func TestParseComFilesystemEmMemoria(t *testing.T) {
	memFS := map[string][]byte{
		"mem/nginx.conf": []byte("worker_processes auto;\n"),
	}
	abrir := func(path string) (io.ReadCloser, error) {
		b, ok := memFS[path]
		if !ok {
			return nil, fmt.Errorf("file does not exist in the in-memory fs: %s", path)
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}

	tree, err := config.Parse(config.ParseOptions{
		Path: "mem/nginx.conf",
		Open: abrir,
	})
	require.NoError(t, err)
	require.Len(t, tree.Files, 1)
	require.Equal(t, memFS["mem/nginx.conf"], tree.Files[0].Source)

	// simples.conf really does exist on disk, but is not in the in-memory
	// FS: the injected Open has to be the only source, with no fallback.
	_, err = config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "simples.conf"),
		Open: abrir,
	})
	require.Error(t, err)
}

// An include with a wildcard must not escape the injected filesystem. Without
// ParseOptions.Glob, crossplane falls back to filepath.Glob and matches the
// pattern against the LOCAL disk: pointed at a remote host, ngx would read
// conf.d/*.conf from the operator's machine and present that as the server's
// configuration.
//
// The test sets both things up at once, in the SAME directory: an in-memory
// filesystem with two files matching the pattern, and a third file, on the
// real disk only, that matches the same pattern and is not in memory. If the
// disk Glob wins, that third file gets into the list and the injected Open
// fails to open it.
func TestParseIncludeComCuringaNaoVazaParaODiscoLocal(t *testing.T) {
	dir := t.TempDir()
	confD := filepath.Join(dir, "conf.d")
	require.NoError(t, os.MkdirAll(confD, 0o755))

	// on the real disk only: no read may reach this file.
	discoLocal := filepath.Join(confD, "disco-local.conf")
	require.NoError(t, os.WriteFile(discoLocal, []byte("worker_shutdown_timeout 1s;\n"), 0o644))

	topo := filepath.Join(dir, "nginx.conf")
	memFS := map[string][]byte{
		topo:                               []byte("include conf.d/*.conf;\n"),
		filepath.Join(confD, "a-mem.conf"): []byte("worker_processes 2;\n"),
		filepath.Join(confD, "b-mem.conf"): []byte("worker_rlimit_nofile 1024;\n"),
	}

	opts := config.ParseOptions{
		Path: topo,
		Open: func(path string) (io.ReadCloser, error) {
			b, ok := memFS[path]
			if !ok {
				return nil, fmt.Errorf("file does not exist in the in-memory fs: %s", path)
			}
			return io.NopCloser(bytes.NewReader(b)), nil
		},
		Glob: func(pattern string) ([]string, error) {
			var casados []string
			for path := range memFS {
				ok, err := filepath.Match(pattern, path)
				if err != nil {
					return nil, err
				}
				if ok {
					casados = append(casados, path)
				}
			}
			sort.Strings(casados)
			return casados, nil
		},
	}

	tree, err := config.Parse(opts)
	require.NoError(t, err)

	var lidos []string
	for _, f := range tree.Files {
		lidos = append(lidos, f.Path)
	}
	sort.Strings(lidos)
	require.Equal(t, []string{
		filepath.Join(confD, "a-mem.conf"),
		filepath.Join(confD, "b-mem.conf"),
		topo,
	}, lidos)
	require.NotContains(t, lidos, discoLocal)
}

// The json:"-" tag on File.Source is the only shield against the raw bytes of
// the .conf -- where private key paths appear as plain text -- leaking into
// the JSON output underneath redaction, which only acts on the arguments.
// This test pins the serialized shape of File so that accidentally removing
// the tag breaks the build.
func TestFileNaoSerializaSource(t *testing.T) {
	f := &config.File{
		Path:   "example.conf",
		Source: []byte("secret-that-must-not-leak"),
		Nodes:  []*config.Node{},
	}

	b, err := json.Marshal(f)
	require.NoError(t, err)
	require.NotContains(t, string(b), "secret-that-must-not-leak")

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	chaves := make([]string, 0, len(m))
	for k := range m {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	require.Equal(t, []string{"file", "parsed"}, chaves)
}

// leitorLento hands the data bytes over in small chunks with a pause between
// each read. A small file read from the real disk usually arrives whole (or
// nearly so) in a single Read call of bufio.Scanner, so organic timing does
// not guarantee that the lexer goroutine is still reading at the moment the
// parser hits the error and closes the file -- the artificial pause makes that
// overlap practically certain, instead of relying on scheduler luck.
type leitorLento struct {
	dados []byte
	pos   int
}

func (l *leitorLento) Read(p []byte) (int, error) {
	if l.pos >= len(l.dados) {
		return 0, io.EOF
	}
	time.Sleep(200 * time.Microsecond)

	fim := l.pos + 32
	if fim > len(l.dados) {
		fim = len(l.dados)
	}
	n := copy(p, l.dados[l.pos:fim])
	l.pos += n
	return n, nil
}

func (l *leitorLento) Close() error { return nil }

// An "include;" with no argument makes crossplane's parser return
// immediately, abandoning the lexer goroutine of that file -- which keeps
// reading from the same reader (the fixture has far more than 2048 tokens
// after the broken include, the capacity of the token channel). The file's
// Close() runs in the middle of that. Before round 2 of the fix,
// leituraEspelhada had no mutex of its own on the buffer, and this real
// concurrency produced a data race under -race -- reproduced by hand by
// reverting leituraEspelhada to the round 1 version and running this very
// test. This test only pins the regression when run with go test -race.
func TestParseComIncludeSemArgumentosNaoTemDataRace(t *testing.T) {
	dados, err := os.ReadFile(filepath.Join("testdata", "include_sem_args.conf"))
	require.NoError(t, err)

	abrir := func(path string) (io.ReadCloser, error) {
		return &leitorLento{dados: dados}, nil
	}

	_, err = config.Parse(config.ParseOptions{
		Path: "include_sem_args.conf",
		Open: abrir,
	})

	// An include with no argument is, by itself, a problem crossplane
	// reports through Status/Errors -- so an error here is expected. What
	// this test pins is the absence of a data race, not the error value.
	require.Error(t, err)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas))
}

// leitorComFalha returns a fixed number of bytes and then starts returning a
// real I/O error (not io.EOF) on every subsequent call, simulating a failure
// midway through reading a file -- e.g. a network FS that drops after
// delivering the first few lines.
// erro, when set, replaces the default error -- it serves the tests that need
// to recognize the raw runtime string in the output (or to prove it does not
// show up).
type leitorComFalha struct {
	restante []byte
	erro     error
}

func (l *leitorComFalha) Read(p []byte) (int, error) {
	if len(l.restante) == 0 {
		if l.erro != nil {
			return 0, l.erro
		}
		return 0, errors.New("simulated i/o failure in the middle of the file")
	}
	n := copy(p, l.restante)
	l.restante = l.restante[n:]
	return n, nil
}

func (l *leitorComFalha) Close() error { return nil }

// Before round 2 of the fix, leituraEspelhada.Close unconditionally wrote
// whatever was in the buffer, even when the underlying read had ended in a
// real error instead of io.EOF. Crossplane, in turn, never consults
// scanner.Err(), so that I/O error ended the tokenization silently -- and
// config.Parse would return a Tree with a truncated Source and err == nil. A
// truncated Source is more dangerous than the original defect of round 1: the
// spans of Task 9 would be coherent with that Source, and a write-back by
// byte replacement would truncate the user's real file.
func TestParseFalhaDeIONaoTruncaSourceSilenciosamente(t *testing.T) {
	primeiraLinha := "worker_processes auto;\n"

	abrir := func(path string) (io.ReadCloser, error) {
		return &leitorComFalha{restante: []byte(primeiraLinha)}, nil
	}

	tree, err := config.Parse(config.ParseOptions{
		Path: "qualquer/nginx.conf",
		Open: abrir,
	})

	require.Error(t, err, "an i/o failure in the middle of the file has to propagate as an error, not produce a Tree with a truncated Source and a nil err")
	require.Nil(t, tree)
}

// Before round 3 of the fix, a read that failed midway but whose fallback
// re-read succeeded -- a transient I/O failure -- produced err == nil, a
// complete Source (from the successful re-read) and Nodes containing only the
// prefix the lexer reached before the original error: a partial tree with
// silent success. This test uses an Open that fails on the first read but
// would succeed on a second one, to prove that lerFonte propagates the
// recorded error instead of re-reading the file.
func TestParseFalhaDeIOTransitoriaNaoViraArvoreParcial(t *testing.T) {
	completo := []byte("worker_processes auto;\nevents {\n    worker_connections 1024;\n}\n")
	primeiraLinha := completo[:len("worker_processes auto;\n")]

	var chamadas int
	abrir := func(path string) (io.ReadCloser, error) {
		chamadas++
		if chamadas == 1 {
			// first read: fails after the first line.
			return &leitorComFalha{restante: append([]byte{}, primeiraLinha...)}, nil
		}
		// if lerFonte re-read the file, this second read would succeed
		// completely -- and that is exactly what must not happen.
		return io.NopCloser(bytes.NewReader(completo)), nil
	}

	tree, err := config.Parse(config.ParseOptions{
		Path: "qualquer/nginx.conf",
		Open: abrir,
	})

	require.Error(t, err, "a transient i/o failure has to propagate as an error, not produce a partial tree with silent success on a re-read")
	require.Nil(t, tree)
	require.Equal(t, 1, chamadas, "lerFonte must not re-read the file when there is already an error recorded for that path")
}

// An I/O failure in an INCLUDED file must not turn into a syntax error in the
// file that does the include. Crossplane, when opening an explicit include,
// turns the Open error into a ParseError located in the including file, on the
// include line -- and the message is the raw runtime string. If ngx forwards
// that, the consumer gets "error on line 2 of nginx.conf" for an intact
// nginx.conf and goes off debugging the wrong file.
//
// This stopped being hypothetical with remote access over SSH: nothing is
// installed on the server, so every config file is a network read (132 on one
// measured host) and the connection dropping in the middle of one of them is
// routine.
//
// The test proves the ATTRIBUTION, not merely that an error happened: the file
// pointed at, a class of its own, and the raw runtime string absent from the
// message.
func TestParseFalhaDeIOEmIncludeNaoCulpaOArquivoQueInclui(t *testing.T) {
	dir := t.TempDir()
	topo := filepath.Join(dir, "nginx.conf")
	incluido := filepath.Join(dir, "conf.d", "app.conf")

	fonte := map[string][]byte{
		topo:     []byte("worker_processes auto;\ninclude conf.d/app.conf;\n"),
		incluido: []byte("server {\n    listen 80;\n}\n"),
	}

	const cru = "read tcp 10.0.0.9:22: connection reset by peer"

	abrir := func(path string) (io.ReadCloser, error) {
		b, ok := fonte[path]
		if !ok {
			return nil, fmt.Errorf("file does not exist in the in-memory fs: %s", path)
		}
		if path == incluido {
			// delivers the first lines and drops midway, like an SSH
			// session dying while one of the files is being read.
			return &leitorComFalha{
				restante: append([]byte{}, b[:9]...),
				erro:     errors.New(cru),
			}, nil
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}

	tree, err := config.Parse(config.ParseOptions{Path: topo, Open: abrir})

	require.Error(t, err)
	require.Nil(t, tree)

	var problemas config.ParseErrors
	require.ErrorAs(t, err, &problemas)
	require.Len(t, problemas, 1)
	p := problemas[0]

	require.Equal(t, incluido, p.File,
		"the diagnostic has to point at the file that could not be read, not at the one doing the include")
	require.NotEqual(t, topo, p.File,
		"the file doing the include is intact: blaming it sends the consumer off debugging the wrong file")
	require.Equal(t, config.RecusaFalhaDeLeitura, p.Classe,
		"an I/O failure has to carry a class of its own, not come out as a crossplane refusal")
	require.NotContains(t, p.Message, cru,
		"the message is ours: it does not forward the raw Go runtime string")
	require.NotContains(t, p.Message, "connection reset")
}

// The same failure in the top-level file: crossplane returns the error
// directly and ngx used to wrap it in an fmt.Errorf with no class, carrying
// the raw runtime string into the diagnostic.
func TestParseFalhaDeIONoTopoTemClasseEMensagemPropria(t *testing.T) {
	const cru = "read tcp 10.0.0.9:22: connection reset by peer"

	abrir := func(path string) (io.ReadCloser, error) {
		return &leitorComFalha{
			restante: []byte("worker_processes auto;\n"),
			erro:     errors.New(cru),
		}, nil
	}

	tree, err := config.Parse(config.ParseOptions{Path: "remote/nginx.conf", Open: abrir})

	require.Error(t, err)
	require.Nil(t, tree)

	var problemas config.ParseErrors
	require.ErrorAs(t, err, &problemas)
	require.Len(t, problemas, 1)
	p := problemas[0]

	require.Equal(t, "remote/nginx.conf", p.File)
	require.Equal(t, config.RecusaFalhaDeLeitura, p.Classe)
	require.NotContains(t, p.Message, cru,
		"the message is ours: it does not forward the raw Go runtime string")
}

// Permission denied on an include is the case remote access made routine: on
// a remote target the connection user often cannot reach every file the
// server's root can. Measured against a production nginx: one file out of 128
// was unreadable by the connection user.
//
// The test demands the three things that were missing. That the diagnostic
// blames the file THAT FAILED, and not the top-level one including it --
// otherwise it sends you debugging the wrong place. That the cause shows up
// CLASSIFIED, because permission is fixed differently from a dropping
// connection. And that the raw runtime string does NOT leak, because it
// changes across systems and an agent branching on it breaks on its own.
func TestParsePermissaoNegadaEmIncludeAcusaOArquivoCerto(t *testing.T) {
	dir := t.TempDir()
	topo := filepath.Join(dir, "nginx.conf")
	incluido := filepath.Join(dir, "denied.conf")
	require.NoError(t, os.WriteFile(topo, []byte("include "+incluido+";\n"), 0o644))
	require.NoError(t, os.WriteFile(incluido, []byte("worker_processes 1;\n"), 0o644))

	_, err := config.Parse(config.ParseOptions{
		Path: topo,
		Open: func(path string) (io.ReadCloser, error) {
			if path == incluido {
				return nil, fmt.Errorf("opening %s: %w", path, fs.ErrPermission)
			}
			return os.Open(path)
		},
	})

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "it has to be ParseErrors, not a generic error")
	require.NotEmpty(t, problemas)

	p := problemas[0]
	require.Equal(t, incluido, p.File, "the diagnostic has to blame the file that failed")
	require.NotEqual(t, topo, p.File, "blaming the top-level file sends you debugging the wrong place")
	require.Contains(t, p.Message, "permission")
	require.NotContains(t, p.Message, "ErrPermission")
	require.NotContains(t, p.Message, "opening ")
	require.Zero(t, p.Line, "a file that never opened has no line to offer")
}
