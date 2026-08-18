package transport

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/s0beran0/ngx/internal/output"
)

// Codigos de diagnostico da politica de host key (DR1).
//
// Os codigos NGX-000N espelham exit codes e nao distinguem casos dentro de um
// mesmo codigo de saida. A recusa de host key precisa de identidade mais fina
// que isso — quem consome a saida tem de separar "primeiro acesso a este host"
// de "a chave do servidor mudou" sem interpretar texto —, entao os erros desta
// politica usam a faixa NGX-E###, e o aviso usa a faixa NGX-W### ja adotada
// pelo aviso de ~/.ssh/config.
//
// Os quatro codigos de erro sao mutuamente exclusivos e cada um tem uma
// mensagem propria. Colapsar dois deles apaga exatamente a informacao que
// justifica a politica existir.
const (
	// CodigoHostDesconhecido: o host nao esta no known_hosts. Atrito normal
	// de primeiro acesso.
	CodigoHostDesconhecido = "NGX-0201"

	// CodigoHostKeyAlterada: o host esta no known_hosts com outra chave.
	// Possivel interceptacao.
	CodigoHostKeyAlterada = "NGX-0202"

	// CodigoHostKeyRevogada: a chave apresentada esta marcada @revoked.
	CodigoHostKeyRevogada = "NGX-0203"

	// CodigoKnownHostsAusente: o arquivo known_hosts nao existe.
	CodigoKnownHostsAusente = "NGX-0204"

	// CodigoAlgoritmoNaoRegistrado: o host esta no known_hosts, mas so com
	// chaves de outro tipo. Nao e ataque nem primeiro acesso -- e negociacao
	// de algoritmo. Merece codigo proprio justamente para nao ser confundido
	// com NGX-0202, cuja mensagem fala em interceptacao.
	CodigoAlgoritmoNaoRegistrado = "NGX-0207"

	// CodigoAvisoHostKeyInsegura: --insecure-host-key foi usado e a
	// verificacao foi pulada.
	CodigoAvisoHostKeyInsegura = "NGX-0211"
)

// VerificadorHostKey monta o ssh.HostKeyCallback do ngx conforme a DR1: a
// chave do servidor e conferida contra o known_hosts do usuario e qualquer
// divergencia recusa a conexao.
//
// Devolve tres coisas porque ha tres momentos distintos:
//   - o callback, que classifica o que acontece durante o handshake;
//   - diagnosticos de construcao — hoje so o aviso de --insecure-host-key;
//   - erro de construcao, quando o known_hosts nao pode ser lido. O
//     knownhosts.New abre os arquivos na construcao, entao "arquivo ausente"
//     nunca chega ao callback: e erro aqui.
//
// O aviso do modo inseguro sai na construcao, e nao dentro do callback, por
// duas razoes: ele nao depende de qual chave o servidor apresenta, e um
// callback que escrevesse numa lista compartilhada seria disputa de dados com
// handshakes concorrentes.
func VerificadorHostKey(opts SSHOptions) (ssh.HostKeyCallback, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	if opts.InsecureHostKey {
		diags = append(diags, avisoHostKeyInsegura(opts.Host))
		// Aceita qualquer chave. O escape existe (DR1), mas nunca em
		// silencio: o aviso acima e a contrapartida de usa-lo.
		return func(string, net.Addr, ssh.PublicKey) error { return nil }, diags, nil
	}

	caminho := opts.KnownHostsPath
	if caminho == "" {
		padrao, err := CaminhoKnownHostsPadrao()
		if err != nil {
			return nil, diags, &output.Error{
				Code: output.ExitInternal,
				Diag: output.Diagnostic{
					Severity: output.SeverityError,
					Code:     CodigoKnownHostsAusente,
					Message: "nao foi possivel localizar o diretorio do usuario para achar o " +
						"known_hosts; passe --known-hosts com o caminho do arquivo",
				},
				Err: err,
			}
		}
		caminho = padrao
	}

	verificar, err := knownhosts.New(caminho)
	if err != nil {
		return nil, diags, erroAoAbrirKnownHosts(caminho, opts.Host, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := verificar(hostname, remote, key); err != nil {
			return classificarErroHostKey(caminho, enderecoLegivel(hostname, remote), key, err)
		}
		return nil
	}, diags, nil
}

// CaminhoKnownHostsPadrao devolve ~/.ssh/known_hosts. O filepath.Join usa o
// separador nativo, entao o mesmo codigo produz /home/x/.ssh/known_hosts e
// C:\Users\x\.ssh\known_hosts.
func CaminhoKnownHostsPadrao() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("nao foi possivel localizar o diretorio do usuario: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// classificarErroHostKey traduz o erro do knownhosts num dos desfechos da DR1.
//
// A distincao entre host desconhecido e chave alterada nao esta em dois tipos
// de erro: esta no campo Want de um unico *knownhosts.KeyError. Want vazio e
// "nunca vi este host"; Want preenchido e "ja vi, e a chave era outra". O
// segundo caso e o unico que descreve um ataque, e por isso nao pode sair com
// a mesma cara do primeiro.
func classificarErroHostKey(caminho, endereco string, key ssh.PublicKey, err error) error {
	var revogada *knownhosts.RevokedError
	if errors.As(err, &revogada) {
		return &output.Error{
			Code: output.ExitInternal,
			Diag: output.Diagnostic{
				Severity: output.SeverityError,
				Code:     CodigoHostKeyRevogada,
				Message: fmt.Sprintf(
					"a chave de host de %s esta REVOGADA em %s:%d — a marcacao @revoked diz "+
						"que esta chave e conhecidamente comprometida. O ngx recusa a conexao. "+
						"Nao remova a marcacao sem saber por que ela foi colocada ali; a chave "+
						"apresentada foi %s",
					endereco, revogada.Revoked.Filename, revogada.Revoked.Line, serializarChave(key)),
				File: revogada.Revoked.Filename,
				Line: revogada.Revoked.Line,
			},
			Err: err,
		}
	}

	var chave *knownhosts.KeyError
	if errors.As(err, &chave) {
		if len(chave.Want) > 0 {
			// Want preenchido nao significa, sozinho, que a chave mudou.
			//
			// Um servidor costuma oferecer varios tipos de chave de host
			// (ed25519, ecdsa, rsa) e o cliente negocia um deles. Se o
			// known_hosts registrou o host por OUTRO tipo, a biblioteca ve
			// uma chave que nao consta e devolve o mesmo KeyError de chave
			// alterada -- e o ngx acusaria ataque de interceptacao onde nao
			// houve nada. Medido contra um servidor real: known_hosts com
			// ssh-ed25519, servidor negociando ecdsa-sha2-nistp256.
			//
			// A distincao e o TIPO. Se nenhuma chave registrada tem o tipo
			// da apresentada, o que houve foi escolha de algoritmo, nao
			// troca de chave. Se ha registro do mesmo tipo e os bytes
			// diferem, ai sim mudou.
			if !registraTipo(chave.Want, key.Type()) {
				return erroAlgoritmoNaoRegistrado(caminho, endereco, key, chave, err)
			}
			return erroChaveAlterada(caminho, endereco, key, chave, err)
		}
		return erroHostDesconhecido(caminho, endereco, key, err)
	}

	// Qualquer outra falha do verificador — endereco malformado, por exemplo.
	// Nao vira nenhum dos quatro desfechos: inventar um deles seria afirmar
	// algo que nao se apurou.
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message: fmt.Sprintf(
				"nao foi possivel verificar a chave de host de %s contra %s: %v",
				endereco, caminho, err),
			File: caminho,
		},
		Err: err,
	}
}

// erroHostDesconhecido monta o desfecho de primeiro acesso. A mensagem entrega
// a linha pronta para o known_hosts porque essa e a acao que resolve, e diz de
// forma inequivoca que o host nunca foi visto — o oposto do caso de chave
// alterada, onde ele ja era conhecido.
func erroHostDesconhecido(caminho, endereco string, key ssh.PublicKey, causa error) error {
	linha := knownhosts.Line([]string{knownhosts.Normalize(endereco)}, key)
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoHostDesconhecido,
			Message: fmt.Sprintf(
				"host desconhecido: %s nao aparece em %s, entao o ngx nao tem com o que "+
					"comparar a chave apresentada e recusa a conexao. Este e o atrito normal "+
					"de primeiro acesso. Se voce confia nesta chave, acrescente a linha ao "+
					"arquivo: %s",
				endereco, caminho, linha),
			File: caminho,
		},
		Err: causa,
	}
}

// erroChaveAlterada monta o desfecho de possivel interceptacao. A mensagem diz
// "pode ser um ataque" com todas as letras, mostra a chave apresentada ao lado
// das registradas, e aponta o arquivo e a linha do registro que diverge.
func erroChaveAlterada(
	caminho, endereco string,
	key ssh.PublicKey,
	chave *knownhosts.KeyError,
	causa error,
) error {
	registradas := make([]string, 0, len(chave.Want))
	for i := range chave.Want {
		registradas = append(registradas, chave.Want[i].String())
	}

	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoHostKeyAlterada,
			Message: fmt.Sprintf(
				"ATENCAO: a chave de host de %s MUDOU. Isto pode ser um ataque de "+
					"interceptacao (man-in-the-middle): alguem no caminho pode estar se "+
					"passando pelo servidor. O host ja era conhecido e a chave apresentada "+
					"(%s) nao corresponde a nenhuma das registradas em %s: %s. O ngx recusa "+
					"a conexao. Se a troca for legitima (servidor reinstalado, por exemplo), "+
					"confirme a chave nova por um canal fora deste, remova a antiga com "+
					"`ssh-keygen -R %s` e registre a nova",
				endereco, serializarChave(key), caminho,
				strings.Join(registradas, "; "), endereco),
			File: chave.Want[0].Filename,
			Line: chave.Want[0].Line,
		},
		Err: causa,
	}
}

// erroAoAbrirKnownHosts separa "o arquivo nao existe" de "o arquivo existe mas
// nao pode ser lido". Sao problemas com solucoes diferentes, e o segundo nao
// pode se disfarcar de primeiro acesso.
func erroAoAbrirKnownHosts(caminho, host string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		alvo := host
		if alvo == "" {
			alvo = "o destino"
		}
		return &output.Error{
			Code: output.ExitInternal,
			Diag: output.Diagnostic{
				Severity: output.SeverityError,
				Code:     CodigoKnownHostsAusente,
				Message: fmt.Sprintf(
					"%s nao existe: o ngx nao tem nenhuma chave registrada para comparar com "+
						"a de %s. Rode `ssh %s` uma vez para registrar o host, aponte outro "+
						"arquivo com --known-hosts, ou aceite qualquer chave com "+
						"--insecure-host-key (inseguro)",
					caminho, alvo, alvo),
				File: caminho,
			},
			Err: err,
		}
	}

	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message: fmt.Sprintf(
				"%s existe mas nao pode ser usado (%v); o ngx nao verifica host key sem ele "+
					"e recusa a conexao",
				caminho, err),
			File: caminho,
		},
		Err: err,
	}
}

// avisoHostKeyInsegura e a contrapartida de --insecure-host-key. O texto diz o
// que se perdeu, nao apenas que uma flag foi usada: quem le a saida precisa
// saber que a conexao deixou de estar protegida.
func avisoHostKeyInsegura(host string) output.Diagnostic {
	alvo := host
	if alvo == "" {
		alvo = "o destino"
	}
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodigoAvisoHostKeyInsegura,
		Message: fmt.Sprintf(
			"--insecure-host-key: a chave de host de %s sera aceita sem nenhuma verificacao. "+
				"A conexao nao esta protegida contra interceptacao (man-in-the-middle) e "+
				"qualquer maquina na rota pode se passar pelo servidor",
			alvo),
	}
}

// enderecoLegivel escolhe como o host aparece nas mensagens. O hostname e o
// alvo que o usuario pediu e e o que ele reconhece; o endereco de rede so
// entra quando nao ha hostname.
func enderecoLegivel(hostname string, remote net.Addr) string {
	if hostname != "" {
		return hostname
	}
	if remote != nil {
		return remote.String()
	}
	return "o destino"
}

// serializarChave devolve a chave no formato de uma linha de known_hosts,
// "ssh-ed25519 AAAA...", sem a quebra de linha que o MarshalAuthorizedKey
// acrescenta.
func serializarChave(key ssh.PublicKey) string {
	if key == nil {
		return "(nenhuma)"
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// registraTipo diz se alguma das chaves conhecidas para o host usa o mesmo
// algoritmo da apresentada. E o que separa "a chave mudou" de "negociamos um
// algoritmo que voce nunca registrou".
func registraTipo(want []knownhosts.KnownKey, tipo string) bool {
	for i := range want {
		if want[i].Key.Type() == tipo {
			return true
		}
	}
	return false
}

// erroAlgoritmoNaoRegistrado cobre o host conhecido cuja chave apresentada e
// de um tipo que o known_hosts nao registra. Recusar continua certo -- nao ha
// como verificar o que nao se conhece --, mas dizer "pode ser ataque" seria
// mentira, e mentira num aviso de seguranca gasta a credibilidade do aviso
// que importa.
func erroAlgoritmoNaoRegistrado(
	caminho, endereco string,
	apresentada ssh.PublicKey,
	chave *knownhosts.KeyError,
	err error,
) error {
	tipos := make([]string, 0, len(chave.Want))
	vistos := map[string]bool{}
	for i := range chave.Want {
		t := chave.Want[i].Key.Type()
		if !vistos[t] {
			vistos[t] = true
			tipos = append(tipos, t)
		}
	}

	return &output.Error{
		Code: output.ExitInvalidConfig,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoAlgoritmoNaoRegistrado,
			Message: fmt.Sprintf(
				"o host %s e conhecido, mas so com chave do tipo %s, e ele apresentou "+
					"uma do tipo %s. Isto NAO indica ataque: o servidor oferece varios "+
					"tipos de chave e o tipo negociado nao esta no seu known_hosts. "+
					"Registre-o com: ssh-keyscan -t %s %s >> %s",
				endereco, strings.Join(tipos, ", "), apresentada.Type(),
				apresentada.Type(), hostDe(endereco), caminho),
			File: chave.Want[0].Filename,
			Line: chave.Want[0].Line,
		},
		Err: err,
	}
}

// hostDe devolve so o host de um "host:porta", para montar a linha de
// ssh-keyscan que a mensagem sugere.
func hostDe(endereco string) string {
	if h, _, err := net.SplitHostPort(endereco); err == nil {
		return h
	}
	return endereco
}
