package observability

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"
)

type alertRule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

type ruleFile struct {
	Groups []struct {
		Name  string      `yaml:"name"`
		Rules []alertRule `yaml:"rules"`
	} `yaml:"groups"`
}

func loadAlertRules(t *testing.T) map[string]alertRule {
	t.Helper()
	raw, err := os.ReadFile("alerts.yml")
	if err != nil {
		t.Fatalf("ler alerts.yml: %v", err)
	}
	var file ruleFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("alerts.yml não é YAML válido: %v", err)
	}
	rules := map[string]alertRule{}
	for _, g := range file.Groups {
		for _, r := range g.Rules {
			if r.Alert == "" {
				t.Fatalf("grupo %s: regra sem nome de alerta", g.Name)
			}
			if _, dup := rules[r.Alert]; dup {
				t.Fatalf("alerta repetido: %s", r.Alert)
			}
			rules[r.Alert] = r
		}
	}
	return rules
}

// TestAlertRules_CoverAcceptanceCriteria confere que cada alerta pedido existe,
// com severidade e resumo, e com o limiar e a janela do critério.
func TestAlertRules_CoverAcceptanceCriteria(t *testing.T) {
	rules := loadAlertRules(t)
	want := map[string][]string{
		"CatalogoHTTP5xxAlto":             {`status=~"5.."`, "[5m]", "> 0.02"},
		"CatalogoLatenciaPublicaP95Alta":  {"histogram_quantile(0.95", `route=~"/api/public/.*"`, "> 1"},
		"CatalogoPodReiniciando":          {"kube_pod_container_status_restarts_total", "[15m]", "> 3"},
		"CatalogoPodCrashLoop":            {`reason="CrashLoopBackOff"`},
		"CatalogoSyncAtrasado":            {"catalogo_sync_last_success_timestamp_seconds", "> 2 * max by (namespace, source) (catalogo_sync_interval_seconds)"},
		"CatalogoWebhookSalesForceFalhou": {`result="failed"`, "> 0"},
	}
	for name, fragments := range want {
		r, ok := rules[name]
		if !ok {
			t.Errorf("alerts.yml sem o alerta %s", name)
			continue
		}
		expr := strings.Join(strings.Fields(r.Expr), " ")
		for _, f := range fragments {
			if !strings.Contains(expr, f) {
				t.Errorf("%s: expr sem %q: %s", name, f, expr)
			}
		}
		if s := r.Labels["severity"]; s != "critical" && s != "warning" {
			t.Errorf("%s: severity %q, esperado critical ou warning", name, s)
		}
		if r.Labels["service"] != "app-catalogo" {
			t.Errorf("%s: label service %q, esperado app-catalogo", name, r.Labels["service"])
		}
		if r.Annotations["summary"] == "" || r.Annotations["description"] == "" {
			t.Errorf("%s: sem summary ou description", name)
		}
	}
	if got := rules["CatalogoHTTP5xxAlto"].For; got != "5m" {
		t.Errorf("CatalogoHTTP5xxAlto: for %q, esperado 5m", got)
	}
	if got := rules["CatalogoLatenciaPublicaP95Alta"].For; got != "10m" {
		t.Errorf("CatalogoLatenciaPublicaP95Alta: for %q, esperado 10m", got)
	}
}

var (
	serviceMetricRe = regexp.MustCompile(`\bcatalogo_[a-z0-9_]+`)
	descFQNameRe    = regexp.MustCompile(`fqName: "([^"]+)"`)
)

// registeredMetricNames devolve o nome de cada métrica do serviço e, nos
// histogramas, os das séries _bucket, _sum e _count.
func registeredMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, c := range collectors {
		ch := make(chan *prometheus.Desc, 1)
		go func() { c.Describe(ch); close(ch) }()
		for d := range ch {
			m := descFQNameRe.FindStringSubmatch(d.String())
			if m == nil {
				t.Fatalf("desc sem fqName: %s", d)
			}
			names[m[1]] = true
			if _, isHistogram := c.(*prometheus.HistogramVec); isHistogram {
				for _, suffix := range []string{"_bucket", "_sum", "_count"} {
					names[m[1]+suffix] = true
				}
			}
		}
	}
	return names
}

// TestAlertRules_ReferenceRegisteredMetrics impede que um alerta fique mudo por
// citar uma métrica catalogo_* que o serviço não expõe (renomeada ou removida).
func TestAlertRules_ReferenceRegisteredMetrics(t *testing.T) {
	registered := registeredMetricNames(t)
	for name, r := range loadAlertRules(t) {
		for _, metric := range serviceMetricRe.FindAllString(r.Expr, -1) {
			if !registered[metric] {
				t.Errorf("%s cita %s, que internal/observability não registra", name, metric)
			}
		}
	}
}
