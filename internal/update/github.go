package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/s0beran0/ngx/internal/output"
)

// RepoPadrao e o repositorio consultado. Fica aqui, e nao numa flag, porque
// apontar o update para outro repositorio seria apontar para outra origem de
// binario — decisao de build, nao de linha de comando.
const RepoPadrao = "s0beran0/ngx"

// BaseURLPadrao e a raiz da API do GitHub. Sobrescrita apenas em teste.
const BaseURLPadrao = "https://api.github.com"

// Nomes dos artefatos de verificacao gerados pelo goreleaser (DD2).
const (
	NomeChecksums  = "checksums.txt"
	NomeAssinatura = "checksums.txt.minisig"
)

// limiteDownload limita o que aceitamos ler da rede para a memoria. Um
// artefato do ngx tem poucas dezenas de megabytes; sem teto, um servidor
// hostil (ou um redirect para o lugar errado) derruba a maquina por consumo
// de memoria em vez de falhar.
const limiteDownload = 128 << 20 // 128 MiB

// Asset e um artefato anexado a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release e uma release do GitHub, no subconjunto de campos que o update usa.
type Release struct {
	Version    string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

// AssetPorNome devolve o artefato de nome exato.
func (r *Release) AssetPorNome(nome string) (Asset, error) {
	for _, a := range r.Assets {
		if a.Name == nome {
			return a, nil
		}
	}
	return Asset{}, erro(CodigoAssetAusente,
		"a release %s nao traz o arquivo %q, entao nao ha como verificar o download; "+
			"artefatos publicados: %s", r.Version, nome, listaDeNomes(r.Assets))
}

// AssetDaPlataforma escolhe o artefato do sistema e arquitetura dados. A
// escolha e por sufixo (_<so>_<arch>.<ext>) em vez de nome montado a partir
// da versao: o template de nome do goreleaser pode mudar sem que o update
// precise ser reescrito.
func (r *Release) AssetDaPlataforma(so, arch string) (Asset, error) {
	sufixos := []string{
		fmt.Sprintf("_%s_%s.tar.gz", so, arch),
		fmt.Sprintf("_%s_%s.zip", so, arch),
	}
	for _, s := range sufixos {
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, s) {
				return a, nil
			}
		}
	}
	return Asset{}, erro(CodigoAssetAusente,
		"a release %s nao traz artefato para %s/%s; artefatos publicados: %s",
		r.Version, so, arch, listaDeNomes(r.Assets))
}

func listaDeNomes(assets []Asset) string {
	if len(assets) == 0 {
		return "nenhum"
	}
	nomes := make([]string, 0, len(assets))
	for _, a := range assets {
		nomes = append(nomes, a.Name)
	}
	return strings.Join(nomes, ", ")
}

// Cliente fala com a API do GitHub.
type Cliente struct {
	HTTP    *http.Client
	BaseURL string
	Repo    string
}

// NovoCliente devolve um cliente com o timeout dado. Timeout zero significa
// "sem limite proprio": quem chama controla pelo contexto, que e como o
// --timeout global do CLI chega aqui.
func NovoCliente(timeout time.Duration) *Cliente {
	return &Cliente{
		HTTP:    &http.Client{Timeout: timeout},
		BaseURL: BaseURLPadrao,
		Repo:    RepoPadrao,
	}
}

func (c *Cliente) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Cliente) base() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return BaseURLPadrao
}

func (c *Cliente) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return RepoPadrao
}

// Latest resolve a release do canal.
//
// Stable usa /releases/latest, que o proprio GitHub ja filtra para excluir
// pre-lancamentos e rascunhos. Beta usa /releases e pega a primeira entrada
// nao-rascunho, porque a API devolve ordenado por criacao decrescente — e o
// canal beta aceita tanto um pre-lancamento quanto um stable, o que vier
// primeiro (DD1).
func (c *Cliente) Latest(ctx context.Context, canal Channel) (*Release, error) {
	if canal == ChannelBeta {
		var releases []Release
		url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", c.base(), c.repo())
		if err := c.pegarJSON(ctx, url, &releases); err != nil {
			return nil, err
		}
		for i := range releases {
			if releases[i].Draft {
				continue
			}
			return normalizar(&releases[i]), nil
		}
		return nil, erro(CodigoReleaseAusente,
			"o canal beta nao tem nenhuma release publicada em %s", c.repo())
	}

	var rel Release
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.base(), c.repo())
	if err := c.pegarJSON(ctx, url, &rel); err != nil {
		return nil, err
	}
	return normalizar(&rel), nil
}

// PorVersao resolve uma versao especifica pela tag. E o caminho de downgrade
// e o de mudanca deliberada de versao: nunca acontece sem alguem pedir.
func (c *Cliente) PorVersao(ctx context.Context, versao string) (*Release, error) {
	tag := strings.TrimSpace(versao)
	if tag == "" {
		return nil, output.Usage("informe a versao no formato vX.Y.Z")
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	var rel Release
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.base(), c.repo(), tag)
	if err := c.pegarJSON(ctx, url, &rel); err != nil {
		return nil, err
	}
	return normalizar(&rel), nil
}

// Baixar le um artefato inteiro para a memoria, respeitando o teto.
func (c *Cliente) Baixar(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, erroCausa(err, CodigoRede, "url de download invalida: %s", url)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "ngx-update")

	resp, err := c.http().Do(req)
	if err != nil {
		return nil, erroCausa(err, CodigoRede,
			"nao foi possivel baixar %s: a rede falhou ou o tempo limite estourou", url)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, erro(CodigoRede,
			"o download de %s devolveu HTTP %d", url, resp.StatusCode)
	}

	dados, err := io.ReadAll(io.LimitReader(resp.Body, limiteDownload+1))
	if err != nil {
		return nil, erroCausa(err, CodigoRede,
			"o download de %s foi interrompido antes do fim", url)
	}
	if int64(len(dados)) > limiteDownload {
		return nil, erro(CodigoRede,
			"o download de %s passou do limite de %d bytes e foi abortado",
			url, int64(limiteDownload))
	}
	return dados, nil
}

func (c *Cliente) pegarJSON(ctx context.Context, url string, destino any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return erroCausa(err, CodigoRede, "url invalida: %s", url)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ngx-update")

	resp, err := c.http().Do(req)
	if err != nil {
		return erroCausa(err, CodigoRede,
			"nao foi possivel consultar a API do GitHub em %s: a rede falhou ou o "+
				"tempo limite estourou", url)
	}
	defer resp.Body.Close()

	if err := statusDaAPI(resp, url); err != nil {
		return err
	}

	corpo, err := io.ReadAll(io.LimitReader(resp.Body, limiteDownload+1))
	if err != nil {
		return erroCausa(err, CodigoRede, "a resposta de %s foi interrompida", url)
	}
	if err := json.Unmarshal(corpo, destino); err != nil {
		return erroCausa(err, CodigoRede,
			"a resposta de %s nao e o JSON esperado da API do GitHub", url)
	}
	return nil
}

// statusDaAPI traduz o status HTTP. O 403 com X-RateLimit-Remaining: 0 tem
// tratamento proprio porque e a falha mais provavel em uso real — a API do
// GitHub sem autenticacao permite 60 chamadas por hora por IP — e um erro
// generico mandaria a pessoa investigar a rede ou o repositorio.
func statusDaAPI(resp *http.Response, url string) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return erro(CodigoRateLimit,
				"o limite de requisicoes da API do GitHub foi atingido%s. Nao e "+
					"problema do ngx nem da sua rede: a API sem autenticacao permite "+
					"60 chamadas por hora por IP. Espere a janela reabrir ou baixe a "+
					"versao nova manualmente da pagina de releases",
				quandoReabre(resp))
		}
		return erro(CodigoRede, "a API do GitHub recusou a consulta a %s com HTTP %d",
			url, resp.StatusCode)
	case http.StatusNotFound:
		// O 404 em /releases/latest tem uma causa comum e nada obvia: o
		// endpoint EXCLUI pre-lancamentos, entao um projeto que so publicou
		// release candidate responde 404 mesmo tendo releases. Sem dizer
		// isso, quem le conclui que o projeto nao publica nada.
		if strings.HasSuffix(url, "/releases/latest") {
			return erro(CodigoReleaseAusente,
				"nenhuma release estavel encontrada em %s. Este endpoint ignora "+
					"pre-lancamentos: se o projeto so publicou versoes -rc ou -beta, "+
					"use --channel beta (ou NGX_CHANNEL=beta) para alcanca-las", url)
		}
		return erro(CodigoReleaseAusente,
			"a API do GitHub nao encontrou %s: a versao pedida pode nao existir ou "+
				"nao ter release publicada", url)
	default:
		return erro(CodigoRede, "a API do GitHub devolveu HTTP %d para %s",
			resp.StatusCode, url)
	}
}

func quandoReabre(resp *http.Response) string {
	reset := resp.Header.Get("X-RateLimit-Reset")
	if reset == "" {
		return ""
	}
	var epoch int64
	if _, err := fmt.Sscanf(reset, "%d", &epoch); err != nil || epoch <= 0 {
		return ""
	}
	return " (a janela reabre em " + time.Unix(epoch, 0).UTC().Format(time.RFC3339) + ")"
}

// normalizar garante que Assets nunca seja nil: uma lista nula serializaria
// como null e quebraria quem faz .length na saida.
func normalizar(r *Release) *Release {
	if r.Assets == nil {
		r.Assets = []Asset{}
	}
	return r
}

// Latest consulta o repositorio oficial com o cliente padrao. E a assinatura
// que a Task D4 especifica; quem precisa de timeout ou de outro endpoint usa
// Cliente diretamente.
func Latest(ctx context.Context, canal Channel) (*Release, error) {
	return NovoCliente(0).Latest(ctx, canal)
}
