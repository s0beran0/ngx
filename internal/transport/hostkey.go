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
	// CodeUnknownHost: the host is not in known_hosts. Normal
	// first-access friction.
	CodeUnknownHost = "NGX-0201"

	// CodeHostKeyChanged: the host is in known_hosts with another key.
	// Possible interception.
	CodeHostKeyChanged = "NGX-0202"

	// CodeHostKeyRevoked: the presented key is marked @revoked.
	CodeHostKeyRevoked = "NGX-0203"

	// CodeKnownHostsMissing: the known_hosts file does not exist.
	CodeKnownHostsMissing = "NGX-0204"

	// CodeAlgorithmNotRegistered: the host is in known_hosts, but only
	// with keys of another type. This is neither an attack nor a first
	// access -- it is algorithm negotiation. It gets its own code precisely
	// so it is not confused with NGX-0202, whose message talks about
	// interception.
	CodeAlgorithmNotRegistered = "NGX-0207"

	// CodeInsecureHostKeyWarning: --insecure-host-key was used and the
	// verification was skipped.
	CodeInsecureHostKeyWarning = "NGX-0211"
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
		diags = append(diags, warnInsecureHostKey(opts.Host))
		// Accepts any key. The escape hatch exists (DR1), but never in
		// silence: the warning above is the price of using it.
		return func(string, net.Addr, ssh.PublicKey) error { return nil }, diags, nil
	}

	path := opts.KnownHostsPath
	if path == "" {
		defaultPath, err := DefaultKnownHostsPath()
		if err != nil {
			return nil, diags, &output.Error{
				Code: output.ExitInternal,
				Diag: output.Diagnostic{
					Severity: output.SeverityError,
					Code:     CodeKnownHostsMissing,
					Message: "could not locate the user's home directory to find " +
						"known_hosts; pass --known-hosts with the path to the file",
				},
				Err: err,
			}
		}
		path = defaultPath
	}

	verify, err := knownhosts.New(path)
	if err != nil {
		return nil, diags, openKnownHostsError(path, opts.Host, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := verify(hostname, remote, key); err != nil {
			return classifyHostKeyError(path, readableAddress(hostname, remote), key, err)
		}
		return nil
	}, diags, nil
}

// DefaultKnownHostsPath returns ~/.ssh/known_hosts. filepath.Join uses the
// native separator, so the same code produces /home/x/.ssh/known_hosts and
// C:\Users\x\.ssh\known_hosts.
func DefaultKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not locate the user's home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// classifyHostKeyError translates the knownhosts error into one of the DR1
// outcomes.
//
// The distinction between unknown host and changed key is not in two error
// types: it is in the Want field of a single *knownhosts.KeyError. An empty
// Want means "I have never seen this host"; a filled Want means "I have seen
// it, and the key was another one". The second case is the only one that
// describes an attack, and that is why it cannot look like the first.
func classifyHostKeyError(path, address string, key ssh.PublicKey, err error) error {
	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return &output.Error{
			Code: output.ExitInternal,
			Diag: output.Diagnostic{
				Severity: output.SeverityError,
				Code:     CodeHostKeyRevoked,
				Message: fmt.Sprintf(
					"the host key of %s is REVOKED in %s:%d — the @revoked mark says "+
						"this key is known to be compromised. ngx refuses the connection. "+
						"Do not remove the mark without knowing why it was put there; the "+
						"key presented was %s",
					address, revoked.Revoked.Filename, revoked.Revoked.Line, serializeKey(key)),
				File: revoked.Revoked.Filename,
				Line: revoked.Revoked.Line,
			},
			Err: err,
		}
	}

	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) > 0 {
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
			if !recordsType(keyErr.Want, key.Type()) {
				return unregisteredAlgorithmError(path, address, key, keyErr, err)
			}
			return changedKeyError(path, address, key, keyErr, err)
		}
		return unknownHostError(path, address, key, err)
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
				address, path, err),
			File: path,
		},
		Err: err,
	}
}

// unknownHostError builds the first-access outcome. The message hands over
// the ready-made known_hosts line because that is the action that resolves it,
// and says unambiguously that the host has never been seen — the opposite of
// the changed key case, where it was already known.
func unknownHostError(path, address string, key ssh.PublicKey, cause error) error {
	line := knownhosts.Line([]string{knownhosts.Normalize(address)}, key)
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodeUnknownHost,
			Message: fmt.Sprintf(
				"unknown host: %s does not appear in %s, so ngx has nothing to compare "+
					"the presented key against and refuses the connection. This is the "+
					"normal friction of a first access. If you trust this key, append the "+
					"line to the file: %s",
				address, path, line),
			File: path,
		},
		Err: cause,
	}
}

// changedKeyError builds the possible-interception outcome. The message says
// "this may be an attack" in so many words, shows the presented key next to
// the recorded ones, and points at the file and line of the record that
// diverges.
func changedKeyError(
	path, address string,
	key ssh.PublicKey,
	keyErr *knownhosts.KeyError,
	cause error,
) error {
	recorded := make([]string, 0, len(keyErr.Want))
	for i := range keyErr.Want {
		recorded = append(recorded, keyErr.Want[i].String())
	}

	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodeHostKeyChanged,
			Message: fmt.Sprintf(
				"WARNING: the host key of %s has CHANGED. This may be an interception "+
					"attack (man-in-the-middle): someone on the path may be impersonating "+
					"the server. The host was already known and the presented key (%s) "+
					"does not match any of the ones recorded in %s: %s. ngx refuses the "+
					"connection. If the change is legitimate (a reinstalled server, for "+
					"example), confirm the new key through a channel other than this one, "+
					"remove the old one with `ssh-keygen -R %s` and record the new one",
				address, serializeKey(key), path,
				strings.Join(recorded, "; "), address),
			File: keyErr.Want[0].Filename,
			Line: keyErr.Want[0].Line,
		},
		Err: cause,
	}
}

// openKnownHostsError separates "the file does not exist" from "the file
// exists but cannot be read". They are different problems with different
// solutions, and the second one cannot disguise itself as a first access.
func openKnownHostsError(path, host string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		target := host
		if target == "" {
			target = "the target"
		}
		return &output.Error{
			Code: output.ExitInternal,
			Diag: output.Diagnostic{
				Severity: output.SeverityError,
				Code:     CodeKnownHostsMissing,
				Message: fmt.Sprintf(
					"%s does not exist: ngx has no recorded key to compare with the one "+
						"from %s. Run `ssh %s` once to record the host, point at another "+
						"file with --known-hosts, or accept any key with "+
						"--insecure-host-key (insecure)",
					path, target, target),
				File: path,
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
				path, err),
			File: path,
		},
		Err: err,
	}
}

// warnInsecureHostKey is the counterpart of --insecure-host-key. The text
// says what was lost, not merely that a flag was used: whoever reads the
// output needs to know the connection stopped being protected.
func warnInsecureHostKey(host string) output.Diagnostic {
	target := host
	if target == "" {
		target = "the target"
	}
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodeInsecureHostKeyWarning,
		Message: fmt.Sprintf(
			"--insecure-host-key: the host key of %s will be accepted with no verification "+
				"at all. The connection is not protected against interception "+
				"(man-in-the-middle) and any machine on the route can impersonate the server",
			target),
	}
}

// readableAddress chooses how the host appears in the messages. The hostname
// is the target the user asked for and is what they recognize; the network
// address only shows up when there is no hostname.
func readableAddress(hostname string, remote net.Addr) string {
	if hostname != "" {
		return hostname
	}
	if remote != nil {
		return remote.String()
	}
	return "the target"
}

// serializeKey returns the key in the format of a known_hosts line,
// "ssh-ed25519 AAAA...", without the newline MarshalAuthorizedKey appends.
func serializeKey(key ssh.PublicKey) string {
	if key == nil {
		return "(none)"
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// recordsType tells whether any of the known keys for the host uses the same
// algorithm as the presented one. It is what separates "the key changed" from
// "we negotiated an algorithm you never recorded".
func recordsType(want []knownhosts.KnownKey, keyType string) bool {
	for i := range want {
		if want[i].Key.Type() == keyType {
			return true
		}
	}
	return false
}

// unregisteredAlgorithmError covers the known host whose presented key is of a
// type known_hosts does not record. Refusing is still right -- there is no way
// to verify what is not known --, but saying "this may be an attack" would be
// a lie, and a lie in a security warning spends the credibility of the warning
// that matters.
func unregisteredAlgorithmError(
	path, address string,
	presented ssh.PublicKey,
	keyErr *knownhosts.KeyError,
	err error,
) error {
	types := make([]string, 0, len(keyErr.Want))
	seen := map[string]bool{}
	for i := range keyErr.Want {
		t := keyErr.Want[i].Key.Type()
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}

	return &output.Error{
		Code: output.ExitInvalidConfig,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodeAlgorithmNotRegistered,
			Message: fmt.Sprintf(
				"the host %s is known, but only with a key of type %s, and it presented "+
					"one of type %s. This does NOT indicate an attack: the server offers "+
					"several key types and the negotiated type is not in your known_hosts. "+
					"Record it with: ssh-keyscan -t %s %s >> %s",
				address, strings.Join(types, ", "), presented.Type(),
				presented.Type(), hostOf(address), path),
			File: keyErr.Want[0].Filename,
			Line: keyErr.Want[0].Line,
		},
		Err: err,
	}
}

// hostOf returns only the host part of a "host:port", to build the ssh-keyscan
// line the message suggests.
func hostOf(address string) string {
	if h, _, err := net.SplitHostPort(address); err == nil {
		return h
	}
	return address
}
