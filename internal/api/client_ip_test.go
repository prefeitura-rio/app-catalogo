package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Cadeia de staging/produção: load balancer HTTP(S) do Google →
// istio-ingressgateway → sidecar do Istio → app. O LB acrescenta
// "<cliente>, <IP do forwarding rule>" ao X-Forwarded-For que o cliente mandou,
// o gateway acrescenta o proxy do Google de onde a conexão veio e o sidecar
// entrega a requisição a partir de 127.0.0.6.
const (
	lbIP       = "8.233.204.175"
	gfeIP      = "35.191.10.20"
	sidecarIP  = "127.0.0.6:40000"
	realClient = "198.51.100.20"
)

var meshProxies = []string{
	"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	"35.191.0.0/16", "130.211.0.0/22", lbIP + "/32",
}

func viaMesh(client, forged string) string {
	chain := client + ", " + lbIP + ", " + gfeIP
	if forged != "" {
		chain = forged + ", " + chain
	}
	return chain
}

func (f routerFixture) from(remoteAddr string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = remoteAddr
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func forgedIP(i int) string {
	return fmt.Sprintf("203.0.%d.%d", (i/250)%250, i%250+1)
}

// loggedIP devolve o campo ip do log de acesso da requisição.
func loggedIP(t *testing.T, f routerFixture, remoteAddr string, header http.Header) string {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	defer func() { log.Logger = previous }()

	f.from(remoteAddr, header)

	var line struct {
		IP string `json:"ip"`
	}
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if err := json.Unmarshal(raw, &line); err == nil && line.IP != "" {
			return line.IP
		}
	}
	t.Fatalf("log de acesso sem campo ip: %q", buf.String())
	return ""
}

// Um X-Forwarded-For diferente a cada requisição não pode criar um contador novo.
func TestSetupRouter_RateLimitIgnoresForgedForwardedFor(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		header     func(i int) http.Header
	}{
		{
			name:       "conexão direta da internet",
			remoteAddr: "192.0.2.7:5000",
			header: func(i int) http.Header {
				return http.Header{"X-Forwarded-For": {forgedIP(i)}}
			},
		},
		{
			name:       "pelo load balancer e pelo Istio",
			remoteAddr: sidecarIP,
			header: func(i int) http.Header {
				return http.Header{"X-Forwarded-For": {viaMesh(realClient, forgedIP(i))}}
			},
		},
		{
			name:       "X-Forwarded-For forjado em linha separada",
			remoteAddr: sidecarIP,
			header: func(i int) http.Header {
				return http.Header{"X-Forwarded-For": {forgedIP(i), viaMesh(realClient, "")}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouterFixtureWithProxies(t, meshProxies)
			for i := 0; i < 300; i++ {
				if w := f.from(tc.remoteAddr, tc.header(i)); w.Code != http.StatusOK {
					t.Fatalf("requisição %d dentro do limite: esperado 200, obtido %d", i+1, w.Code)
				}
			}
			if w := f.from(tc.remoteAddr, tc.header(300)); w.Code != http.StatusTooManyRequests {
				t.Fatalf("301ª requisição com X-Forwarded-For novo: esperado 429, obtido %d", w.Code)
			}
		})
	}

	// Pela cadeia real, cada cliente continua com o próprio contador.
	f := newRouterFixtureWithProxies(t, meshProxies)
	for i := 0; i < 301; i++ {
		f.from(sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "")}})
	}
	if w := f.from(sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "")}}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("cliente acima do limite: esperado 429, obtido %d", w.Code)
	}
	if w := f.from(sidecarIP, http.Header{"X-Forwarded-For": {viaMesh("198.51.100.21", "")}}); w.Code != http.StatusOK {
		t.Fatalf("outro cliente atrás do mesmo LB: esperado 200, obtido %d", w.Code)
	}
}

// O IP do log de acesso é o do cliente real, não o que ele escreveu nos cabeçalhos.
func TestSetupRouter_ClientIP(t *testing.T) {
	mesh := newRouterFixtureWithProxies(t, meshProxies)
	none := newRouterFixtureWithProxies(t, nil)

	cases := []struct {
		name       string
		f          routerFixture
		remoteAddr string
		header     http.Header
		want       string
	}{
		{"cadeia real sem cabeçalho do cliente", mesh, sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "")}}, realClient},
		{"cadeia real com IP forjado", mesh, sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "192.0.2.99")}}, realClient},
		{"cadeia real com forjado privado", mesh, sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "10.1.2.3")}}, realClient},
		{"cadeia real com lixo à esquerda", mesh, sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "nao-e-ip")}}, realClient},
		{"forjado em linha separada", mesh, sidecarIP, http.Header{"X-Forwarded-For": {"192.0.2.99", viaMesh(realClient, "")}}, realClient},
		{"conexão direta da internet", mesh, "192.0.2.7:5000", http.Header{"X-Forwarded-For": {"192.0.2.99"}}, "192.0.2.7"},
		{"X-Real-IP não é lido", mesh, sidecarIP, http.Header{"X-Real-Ip": {"192.0.2.99"}}, "127.0.0.6"},
		{"chamada interna sem cabeçalho", mesh, sidecarIP, nil, "127.0.0.6"},
		{"sem proxies confiáveis", none, sidecarIP, http.Header{"X-Forwarded-For": {viaMesh(realClient, "192.0.2.99")}}, "127.0.0.6"},
	}
	for _, tc := range cases {
		if got := loggedIP(t, tc.f, tc.remoteAddr, tc.header); got != tc.want {
			t.Errorf("%s: ip esperado %s, obtido %s", tc.name, tc.want, got)
		}
	}
}
