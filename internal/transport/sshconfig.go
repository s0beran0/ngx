package transport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"

	"github.com/s0beran0/ngx/internal/output"
)

// PortaSSHPadrao e a porta usada quando nem a flag nem o ~/.ssh/config dizem
// qual e. A biblioteca de parse nao aplica defaults — (*Config).Get devolve
// string vazia para uma chave ausente, nunca "22" —, entao o default e nosso.
const PortaSSHPadrao = 22

// CodigoAvisoSSHConfig identifica o diagnostico da DR7: o ~/.ssh/config
// existe mas nao pode ser lido inteiro.
//
// Os codigos NGX-000N espelham exit codes e sao sempre erros. Avisos, que por
// definicao nao derrubam o comando, usam a faixa NGX-W###.
const CodigoAvisoSSHConfig = "NGX-0210"

// SSHOptions descreve como o ngx alcanca um host remoto. Host e o alvo final
// da conexao — se o ~/.ssh/config traduzir o alias via HostName, e o HostName
// que fica aqui.
//
// Port zero, e as strings vazias, significam "nao informado": e assim que a
// resolucao distingue uma flag ausente de uma flag deliberadamente vazia, e e
// o que faz --user "" nao apagar o User que veio do arquivo.
type SSHOptions struct {
	Host            string
	Port            int
	User            string
	KeyPath         string
	Password        string
	KnownHostsPath  string
	InsecureHostKey bool
	Timeout         time.Duration
}

// posicaoDeErro casa o prefixo "(linha, coluna): " que a kevinburke/ssh_config
// coloca na mensagem. A biblioteca formata a posicao dentro da string do erro
// e nao expoe a Position em nenhum tipo publico: recuperar o lugar exato do
// problema so e possivel relendo a mensagem.
var posicaoDeErro = regexp.MustCompile(`^\((\d+), (\d+)\): (.*)$`)

// ResolverSSHConfig resolve as opcoes de conexao para o host pedido, aplicando
// a precedencia da DR2: flag explicita vence o arquivo, arquivo vence default.
//
// Uma flag vazia nao e uma flag: ela nao sobrescreve o valor do arquivo. Quem
// nao passou --port fica com o Port do ~/.ssh/config, e so na falta dele com a
// porta 22.
//
// O caminho do arquivo e parametro, e nao derivado de os.UserHomeDir() aqui
// dentro, para que a resolucao seja testavel sem depender do HOME de quem roda.
// Use CaminhoSSHConfigPadrao para o caminho de producao.
//
// Ausencia do arquivo nao e erro nem aviso: quem nao tem ~/.ssh/config
// simplesmente cai nos defaults. Ja um arquivo que existe e nao pode ser lido
// inteiro devolve um diagnostico de severidade warning (DR7) — a resolucao
// segue com flags e defaults, mas nunca em silencio.
//
// A lista de diagnosticos nunca e nil.
func ResolverSSHConfig(flags SSHOptions, caminhoConfig string) (SSHOptions, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	alias := strings.TrimSpace(flags.Host)
	if alias == "" {
		return SSHOptions{}, diags, output.Usage("host de destino nao informado")
	}

	resolvido := flags
	resolvido.Host = alias

	cfg, aviso := carregarSSHConfig(caminhoConfig)
	if aviso != nil {
		diags = append(diags, *aviso)
	}

	if cfg != nil {
		diags = aplicarArquivo(&resolvido, cfg, alias, caminhoConfig, diags)
	}

	// Defaults: o ultimo nivel da precedencia, aplicado sobre o que sobrou
	// vazio depois de flags e arquivo.
	if resolvido.Port == 0 {
		resolvido.Port = PortaSSHPadrao
	}
	if resolvido.User == "" {
		resolvido.User = usuarioCorrente()
	}

	return resolvido, diags, nil
}

// CaminhoSSHConfigPadrao devolve ~/.ssh/config. O filepath.Join usa o
// separador nativo, entao o mesmo codigo produz /home/x/.ssh/config e
// C:\Users\x\.ssh\config.
func CaminhoSSHConfigPadrao() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("nao foi possivel localizar o diretorio do usuario; "+
			"passe --host, --user, --port e --key explicitamente: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// carregarSSHConfig le e parseia o arquivo. Devolve (nil, nil) quando o
// arquivo nao existe — a ausencia e normal — e (nil, aviso) quando ele existe
// mas nao pode ser lido ou parseado.
func carregarSSHConfig(caminho string) (*ssh_config.Config, *output.Diagnostic) {
	if caminho == "" {
		return nil, nil
	}

	f, err := os.Open(caminho)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		d := avisoSSHConfig(caminho, 0, 0, err.Error())
		return nil, &d
	}
	defer f.Close()

	cfg, err := ssh_config.Decode(f)
	if err != nil {
		linha, coluna, motivo := posicaoDoErro(err)
		d := avisoSSHConfig(caminho, linha, coluna, motivo)
		return nil, &d
	}
	return cfg, nil
}

// aplicarArquivo preenche os campos que a flag deixou vazios com o que o
// arquivo diz para aquele alias.
func aplicarArquivo(
	opts *SSHOptions,
	cfg *ssh_config.Config,
	alias, caminho string,
	diags []output.Diagnostic,
) []output.Diagnostic {
	ler := func(chave string) string {
		v, err := cfg.Get(alias, chave)
		if err != nil {
			diags = append(diags, avisoSSHConfig(caminho, 0, 0,
				fmt.Sprintf("nao foi possivel ler %s: %v", chave, err)))
			return ""
		}
		return strings.TrimSpace(v)
	}

	// HostName nao concorre com --host: --host da o alias que se procura no
	// arquivo, e o HostName e a traducao desse alias para o alvo real. E o
	// mesmo que o ssh faz quando `ssh web1` conecta em 10.0.0.1.
	if hostName := ler("HostName"); hostName != "" {
		opts.Host = hostName
	}

	if opts.User == "" {
		opts.User = ler("User")
	}

	if opts.Port == 0 {
		if porta := ler("Port"); porta != "" {
			n, err := strconv.Atoi(porta)
			if err != nil || n <= 0 || n > 65535 {
				diags = append(diags, avisoSSHConfig(caminho, 0, 0,
					fmt.Sprintf("Port %q nao e um numero de porta valido; usando %d", porta, PortaSSHPadrao)))
			} else {
				opts.Port = n
			}
		}
	}

	if opts.KeyPath == "" {
		if key := ler("IdentityFile"); key != "" {
			opts.KeyPath = expandirTil(key)
		}
	}

	return diags
}

// avisoSSHConfig monta o diagnostico da DR7. A mensagem diz o que nao foi lido
// e o que continua valendo, porque um aviso que so diz "falhou" leva quem
// consome a saida a achar que o host nao estava no arquivo — e a causa e outra.
func avisoSSHConfig(caminho string, linha, coluna int, motivo string) output.Diagnostic {
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodigoAvisoSSHConfig,
		Message: fmt.Sprintf(
			"%s nao pode ser lido (%s); a resolucao por ssh_config foi pulada e valem "+
				"apenas as flags explicitas (--host, --user, --port, --key) e os defaults "+
				"(porta %d, usuario corrente)",
			caminho, motivo, PortaSSHPadrao),
		File:   caminho,
		Line:   linha,
		Column: coluna,
	}
}

// posicaoDoErro separa a posicao da mensagem. Erro sem posicao devolve zeros,
// e o Diagnostic omite os campos.
func posicaoDoErro(err error) (linha, coluna int, motivo string) {
	msg := err.Error()
	m := posicaoDeErro.FindStringSubmatch(msg)
	if m == nil {
		return 0, 0, msg
	}
	linha, _ = strconv.Atoi(m[1])
	coluna, _ = strconv.Atoi(m[2])
	return linha, coluna, m[3]
}

// expandirTil resolve "~/" contra o diretorio do usuario. Um til que nao pode
// ser resolvido fica como esta: e melhor devolver o caminho literal, que falha
// visivelmente ao abrir, do que inventar um diretorio.
func expandirTil(caminho string) string {
	if caminho != "~" && !strings.HasPrefix(caminho, "~/") && !strings.HasPrefix(caminho, `~\`) {
		return caminho
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return caminho
	}
	if caminho == "~" {
		return home
	}
	return filepath.Join(home, filepath.FromSlash(caminho[2:]))
}

// usuarioCorrente devolve o usuario do SO. os/user.Current funciona sem cgo
// (o Go cai num leitor puro de /etc/passwd), mas pode falhar em container sem
// entrada de usuario; ai as variaveis de ambiente sao a ultima tentativa.
func usuarioCorrente() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return nomeSemDominio(u.Username)
	}
	for _, env := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(env); v != "" {
			return nomeSemDominio(v)
		}
	}
	return ""
}

// nomeSemDominio remove o prefixo de dominio que o Windows poe em
// Username ("DOMINIO\usuario"): o SSH quer so o nome.
func nomeSemDominio(nome string) string {
	if i := strings.LastIndex(nome, `\`); i >= 0 {
		return nome[i+1:]
	}
	return nome
}
