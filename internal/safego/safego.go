// Package safego protege as goroutines soltas do serviço contra panic.
//
// O gin.Recovery() só cobre a goroutine da requisição HTTP: um panic numa
// goroutine disparada com `go` derruba o processo inteiro, com todas as
// requisições em andamento. Toda goroutine solta que processa dado externo
// (SDK de terceiros, resposta de outro serviço, evento de webhook) deve rodar
// com Go ou começar com `defer safego.Recover(...)`.
//
// Goroutines de ciclo de vida (ListenAndServe, espera de sinal, laço de
// reprocessamento) ficam de fora de propósito: nelas, reiniciar o processo é o
// comportamento certo.
package safego

import (
	"fmt"
	"runtime/debug"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

// PanicError é o panic recuperado de uma goroutine, entregue como erro.
type PanicError struct {
	Goroutine string
	Value     any
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("panic na goroutine %s: %v", e.Goroutine, e.Value)
}

// Go roda fn numa goroutine nova; um panic em fn é recuperado e logado em vez
// de derrubar o processo.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name, nil)
		fn()
	}()
}

// Recover precisa ser chamado direto num defer (`defer safego.Recover(...)`).
// Recupera o panic da goroutine, loga com a pilha, conta em
// catalogo_goroutine_panics_total e, se onPanic não é nil, entrega o panic a
// ele como *PanicError (para quem espera um resultado da goroutine não ficar
// bloqueado).
func Recover(name string, onPanic func(error)) {
	r := recover()
	if r == nil {
		return
	}
	err := &PanicError{Goroutine: name, Value: r}
	observability.GoroutinePanicsTotal.WithLabelValues(name).Inc()
	log.Error().
		Str("goroutine", name).
		Str("panic", fmt.Sprint(r)).
		Bytes("stack", debug.Stack()).
		Msg("goroutine: panic recuperado")
	if onPanic != nil {
		onPanic(err)
	}
}
