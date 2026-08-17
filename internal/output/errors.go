package output

import (
	"errors"
	"fmt"
)

// ExitCode e o codigo de saida do processo. A v0.1 emite apenas os codigos
// abaixo; 4 (lint), 5 e 6 (apply) e 8 (mutacao ambigua) pertencem a comandos
// que ainda nao existem e nao sao documentados como suportados ate que sejam
// emitiveis.
type ExitCode int

const (
	ExitOK            ExitCode = 0
	ExitInternal      ExitCode = 1
	ExitUsage         ExitCode = 2
	ExitInvalidConfig ExitCode = 3
	ExitDrift         ExitCode = 7
	ExitHashMismatch  ExitCode = 9
)

// Error e um erro que carrega seu proprio exit code e o diagnostico
// correspondente. Comandos nunca escolhem exit code diretamente: eles
// devolvem um destes, e main.go traduz num unico ponto.
type Error struct {
	Code ExitCode
	Diag Diagnostic
	Err  error
}

func (e *Error) Error() string { return e.Diag.Message }

func (e *Error) Unwrap() error { return e.Err }

func newError(code ExitCode, diagCode, format string, args ...any) *Error {
	return &Error{
		Code: code,
		Diag: Diagnostic{
			Severity: SeverityError,
			Code:     diagCode,
			Message:  fmt.Sprintf(format, args...),
		},
	}
}

// Usage sinaliza erro de uso: flag invalida, seletor malformado, argumento
// obrigatorio ausente.
func Usage(format string, args ...any) *Error {
	return newError(ExitUsage, "NGX-0002", format, args...)
}

// InvalidConfig sinaliza que a configuracao do nginx nao e valida.
func InvalidConfig(format string, args ...any) *Error {
	return newError(ExitInvalidConfig, "NGX-0003", format, args...)
}

// Drift sinaliza que a configuracao em disco difere da que esta carregada.
func Drift(format string, args ...any) *Error {
	return newError(ExitDrift, "NGX-0007", format, args...)
}

// HashMismatch sinaliza que um ID foi apresentado contra uma versao da
// configuracao diferente daquela em que foi gerado. Os IDs anteriores sao
// invalidos e o agente precisa reler antes de agir.
func HashMismatch(esperado, atual string) *Error {
	return newError(ExitHashMismatch, "NGX-0009",
		"a configuracao mudou desde a leitura: esperado %s, atual %s", esperado, atual)
}

// Internal envolve uma falha de IO ou um defeito do proprio ngx. A causa
// original (err) e guardada no campo Err e so fica acessivel via
// errors.Unwrap/errors.Is/errors.As: Error() e Diag.Message devolvem apenas
// o format, nunca a causa. Isso e deliberado — quem renderizar o diagnostico
// no envelope JSON nao deve vazar detalhes internos ao agente.
func Internal(err error, format string, args ...any) *Error {
	e := newError(ExitInternal, "NGX-0001", format, args...)
	e.Err = err
	return e
}

// CodeOf extrai o exit code de um erro, atravessando wrapping. Um erro sem
// codigo e tratado como falha interna, nunca como sucesso.
func CodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitInternal
}
