package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInitTracer(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	// A conexão gRPC é preguiçosa: o coletor fora do ar não impede a inicialização.
	shutdown, err := InitTracer(context.Background(), "127.0.0.1:1", "app-catalogo-test", "v0.0.0-test")
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	if shutdown == nil {
		t.Fatal("InitTracer deve devolver a função de shutdown")
	}

	tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatalf("tracer provider global esperado *sdktrace.TracerProvider, obtido %T", otel.GetTracerProvider())
	}
	_, span := tp.Tracer("teste").Start(context.Background(), "span-teste")
	if !span.SpanContext().IsValid() {
		t.Fatal("span do provider configurado deve ter contexto válido")
	}
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// O export falha (coletor inalcançável) ou é interrompido pelo prazo; o que
	// importa é o shutdown terminar dentro do prazo, sem travar o processo.
	done := make(chan struct{})
	go func() {
		_ = shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown não respeitou o prazo do contexto")
	}
}

func TestInitTracer_InvalidEndpoint(t *testing.T) {
	previous := otel.GetTracerProvider()

	if _, err := InitTracer(context.Background(), "dns://%zz/coletor", "app-catalogo-test", "v0"); err == nil {
		t.Fatal("endpoint inválido deveria falhar ao criar a conexão gRPC")
	}
	if otel.GetTracerProvider() != previous {
		otel.SetTracerProvider(previous)
		t.Fatal("falha na inicialização não deve trocar o tracer provider global")
	}
}
