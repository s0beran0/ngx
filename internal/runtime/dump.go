package runtime

import (
	"context"
	"regexp"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
)

// DumpFile e um arquivo da configuracao efetiva, como o proprio nginx a
// enumera. Path e o caminho no alvo.
type DumpFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Dump e a configuracao efetiva devolvida por `nginx -T`: o conjunto de
// arquivos que o nginx de fato leria, ja com os includes resolvidos por ele.
//
// Medido num nginx de producao real, isso da 132 arquivos — o `-T` responde
// numa viagem so o que a leitura arquivo a arquivo responderia em 132.
type Dump struct {
	// OK espelha o codigo de saida: `nginx -T` testa antes de despejar, e
	// uma configuracao invalida sai diferente de zero e sem despejo algum.
	OK bool `json:"ok"`

	// ConfigFile e o arquivo de topo, quando o nginx o nomeia.
	ConfigFile string `json:"config_file,omitempty"`

	// Files nunca e nil.
	Files []DumpFile `json:"files"`

	// Diagnostics traz o que o nginx escreveu em stderr durante o teste que
	// precede o despejo. Nunca e nil.
	Diagnostics []output.Diagnostic `json:"diagnostics"`
}

// reMarcadorArquivo casa o cabecalho que o nginx emite antes de cada arquivo:
// "# configuration file /etc/nginx/nginx.conf:".
//
// O marcador so e reconhecido no inicio da linha e com o dois-pontos final,
// porque uma linha de comentario dentro de uma configuracao pode conter o
// mesmo texto e nao deve partir o arquivo em dois.
var reMarcadorArquivo = regexp.MustCompile(`^# configuration file (.+):$`)

// DumpConfig executa `nginx -T` no alvo e separa a saida em arquivos.
//
// Este e o comando que, medido num host real, falha para usuario comum e so
// funciona com sudo. Sem --sudo o ngx nao escala: executar devolve o erro de
// privilegio dizendo qual e o comando (DR5).
func (r *Runtime) DumpConfig(ctx context.Context) (*Dump, error) {
	e, err := r.executar(ctx, "-T")
	if err != nil {
		return nil, err
	}

	d := &Dump{
		OK: e.exit == 0,
		// O despejo vai para stdout; os diagnosticos, para stderr. Aqui os
		// canais sao separados de proposito: misturar poria linhas de
		// diagnostico dentro do conteudo de um arquivo.
		Files:       DividirDump(e.stdout),
		Diagnostics: ParseDiagnosticos(e.stderr),
		ConfigFile:  arquivoTestado(e.stderr),
	}
	if d.ConfigFile == "" {
		d.ConfigFile = arquivoTestado(e.stdout)
	}
	return d, nil
}

// DividirDump separa o stdout de `nginx -T` nos arquivos que o compoem.
//
// Como ParseDiagnosticos, recebe so o texto: o mesmo teste vale para bytes
// vindos de um pipe local ou de uma sessao SSH.
//
// Conteudo que apareca antes do primeiro marcador e descartado — nao pertence
// a arquivo nenhum, e atribui-lo ao primeiro seria inventar procedencia.
func DividirDump(texto string) []DumpFile {
	arquivos := []DumpFile{}
	if strings.TrimSpace(texto) == "" {
		return arquivos
	}

	var atual *DumpFile
	var conteudo strings.Builder

	fecha := func() {
		if atual != nil {
			atual.Content = conteudo.String()
			arquivos = append(arquivos, *atual)
		}
		conteudo.Reset()
	}

	// Uma quebra final e artefato do proprio despejo, nao linha em branco do
	// ultimo arquivo: sem tirar aqui, o conteudo do ultimo arquivo sairia com
	// uma linha vazia a mais que os demais.
	for _, linha := range strings.Split(strings.TrimSuffix(texto, "\n"), "\n") {
		sem := strings.TrimRight(linha, "\r")
		if m := reMarcadorArquivo.FindStringSubmatch(sem); m != nil {
			fecha()
			atual = &DumpFile{Path: m[1]}
			continue
		}
		if atual == nil {
			continue
		}
		conteudo.WriteString(sem)
		conteudo.WriteString("\n")
	}
	fecha()

	return arquivos
}
