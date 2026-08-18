// Package update implementa a auto-atualizacao do ngx: resolve a release do
// canal pedido, baixa o artefato, verifica assinatura e checksum, e so entao
// troca o binario em disco.
//
// A ordem acima e a garantia central do pacote. Nada e escrito por cima do
// binario em uso antes de a verificacao passar: o download vai para um
// arquivo temporario no mesmo diretorio e a troca e um rename. Um download
// interrompido, um artefato adulterado ou um binario construido sem chave
// publica embutida terminam com o ngx atual intacto.
package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/s0beran0/ngx/internal/output"
)

// Codigos de diagnostico do update. Cada modo de falha tem o seu: um "falhou"
// generico manda quem le procurar no lugar errado.
const (
	CodigoSemChavePublica    = "NGX-0301"
	CodigoChavePlaceholder   = "NGX-0302"
	CodigoChaveInvalida      = "NGX-0303"
	CodigoAssinaturaInvalida = "NGX-0304"
	CodigoChecksumAusente    = "NGX-0305"
	CodigoChecksumDivergente = "NGX-0306"
	CodigoRateLimit          = "NGX-0307"
	CodigoReleaseAusente     = "NGX-0308"
	CodigoAssetAusente       = "NGX-0309"
	CodigoPermissao          = "NGX-0310"
	CodigoTrocaFalhou        = "NGX-0311"
	CodigoCanalInvalido      = "NGX-0312"
	CodigoDowngrade          = "NGX-0313"
	CodigoRede               = "NGX-0314"
	CodigoArtefatoInvalido   = "NGX-0315"
)

// ChavePublica e a chave publica minisign embutida no binario (DD2/DD3).
//
// ATENCAO — PLACEHOLDER: A CHAVE PUBLICA REAL AINDA NAO EXISTE.
//
// Nenhum par de chaves foi gerado para o projeto ate aqui, entao esta
// variavel nasce VAZIA de proposito. Vazia significa "este binario nao sabe
// verificar nada", e Verify RECUSA atualizar nesse estado — jamais segue sem
// verificar. Nao preencha com um valor plausivel para "destravar o fluxo":
// uma chave que parece real passa despercebida em review e vai para producao
// dando falsa garantia de assinatura.
//
// Quando o par existir, o valor entra no build via -ldflags -X (ver
// .goreleaser.yaml). O ponto de injecao planejado e output.PublicKey (Task
// D2), que ainda nao existe; enquanto nao existir, a fiacao do comando deve
// atribuir o valor a esta variavel na inicializacao. A chave nunca e baixada
// em tempo de execucao (DD3).
var ChavePublica = ""

// PlaceholderChavePublica e o texto que sinaliza "chave ainda nao gerada".
// Existe para que um placeholder esquecido em algum lugar da cadeia de build
// falhe com mensagem propria em vez de virar erro de parse obscuro.
const PlaceholderChavePublica = "CHAVE-MINISIGN-PENDENTE-NAO-GERADA"

// Channel e o canal de atualizacao. Os canais sao derivados do semver da tag
// (DD1), nao de branches: "v0.2.0" e stable, "v0.2.0-rc.1" e pre-lancamento.
// EnvCanal e a variavel que o install.sh ja usa para escolher o canal. O
// `ngx update` a honra pelo mesmo motivo: quem instalou pelo beta espera
// continuar no beta sem repetir a flag a cada atualizacao.
const EnvCanal = "NGX_CHANNEL"

type Channel string

const (
	// ChannelStable so aceita releases nao pre-lancamento.
	ChannelStable Channel = "stable"
	// ChannelBeta aceita tambem pre-lancamentos.
	ChannelBeta Channel = "beta"
)

// ParseChannel converte texto em Channel. Canal desconhecido e erro de uso:
// aceitar um valor qualquer silenciosamente colocaria o usuario num canal que
// ele nao pediu.
func ParseChannel(s string) (Channel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ChannelStable):
		return ChannelStable, nil
	case string(ChannelBeta):
		return ChannelBeta, nil
	default:
		return "", output.Usage(
			"canal desconhecido %q: os canais validos sao \"stable\" e \"beta\"", s)
	}
}

// CanalDoAmbiente le NGX_CHANNEL. Recebe a funcao de leitura para poder ser
// testada sem mexer no ambiente do processo.
func CanalDoAmbiente(getenv func(string) string) (Channel, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseChannel(getenv("NGX_CHANNEL"))
}

// Opcoes descreve uma execucao de update.
type Opcoes struct {
	// Canal e o canal consultado quando Versao esta vazia.
	Canal Channel
	// Versao, quando preenchida, instala exatamente aquela versao — inclusive
	// mais antiga que a atual. E o unico caminho para downgrade: sem ela, uma
	// release mais antiga nunca e aplicada.
	Versao string
	// VersaoAtual e a versao deste binario (output.Version).
	VersaoAtual string
	// CaminhoBinario e o executavel a ser substituido. Vazio usa
	// os.Executable().
	CaminhoBinario string
	// ChavePublica sobrescreve a chave embutida. Existe para teste; em
	// producao fica vazia e o pacote usa ChavePublica.
	ChavePublicaOverride string
	// Cliente e o cliente da API do GitHub. Vazio usa o padrao.
	Cliente *Cliente
	// SomenteVerificar nao baixa nem troca nada: so reporta se ha versao nova.
	SomenteVerificar bool
	// SO e Arch selecionam o artefato. Vazios usam os do processo.
	SO   string
	Arch string
}

// Resultado e o que o comando reporta. Os nomes JSON seguem o que a Task D4
// especifica para o campo data do envelope.
type Resultado struct {
	VersaoAtual  string  `json:"current_version"`
	VersaoRemota string  `json:"latest_version"`
	Canal        Channel `json:"channel"`
	Atualizado   bool    `json:"updated"`
	// Disponivel e verdadeiro quando ha versao mais nova que a atual. Com
	// SomenteVerificar, e a unica informacao que interessa.
	Disponivel bool `json:"update_available"`
}

// Executar resolve, baixa, verifica e troca o binario. E a funcao que o
// comando `ngx update` chama; ela nao imprime nada e nao escolhe exit code.
func Executar(ctx context.Context, opts Opcoes) (*Resultado, error) {
	canal := opts.Canal
	if canal == "" {
		canal = ChannelStable
	}
	if canal != ChannelStable && canal != ChannelBeta {
		return nil, output.Usage(
			"canal desconhecido %q: os canais validos sao \"stable\" e \"beta\"", canal)
	}

	cli := opts.Cliente
	if cli == nil {
		cli = NovoCliente(0)
	}

	chave := opts.ChavePublicaOverride
	if chave == "" {
		chave = ChavePublica
	}
	// A chave e conferida ANTES de qualquer download: um binario que nao pode
	// verificar nada nao deveria nem comecar a baixar. So --check escapa,
	// porque ele nao troca binario nenhum.
	if !opts.SomenteVerificar {
		if err := validarChave(chave); err != nil {
			return nil, err
		}
	}

	rel, err := resolverRelease(ctx, cli, canal, opts.Versao)
	if err != nil {
		return nil, err
	}

	res := &Resultado{
		VersaoAtual:  opts.VersaoAtual,
		VersaoRemota: rel.Version,
		Canal:        canal,
	}

	explicita := opts.Versao != ""
	res.Disponivel = maisNova(rel.Version, opts.VersaoAtual)

	if opts.SomenteVerificar {
		return res, nil
	}

	if !explicita {
		// Sem --version, so avanca. Nunca voltar de versao por acidente: se a
		// release do canal for mais antiga (ou igual), o update e no-op.
		if !res.Disponivel {
			return res, nil
		}
	} else if mesmaVersao(rel.Version, opts.VersaoAtual) {
		// --version apontando para a versao ja instalada: nada a fazer, e nao
		// e erro.
		return res, nil
	}

	caminho := opts.CaminhoBinario
	if caminho == "" {
		caminho, err = os.Executable()
		if err != nil {
			return nil, output.Internal(err,
				"nao foi possivel descobrir o caminho do proprio binario para substituir")
		}
		if resolvido, errLink := filepath.EvalSymlinks(caminho); errLink == nil {
			caminho = resolvido
		}
	}

	so, arch := opts.SO, opts.Arch
	if so == "" {
		so = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}

	artefato, err := rel.AssetDaPlataforma(so, arch)
	if err != nil {
		return nil, err
	}
	somas, err := rel.AssetPorNome(NomeChecksums)
	if err != nil {
		return nil, err
	}
	assinatura, err := rel.AssetPorNome(NomeAssinatura)
	if err != nil {
		return nil, err
	}

	dadosArtefato, err := cli.Baixar(ctx, artefato.URL)
	if err != nil {
		return nil, err
	}
	dadosSomas, err := cli.Baixar(ctx, somas.URL)
	if err != nil {
		return nil, err
	}
	dadosAssinatura, err := cli.Baixar(ctx, assinatura.URL)
	if err != nil {
		return nil, err
	}

	if err := Verify(dadosArtefato, dadosSomas, dadosAssinatura, chave, artefato.Name); err != nil {
		return nil, err
	}

	novo, err := ExtrairBinario(artefato.Name, dadosArtefato, so)
	if err != nil {
		return nil, err
	}

	if err := Apply(caminho, novo); err != nil {
		return nil, err
	}

	res.Atualizado = true
	return res, nil
}

// validarChave recusa explicitamente o binario sem chave. Nao existe caminho
// de bypass: nenhuma variavel de ambiente, flag ou modo "sem verificacao".
func validarChave(chave string) error {
	c := strings.TrimSpace(chave)
	if c == "" {
		return erro(CodigoSemChavePublica,
			"este binario foi construido sem chave publica de verificacao embutida e "+
				"por isso nao pode se auto-atualizar: nao ha como provar que a release "+
				"baixada veio do projeto. Baixe a versao nova manualmente da pagina de "+
				"releases e confira `checksums.txt` com o minisign, ou use um binario "+
				"oficial, que ja vem com a chave embutida")
	}
	if c == PlaceholderChavePublica {
		return erro(CodigoChavePlaceholder,
			"a chave publica embutida ainda e o placeholder %q: nenhum par de chaves "+
				"minisign foi gerado para o projeto. Atualizar sem verificacao real nao "+
				"e uma opcao", PlaceholderChavePublica)
	}
	return nil
}

func resolverRelease(ctx context.Context, cli *Cliente, canal Channel, versao string) (*Release, error) {
	if versao != "" {
		return cli.PorVersao(ctx, versao)
	}
	return cli.Latest(ctx, canal)
}

// normalizarVersao devolve a versao no formato que golang.org/x/mod/semver
// espera (com "v"), ou "" se nao for semver valido.
func normalizarVersao(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// maisNova diz se remota e mais nova que atual. Versao atual ilegivel (build
// local sem -ldflags, por exemplo) conta como "qualquer release e mais nova":
// o contrario travaria o update de quem mais precisa dele.
func maisNova(remota, atual string) bool {
	r := normalizarVersao(remota)
	if r == "" {
		return false
	}
	a := normalizarVersao(atual)
	if a == "" {
		return true
	}
	return semver.Compare(r, a) > 0
}

func mesmaVersao(a, b string) bool {
	na, nb := normalizarVersao(a), normalizarVersao(b)
	if na == "" || nb == "" {
		return false
	}
	return semver.Compare(na, nb) == 0
}

func erro(codigo, format string, args ...any) *output.Error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     codigo,
			Message:  fmt.Sprintf(format, args...),
		},
	}
}

func erroCausa(causa error, codigo, format string, args ...any) *output.Error {
	e := erro(codigo, format, args...)
	e.Err = causa
	return e
}
