package config

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"path/filepath"
	"strconv"
)

// Hash devolve o hash canonico da arvore.
//
// O que o hash protege e o significado, nao o texto: comentarios e espacamento
// ficam de fora, entao rodar fmt nao invalida os IDs que o agente esta
// segurando. Ja a ordem dos blocos entra, porque mover um server muda a que
// no cada ID se refere.
func Hash(t *Tree) string {
	h := sha256.New()
	for _, f := range t.Files {
		// So o nome base entra no hash, nao o caminho absoluto: mover a
		// configuracao de diretorio nao muda seu significado, e o caminho
		// absoluto varia por ambiente (t.TempDir() nos testes, por exemplo).
		escreverCampo(h, filepath.Base(f.Path))
		escreverNodes(h, f.Nodes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func escreverNodes(h hash.Hash, nodes []*Node) {
	for _, n := range nodes {
		if n.IsComment() {
			continue
		}
		escreverCampo(h, n.Directive)
		escreverCampo(h, strconv.Itoa(len(n.Args)))
		for _, a := range n.Args {
			escreverCampo(h, a)
		}
		if n.HasBlock() {
			escreverCampo(h, "{")
			escreverNodes(h, n.Block)
			escreverCampo(h, "}")
		} else {
			escreverCampo(h, ";")
		}
	}
}

// escreverCampo usa um separador que nao pode aparecer numa diretiva, para
// que "ab c" e "a bc" nunca colidam.
func escreverCampo(h hash.Hash, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
