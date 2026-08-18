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

// Diagnostic codes for the host key policy (DR1).
//
// The NGX-000N codes mirror exit codes and do not distinguish cases within the
// same exit code. A host key refusal needs a finer identity than that —
// whoever consumes the output has to separate "first access to this host" from
// "the server key changed" without interpreting text —, so the errors of this
// policy use the NGX-E### range, and the warning uses the NGX-W### range
// already adopted by the ~/.ssh/config warning.
//
// The four error codes are mutually exclusive and each one has its own
// message. Collapsing two of them erases exactly the information that
// justifies the policy existing.
const (
	// CodigoHostDesconhecido: the host is not in known_hosts. Normal
	// first-access friction.
	CodigoHostDesconhecido = "NGX-0201"

	// CodigoHostKeyAlterada: the host is in known_hosts with another key.
	// Possible interception.
	CodigoHostKeyAlterada = "NGX-0202"

	// CodigoHostKeyRevogada: the presented key is marked @revoked.
	CodigoHostKeyRevogada = "NGX-0203"

	// CodigoKnownHostsAusente: the known_hosts file does not exist.
	CodigoKnownHostsAusente = "NGX-0204"

	// CodigoAlgoritmoNaoRegistrado: the host is in known_hosts, but only
	// with keys of another type. This is neither an attack nor a first
	// access -- it is algorithm negotiation. It gets its own code precisely
	// so it is not confused with NGX-0202, whose message talks about
	// interception.
	CodigoAlgoritmoNaoRegistrado = "NGX-0207"

	// CodigoAvisoHostKeyInsegura: --insecure-host-key was used and the
	// verification was skipped.
	CodigoAvisoHostKeyInsegura = "NGX-0211"
)

// VerificadorHostKey builds the ngx ssh.HostKeyCallback according to DR1: the
// server key is checked against the user's known_hosts and any divergence
// refuses the connection.
//
// It returns three things because there are three distinct moments:
//   - the callback, which classifies what happens during the handshake;
//   - construction diagnostics — today only the --insecure-host-key warning;
//   - a construction error, when known_hosts cannot be read. knownhosts.New
//     opens the files at construction time, so "missing file" never reaches
//     the callback: it is an error here.
//
// The insecure-mode warning is emitted at construction, and not inside the
// callback, for two reasons: it does not depend on which key the server
// presents, and a callback writing into a shared list would be a data race
// with concurrent handshakes.
func VerificadorHostKey(opts SSHOptions) (ssh.HostKeyCallback, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	if opts.InsecureHostKey {
		diags = append(diags, avisoHostKeyInsegura(opts.Host))
		// Accepts any key. The escape hatch exists (DR1), but never in
		// silence: the warning above is the price of using it.
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
					Message: "could not locate the user's home directory to find " +
						"known_hosts; pass --known-hosts with the path to the file",
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

// CaminhoKnownHostsPadrao returns ~/.ssh/known_hosts. filepath.Join uses the
// native separator, so the same code produces /home/x/.ssh/known_hosts and
// C:\Users\x\.ssh\known_hosts.
func CaminhoKnownHostsPadrao() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not locate the user's home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// classificarErroHostKey translates the knownhosts error into one of the DR1
// outcomes.
//
// The distinction between unknown host and changed key is not in two error
// types: it is in the Want field of a single *knownhosts.KeyError. An empty
// Want means "I have never seen this host"; a filled Want means "I have seen
// it, and the key was another one". The second case is the only one that
// describes an attack, and that is why it cannot look like the first.
func classificarErroHostKey(caminho, endereco string, key ssh.PublicKey, err error) error {
	var revogada *knownhosts.RevokedError
	if errors.As(err, &revogada) {
		return &output.Error{
			Code: output.ExitInternal,
			Diag: output.Diagnostic{
				Severity: output.SeverityError,
				Code:     CodigoHostKeyRevogada,
				Message: fmt.Sprintf(
					"the host key of %s is REVOKED in %s:%d — the @revoked mark says "+
						"this key is known to be compromised. ngx refuses the connection. "+
						"Do not remove the mark without knowing why it was put there; the "+
						"key presented was %s",
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
			// A filled Want does not, on its own, mean the key changed.
			//
			// A server usually offers several host key types (ed25519,
			// ecdsa, rsa) and the client negotiates one of them. If
			// known_hosts recorded the host under ANOTHER type, the
			// library sees a key that is not on record and returns the
			// same KeyError as a changed key -- and ngx would accuse an
			// interception attack where nothing happened. Measured
			// against a real server: known_hosts with ssh-ed25519,
			// server negotiating ecdsa-sha2-nistp256.
			//
			// The distinction is the TYPE. If no recorded key has the
			// type of the presented one, what happened was algorithm
			// choice, not a key swap. If there is a record of the same
			// type and the bytes differ, then it really did change.
			if !registraTipo(chave.Want, key.Type()) {
				return erroAlgoritmoNaoRegistrado(caminho, endereco, key, chave, err)
			}
			return erroChaveAlterada(caminho, endereco, key, chave, err)
		}
		return erroHostDesconhecido(caminho, endereco, key, err)
	}

	// Any other failure of the verifier — a malformed address, for example.
	// It does not become any of the four outcomes: inventing one of them
	// would be asserting something that was not established.
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message: fmt.Sprintf(
				"could not verify the host key of %s against %s: %v",
				endereco, caminho, err),
			File: caminho,
		},
		Err: err,
	}
}

// erroHostDesconhecido builds the first-access outcome. The message hands over
// the ready-made known_hosts line because that is the action that resolves it,
// and says unambiguously that the host has never been seen — the opposite of
// the changed key case, where it was already known.
func erroHostDesconhecido(caminho, endereco string, key ssh.PublicKey, causa error) error {
	linha := knownhosts.Line([]string{knownhosts.Normalize(endereco)}, key)
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoHostDesconhecido,
			Message: fmt.Sprintf(
				"unknown host: %s does not appear in %s, so ngx has nothing to compare "+
					"the presented key against and refuses the connection. This is the "+
					"normal friction of a first access. If you trust this key, append the "+
					"line to the file: %s",
				endereco, caminho, linha),
			File: caminho,
		},
		Err: causa,
	}
}

// erroChaveAlterada builds the possible-interception outcome. The message says
// "this may be an attack" in so many words, shows the presented key next to
// the recorded ones, and points at the file and line of the record that
// diverges.
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
				"WARNING: the host key of %s has CHANGED. This may be an interception "+
					"attack (man-in-the-middle): someone on the path may be impersonating "+
					"the server. The host was already known and the presented key (%s) "+
					"does not match any of the ones recorded in %s: %s. ngx refuses the "+
					"connection. If the change is legitimate (a reinstalled server, for "+
					"example), confirm the new key through a channel other than this one, "+
					"remove the old one with `ssh-keygen -R %s` and record the new one",
				endereco, serializarChave(key), caminho,
				strings.Join(registradas, "; "), endereco),
			File: chave.Want[0].Filename,
			Line: chave.Want[0].Line,
		},
		Err: causa,
	}
}

// erroAoAbrirKnownHosts separates "the file does not exist" from "the file
// exists but cannot be read". They are different problems with different
// solutions, and the second one cannot disguise itself as a first access.
func erroAoAbrirKnownHosts(caminho, host string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		alvo := host
		if alvo == "" {
			alvo = "the target"
		}
		return &output.Error{
			Code: output.ExitInternal,
			Diag: output.Diagnostic{
				Severity: output.SeverityError,
				Code:     CodigoKnownHostsAusente,
				Message: fmt.Sprintf(
					"%s does not exist: ngx has no recorded key to compare with the one "+
						"from %s. Run `ssh %s` once to record the host, point at another "+
						"file with --known-hosts, or accept any key with "+
						"--insecure-host-key (insecure)",
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
				"%s exists but cannot be used (%v); ngx does not verify host keys without "+
					"it and refuses the connection",
				caminho, err),
			File: caminho,
		},
		Err: err,
	}
}

// avisoHostKeyInsegura is the counterpart of --insecure-host-key. The text
// says what was lost, not merely that a flag was used: whoever reads the
// output needs to know the connection stopped being protected.
func avisoHostKeyInsegura(host string) output.Diagnostic {
	alvo := host
	if alvo == "" {
		alvo = "the target"
	}
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodigoAvisoHostKeyInsegura,
		Message: fmt.Sprintf(
			"--insecure-host-key: the host key of %s will be accepted with no verification "+
				"at all. The connection is not protected against interception "+
				"(man-in-the-middle) and any machine on the route can impersonate the server",
			alvo),
	}
}

// enderecoLegivel chooses how the host appears in the messages. The hostname
// is the target the user asked for and is what they recognize; the network
// address only shows up when there is no hostname.
func enderecoLegivel(hostname string, remote net.Addr) string {
	if hostname != "" {
		return hostname
	}
	if remote != nil {
		return remote.String()
	}
	return "the target"
}

// serializarChave returns the key in the format of a known_hosts line,
// "ssh-ed25519 AAAA...", without the newline MarshalAuthorizedKey appends.
func serializarChave(key ssh.PublicKey) string {
	if key == nil {
		return "(none)"
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// registraTipo tells whether any of the known keys for the host uses the same
// algorithm as the presented one. It is what separates "the key changed" from
// "we negotiated an algorithm you never recorded".
func registraTipo(want []knownhosts.KnownKey, tipo string) bool {
	for i := range want {
		if want[i].Key.Type() == tipo {
			return true
		}
	}
	return false
}

// erroAlgoritmoNaoRegistrado covers the known host whose presented key is of a
// type known_hosts does not record. Refusing is still right -- there is no way
// to verify what is not known --, but saying "this may be an attack" would be
// a lie, and a lie in a security warning spends the credibility of the warning
// that matters.
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
				"the host %s is known, but only with a key of type %s, and it presented "+
					"one of type %s. This does NOT indicate an attack: the server offers "+
					"several key types and the negotiated type is not in your known_hosts. "+
					"Record it with: ssh-keyscan -t %s %s >> %s",
				endereco, strings.Join(tipos, ", "), apresentada.Type(),
				apresentada.Type(), hostDe(endereco), caminho),
			File: chave.Want[0].Filename,
			Line: chave.Want[0].Line,
		},
		Err: err,
	}
}

// hostDe returns only the host part of a "host:port", to build the ssh-keyscan
// line the message suggests.
func hostDe(endereco string) string {
	if h, _, err := net.SplitHostPort(endereco); err == nil {
		return h
	}
	return endereco
}
