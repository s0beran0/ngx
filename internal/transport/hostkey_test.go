package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/s0beran0/ngx/internal/output"
)

// Nenhum teste deste arquivo abre socket. O ssh.HostKeyCallback e chamado
// direto, com um net.Addr montado a mao — e assim que a politica de host key
// se testa sem servidor e sem rede.

const hostDeTeste = "10.0.0.9:22"

// gerarChave produz uma chave de host nova a cada chamada. Chave de teste e
// gerada no teste, nunca commitada: uma chave privada versionada e uma chave
// publicada.
func gerarChave(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return signer.PublicKey()
}

// escreverKnownHosts monta um known_hosts em t.TempDir() com as linhas dadas.
func escreverKnownHosts(t *testing.T, linhas ...string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "known_hosts")
	conteudo := strings.Join(linhas, "\n") + "\n"
	require.NoError(t, os.WriteFile(caminho, []byte(conteudo), 0o600))
	return caminho
}

// linhaKnownHosts formata a chave como o known_hosts espera, para o host de
// teste.
func linhaKnownHosts(key ssh.PublicKey) string {
	return knownhosts.Line([]string{knownhosts.Normalize(hostDeTeste)}, key)
}

// enderecoRemoto e o net.Addr que o handshake passaria ao callback.
func enderecoRemoto() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 22}
}

// diagnosticoDe extrai o Diagnostic de um erro do ngx. Falha o teste se o erro
// nao for um *output.Error — um erro cru nao tem mensagem propria e nao serve
// a nenhum dos quatro desfechos.
func diagnosticoDe(t *testing.T, err error) output.Diagnostic {
	t.Helper()
	require.Error(t, err)
	var e *output.Error
	require.ErrorAs(t, err, &e, "a recusa precisa carregar diagnostico proprio")
	return e.Diag
}

// --- Desfecho 1: a chave confere ---

func TestHostKeyChaveConfereNaoProduzDiagnostico(t *testing.T) {
	key := gerarChave(t)
	caminho := escreverKnownHosts(t, linhaKnownHosts(key))

	callback, diags, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)
	require.NotNil(t, diags, "a lista de diagnosticos nunca e nil")
	assert.Empty(t, diags, "o caminho feliz nao avisa nada")

	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), key))
}

// --- Desfecho 2: host desconhecido ---

func TestHostKeyHostDesconhecidoRecusaEEnsinaAAdicionar(t *testing.T) {
	registrada := gerarChave(t)
	// O arquivo existe e tem entradas, so nao para este host.
	caminho := escreverKnownHosts(t,
		knownhosts.Line([]string{"outro.exemplo"}, registrada))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)

	apresentada := gerarChave(t)
	diag := diagnosticoDe(t, callback(hostDeTeste, enderecoRemoto(), apresentada))

	assert.Equal(t, CodigoHostDesconhecido, diag.Code)
	assert.Equal(t, output.SeverityError, diag.Severity)
	assert.Contains(t, diag.Message, "10.0.0.9", "a mensagem diz qual host")
	assert.Contains(t, diag.Message, caminho, "a mensagem diz qual arquivo")
	assert.Contains(t, diag.Message, linhaKnownHosts(apresentada),
		"a mensagem entrega a linha pronta para o known_hosts")
	assert.Equal(t, caminho, diag.File)
}

// --- Desfecho 3: a chave mudou ---

func TestHostKeyChaveAlteradaAcusaPossivelAtaque(t *testing.T) {
	registrada := gerarChave(t)
	caminho := escreverKnownHosts(t, linhaKnownHosts(registrada))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)

	apresentada := gerarChave(t)
	diag := diagnosticoDe(t, callback(hostDeTeste, enderecoRemoto(), apresentada))

	assert.Equal(t, CodigoHostKeyAlterada, diag.Code)
	assert.Equal(t, output.SeverityError, diag.Severity)

	minuscula := strings.ToLower(diag.Message)
	assert.Contains(t, minuscula, "mudou", "a mensagem diz que a chave mudou")
	assert.Contains(t, minuscula, "ataque",
		"chave alterada e possivel ataque e a mensagem tem que dizer isso")
	assert.Contains(t, minuscula, "man-in-the-middle")
	assert.Contains(t, diag.Message, serializarChave(apresentada),
		"a mensagem mostra a chave apresentada")
	assert.Contains(t, diag.Message, serializarChave(registrada),
		"a mensagem mostra a chave registrada")
	assert.Equal(t, caminho, diag.File, "aponta o arquivo do registro que diverge")
	assert.Equal(t, 1, diag.Line, "aponta a linha do registro que diverge")
}

// TestHostKeyDesconhecidoEAlteradaSaoDistinguiveis e o teste que justifica a
// tarefa. Um teste que so verificasse "deu erro" nos dois casos passaria com a
// mesma mensagem nos dois — e quem le a saida nao teria como separar primeiro
// acesso de interceptacao, que e a unica distincao que importa aqui.
func TestHostKeyDesconhecidoEAlteradaSaoDistinguiveis(t *testing.T) {
	registrada := gerarChave(t)
	apresentada := gerarChave(t)

	caminhoDesconhecido := escreverKnownHosts(t,
		knownhosts.Line([]string{"outro.exemplo"}, registrada))
	caminhoAlterada := escreverKnownHosts(t, linhaKnownHosts(registrada))

	callbackDesconhecido, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: caminhoDesconhecido})
	require.NoError(t, err)
	callbackAlterada, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: caminhoAlterada})
	require.NoError(t, err)

	diagDesconhecido := diagnosticoDe(t, callbackDesconhecido(hostDeTeste, enderecoRemoto(), apresentada))
	diagAlterada := diagnosticoDe(t, callbackAlterada(hostDeTeste, enderecoRemoto(), apresentada))

	// 1. Distinguiveis por campo, que e como um agente separa os casos sem
	//    interpretar texto.
	assert.NotEqual(t, diagDesconhecido.Code, diagAlterada.Code,
		"os dois desfechos precisam de codigos diferentes")

	// 2. Distinguiveis por texto, que e como um humano os separa.
	assert.NotEqual(t, diagDesconhecido.Message, diagAlterada.Message)

	// 3. E a diferenca e a certa: so o caso de chave alterada fala em ataque,
	//    e so o caso de host desconhecido fala em primeiro acesso.
	assert.NotContains(t, strings.ToLower(diagDesconhecido.Message), "ataque",
		"primeiro acesso nao pode sequer mencionar ataque: quem filtra a saida por "+
			"essa palavra tem que pegar so o caso 3")
	assert.Contains(t, strings.ToLower(diagDesconhecido.Message), "primeiro acesso")
	assert.Contains(t, strings.ToLower(diagAlterada.Message), "ataque")
	assert.NotContains(t, strings.ToLower(diagAlterada.Message), "primeiro acesso",
		"chave trocada nao pode ser confundida com primeiro acesso")

	// 4. E ambos recusam de fato: distinguir sem recusar nao seria a DR1.
	assert.Equal(t, output.SeverityError, diagDesconhecido.Severity)
	assert.Equal(t, output.SeverityError, diagAlterada.Severity)
}

// --- Desfecho 4a: chave revogada ---

func TestHostKeyChaveRevogadaTemDesfechoProprio(t *testing.T) {
	revogada := gerarChave(t)
	valida := gerarChave(t)
	caminho := escreverKnownHosts(t,
		linhaKnownHosts(valida),
		"@revoked "+linhaKnownHosts(revogada))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)

	diag := diagnosticoDe(t, callback(hostDeTeste, enderecoRemoto(), revogada))

	assert.Equal(t, CodigoHostKeyRevogada, diag.Code)
	assert.NotEqual(t, CodigoHostDesconhecido, diag.Code)
	assert.NotEqual(t, CodigoHostKeyAlterada, diag.Code)
	assert.Contains(t, strings.ToLower(diag.Message), "revogada")
	assert.Equal(t, caminho, diag.File, "informa o arquivo da revogacao")
	assert.Equal(t, 2, diag.Line, "informa a linha da revogacao")

	// A mesma politica ainda aceita a chave valida do mesmo arquivo: a
	// revogacao nao contamina o resto.
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), valida))
}

// --- Desfecho 4b: known_hosts ausente ---

func TestHostKeyKnownHostsAusenteTemDesfechoProprio(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "known_hosts")

	callback, diags, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.Error(t, err, "sem arquivo nao ha o que comparar; o ngx recusa")
	assert.Nil(t, callback)
	assert.Empty(t, diags)

	diag := diagnosticoDe(t, err)
	assert.Equal(t, CodigoKnownHostsAusente, diag.Code)
	assert.NotEqual(t, CodigoHostDesconhecido, diag.Code,
		"arquivo ausente nao e a mesma coisa que host ausente do arquivo")
	assert.Contains(t, diag.Message, caminho)
	assert.Contains(t, diag.Message, "--insecure-host-key",
		"a mensagem diz qual e a saida")
	assert.Contains(t, diag.Message, "ssh 10.0.0.9",
		"a mensagem diz como registrar o host")

	// O PathError original continua acessivel para quem quiser inspecionar,
	// mas nao vaza cru na mensagem.
	var pathErr *fs.PathError
	assert.True(t, errors.As(err, &pathErr), "a causa original e preservada")
}

func TestHostKeyKnownHostsIlegivelNaoViraArquivoAusente(t *testing.T) {
	// Um diretorio no lugar do arquivo: existe, mas nao da para ler.
	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.Mkdir(caminho, 0o700))

	_, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.Error(t, err)

	diag := diagnosticoDe(t, err)
	assert.NotEqual(t, CodigoKnownHostsAusente, diag.Code,
		"arquivo ilegivel tem causa e solucao diferentes de arquivo ausente")
	assert.Contains(t, diag.Message, caminho)
}

// --- O escape da DR1 ---

func TestHostKeyInseguroAceitaQualquerChaveMasAvisa(t *testing.T) {
	callback, diags, err := VerificadorHostKey(SSHOptions{
		Host:            "10.0.0.9",
		InsecureHostKey: true,
	})
	require.NoError(t, err)
	require.NotNil(t, callback)

	// Aceita qualquer chave, inclusive uma nunca vista.
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), gerarChave(t)))
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), gerarChave(t)))

	// Mas nunca em silencio.
	require.Len(t, diags, 1, "usar o escape tem que aparecer na saida")
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, CodigoAvisoHostKeyInsegura, diags[0].Code)
	assert.Contains(t, diags[0].Message, "--insecure-host-key")
	assert.Contains(t, diags[0].Message, "10.0.0.9")
	assert.Contains(t, strings.ToLower(diags[0].Message), "man-in-the-middle",
		"o aviso diz o que se perdeu, nao so que uma flag foi usada")
}

func TestHostKeyInseguroIgnoraKnownHostsAusente(t *testing.T) {
	// O escape serve justamente a quem nao tem o host registrado: ele nao
	// pode esbarrar no arquivo ausente antes de tomar efeito.
	callback, diags, err := VerificadorHostKey(SSHOptions{
		Host:            "10.0.0.9",
		KnownHostsPath:  filepath.Join(t.TempDir(), "nao-existe"),
		InsecureHostKey: true,
	})
	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), gerarChave(t)))
}

// --- Contrato comum aos desfechos de recusa ---

func TestHostKeyRecusasUsamExitCodeDocumentado(t *testing.T) {
	registrada := gerarChave(t)
	apresentada := gerarChave(t)

	comChaveOutra := escreverKnownHosts(t, linhaKnownHosts(registrada))
	semOHost := escreverKnownHosts(t, knownhosts.Line([]string{"outro.exemplo"}, registrada))
	comRevogada := escreverKnownHosts(t, "@revoked "+linhaKnownHosts(apresentada))

	recusa := func(t *testing.T, caminho string) error {
		t.Helper()
		callback, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: caminho})
		require.NoError(t, err)
		return callback(hostDeTeste, enderecoRemoto(), apresentada)
	}

	casos := map[string]error{
		"host desconhecido": recusa(t, semOHost),
		"chave alterada":    recusa(t, comChaveOutra),
		"chave revogada":    recusa(t, comRevogada),
	}

	vistos := map[string]string{}
	for nome, err := range casos {
		// A v0.1 so documenta os exit codes que emite; recusa de host key
		// entra em 1 (interno/IO) ate haver codigo proprio documentado.
		assert.Equal(t, output.ExitInternal, output.CodeOf(err), nome)

		diag := diagnosticoDe(t, err)
		anterior, repetido := vistos[diag.Code]
		assert.False(t, repetido, "%s e %s dividem o codigo %s", nome, anterior, diag.Code)
		vistos[diag.Code] = nome
	}

	// Somando o known_hosts ausente, sao quatro codigos distintos.
	_, _, err := VerificadorHostKey(SSHOptions{
		KnownHostsPath: filepath.Join(t.TempDir(), "nao-existe"),
	})
	vistos[diagnosticoDe(t, err).Code] = "known_hosts ausente"
	assert.Len(t, vistos, 4, "os quatro desfechos de recusa sao distinguiveis por codigo")
}
