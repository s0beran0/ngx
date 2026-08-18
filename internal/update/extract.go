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

// limiteBinario limita o que aceitamos extrair do arquivo para a memoria. Um
// arquivo comprimido pequeno pode declarar um membro enorme; sem teto, a
// extracao vira consumo ilimitado de memoria.
const limiteBinario = 256 << 20 // 256 MiB

// ExtrairBinario tira o executavel do arquivo publicado na release. O
// goreleaser empacota tar.gz em Unix e zip no Windows.
//
// A extracao acontece SO DEPOIS de Verify: descomprimir dados nao
// autenticados e dar ao atacante um parser para atacar antes mesmo de a
// assinatura ser conferida.
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
			"o artefato %s nao esta num formato que o update saiba abrir (.tar.gz ou "+
				".zip); baixe a versao nova manualmente", nomeArquivo)
	}
}

func doTarGz(nomeArquivo string, dados []byte, alvo string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(dados))
	if err != nil {
		return nil, erroCausa(err, CodigoArtefatoInvalido,
			"o artefato %s nao pode ser descomprimido", nomeArquivo)
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
				"o artefato %s esta truncado ou corrompido", nomeArquivo)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != alvo {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, limiteBinario+1))
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"nao foi possivel ler %s de dentro de %s", alvo, nomeArquivo)
		}
		if int64(len(bin)) > limiteBinario {
			return nil, erro(CodigoArtefatoInvalido,
				"o binario dentro de %s passou do limite de tamanho aceito", nomeArquivo)
		}
		return bin, nil
	}
	return nil, erro(CodigoArtefatoInvalido,
		"o artefato %s nao contem o executavel %s", nomeArquivo, alvo)
}

func doZip(nomeArquivo string, dados []byte, alvo string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(dados), int64(len(dados)))
	if err != nil {
		return nil, erroCausa(err, CodigoArtefatoInvalido,
			"o artefato %s nao pode ser aberto como zip", nomeArquivo)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != alvo {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"nao foi possivel abrir %s dentro de %s", alvo, nomeArquivo)
		}
		bin, err := io.ReadAll(io.LimitReader(rc, limiteBinario+1))
		rc.Close()
		if err != nil {
			return nil, erroCausa(err, CodigoArtefatoInvalido,
				"nao foi possivel ler %s de dentro de %s", alvo, nomeArquivo)
		}
		if int64(len(bin)) > limiteBinario {
			return nil, erro(CodigoArtefatoInvalido,
				"o binario dentro de %s passou do limite de tamanho aceito", nomeArquivo)
		}
		return bin, nil
	}
	return nil, erro(CodigoArtefatoInvalido,
		"o artefato %s nao contem o executavel %s", nomeArquivo, alvo)
}
