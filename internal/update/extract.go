package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"path"
	"strings"
)

// limiteBinario caps what we accept to extract from the archive into memory.
// A small compressed file can declare a huge member; with no ceiling,
// extraction becomes unbounded memory consumption.
const limiteBinario = 256 << 20 // 256 MiB

// ExtrairBinario takes the executable out of the archive published in the
// release. goreleaser packages tar.gz on Unix and zip on Windows.
//
// Extraction happens ONLY AFTER Verify: decompressing unauthenticated data is
// handing the attacker a parser to attack before the signature has even been
// checked.
func ExtrairBinario(nomeArquivo string, dados []byte, so string) ([]byte, error) {
	alvo := "ngx"
	if so == "windows" {
		alvo = "ngx.exe"
	}

	switch {
	case strings.HasSuffix(nomeArquivo, ".tar.gz"), strings.HasSuffix(nomeArquivo, ".tgz"):
		return doTarGz(nomeArquivo, dados, alvo)
	case strings.HasSuffix(nomeArquivo, ".zip"):
		return doZip(nomeArquivo, dados, alvo)
	default:
		return nil, erro(CodigoArtefatoInvalido,
			"the artifact %s is not in a format the update knows how to open (.tar.gz "+
				"or .zip); download the new version manually", nomeArquivo)
	}
}

func doTarGz(nomeArquivo string, dados []byte, alvo string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(dados))
	if err != nil {
		return nil, erroCausa(err, CodigoArtefatoInvalido,
			"the artifact %s cannot be decompressed", nomeArquivo)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"the artifact %s is truncated or corrupted", nomeArquivo)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != alvo {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, limiteBinario+1))
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"could not read %s from inside %s", alvo, nomeArquivo)
		}
		if int64(len(bin)) > limiteBinario {
			return nil, erro(CodigoArtefatoInvalido,
				"the binary inside %s went past the accepted size limit", nomeArquivo)
		}
		return bin, nil
	}
	return nil, erro(CodigoArtefatoInvalido,
		"the artifact %s does not contain the executable %s", nomeArquivo, alvo)
}

func doZip(nomeArquivo string, dados []byte, alvo string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(dados), int64(len(dados)))
	if err != nil {
		return nil, erroCausa(err, CodigoArtefatoInvalido,
			"the artifact %s cannot be opened as a zip", nomeArquivo)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != alvo {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"could not open %s inside %s", alvo, nomeArquivo)
		}
		bin, err := io.ReadAll(io.LimitReader(rc, limiteBinario+1))
		rc.Close()
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"could not read %s from inside %s", alvo, nomeArquivo)
		}
		if int64(len(bin)) > limiteBinario {
			return nil, erro(CodigoArtefatoInvalido,
				"the binary inside %s went past the accepted size limit", nomeArquivo)
		}
		return bin, nil
	}
	return nil, erro(CodigoArtefatoInvalido,
		"the artifact %s does not contain the executable %s", nomeArquivo, alvo)
}
