package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRegistry = "asia-south1-docker.pkg.dev/test-project/test-repo"

// ── replaceImageRef unit tests ────────────────────────────────────────────────

// Case 1 — literal full path in repository field (cert-manager style)
func TestReplaceImageRef_Literal_CertManager(t *testing.T) {
	values := `
image:
  repository: quay.io/jetstack/cert-manager-controller
  tag: v1.19.2
webhook:
  image:
    repository: quay.io/jetstack/cert-manager-webhook
    tag: v1.19.2
cainjector:
  image:
    repository: quay.io/jetstack/cert-manager-cainjector
    tag: v1.19.2
`
	// Each image triggers an independent replaceImageRef call. The first call
	// replaces quay.io/jetstack/cert-manager-controller literally; it does NOT
	// touch the other two because they don't share the exact base path.
	result := replaceImageRef(values, "quay.io/jetstack/cert-manager-controller:v1.19.2", testRegistry)
	if !strings.Contains(result, testRegistry+"/cert-manager-controller") {
		t.Errorf("expected controller repository to be rewritten\ngot:\n%s", result)
	}
	if strings.Contains(result, "quay.io/jetstack/cert-manager-controller") {
		t.Errorf("original controller path should be gone\ngot:\n%s", result)
	}
	// Webhook and cainjector must remain untouched by this single call
	if !strings.Contains(result, "quay.io/jetstack/cert-manager-webhook") {
		t.Errorf("webhook should be unchanged after replacing only controller\ngot:\n%s", result)
	}
}

func TestReplaceImageRef_Literal_MetricsServer(t *testing.T) {
	values := `
image:
  repository: registry.k8s.io/metrics-server/metrics-server
  tag: v0.7.2
  pullPolicy: IfNotPresent
`
	result := replaceImageRef(values, "registry.k8s.io/metrics-server/metrics-server:v0.7.2", testRegistry)
	if !strings.Contains(result, testRegistry+"/metrics-server") {
		t.Errorf("expected repository to be rewritten\ngot:\n%s", result)
	}
	if strings.Contains(result, "registry.k8s.io/metrics-server/metrics-server") {
		t.Errorf("original path should be gone\ngot:\n%s", result)
	}
	// tag must be untouched
	if !strings.Contains(result, "tag: v0.7.2") {
		t.Errorf("tag should be preserved\ngot:\n%s", result)
	}
}

// Case 2 — org-prefix replacement (cilium style: suffix appended at runtime)
func TestReplaceImageRef_OrgPrefix_Cilium(t *testing.T) {
	values := `
image:
  repository: "quay.io/cilium/cilium"
  tag: "v1.19.1"
  digest: "sha256:abc123"

operator:
  image:
    repository: "quay.io/cilium/operator"
    tag: "v1.19.1"
    suffix: "-generic"
`
	// The literal base "quay.io/cilium/cilium" is in the text → Case 1 fires.
	// That replaces only the exact base. Let's first verify Case 1 here.
	result := replaceImageRef(values, "quay.io/cilium/cilium:v1.19.1", testRegistry)
	if !strings.Contains(result, testRegistry+"/cilium") {
		t.Errorf("expected cilium repository to be rewritten\ngot:\n%s", result)
	}
	if strings.Contains(result, "quay.io/cilium/cilium") {
		t.Errorf("original cilium path should be gone\ngot:\n%s", result)
	}
}

func TestReplaceImageRef_OrgPrefix_CiliumOperatorSuffix(t *testing.T) {
	// The rendered image for the operator is "operator-generic" (suffix appended).
	// The values.yaml only has "quay.io/cilium/operator" (no exact literal match),
	// so Case 2 (org-prefix) fires and replaces quay.io/cilium/ everywhere.
	values := `
operator:
  image:
    repository: "quay.io/cilium/operator"
    tag: "v1.19.1"
    suffix: "-generic"
`
	result := replaceImageRef(values, "quay.io/cilium/operator-generic:v1.19.1", testRegistry)
	// "quay.io/cilium/operator-generic" is NOT literally in text,
	// but "quay.io/cilium/" (orgPrefix+/) IS → Case 2 fires.
	if !strings.Contains(result, testRegistry+"/operator") {
		t.Errorf("expected operator repository to use new registry\ngot:\n%s", result)
	}
	if strings.Contains(result, "quay.io/cilium/") {
		t.Errorf("original org prefix should be gone\ngot:\n%s", result)
	}
	// The suffix "-generic" must be untouched
	if !strings.Contains(result, `suffix: "-generic"`) {
		t.Errorf("operator suffix must be preserved\ngot:\n%s", result)
	}
}

// Case 3 — split registry/repository fields (kube-state-metrics style)
func TestReplaceImageRef_SplitFields_KubeStateMetrics(t *testing.T) {
	values := `
image:
  registry: registry.k8s.io
  repository: kube-state-metrics/kube-state-metrics
  tag: v2.10.0
  pullPolicy: IfNotPresent
`
	result := replaceImageRef(values, "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.10.0", testRegistry)

	if !strings.Contains(result, "registry: "+testRegistry) {
		t.Errorf("expected registry field to be rewritten\ngot:\n%s", result)
	}
	if strings.Contains(result, "registry: registry.k8s.io") {
		t.Errorf("original registry field should be gone\ngot:\n%s", result)
	}
	// Repository should be simplified to just the image name
	if !strings.Contains(result, "repository: kube-state-metrics") {
		t.Errorf("expected repository simplified to image name\ngot:\n%s", result)
	}
	if strings.Contains(result, "kube-state-metrics/kube-state-metrics") {
		t.Errorf("original org/image path should be simplified\ngot:\n%s", result)
	}
	// Tag must be untouched
	if !strings.Contains(result, "tag: v2.10.0") {
		t.Errorf("tag should be preserved\ngot:\n%s", result)
	}
}

// No match — all three cases miss, text returned unchanged
func TestReplaceImageRef_NoMatch(t *testing.T) {
	values := `
image:
  repository: some-other-registry.io/other/image
  tag: v9.9.9
`
	result := replaceImageRef(values, "quay.io/jetstack/cert-manager-controller:v1.19.2", testRegistry)
	if result != values {
		t.Errorf("expected text unchanged when no match, got:\n%s", result)
	}
}

// Image without a colon (no tag) — returns text unchanged immediately
func TestReplaceImageRef_NoTag(t *testing.T) {
	values := `image: somerepo`
	result := replaceImageRef(values, "notag", testRegistry)
	if result != values {
		t.Errorf("expected unchanged text for tagless image, got:\n%s", result)
	}
}

// ── patchValues integration (in-memory via temp dir) ─────────────────────────

func writeValues(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write values.yaml: %v", err)
	}
	return dir
}

func readValues(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	return string(b)
}

// patchValues for a cert-manager-style chart (all literal paths, multiple images)
func TestPatchValues_CertManager(t *testing.T) {
	original := `
image:
  repository: quay.io/jetstack/cert-manager-controller
  tag: v1.19.2
webhook:
  image:
    repository: quay.io/jetstack/cert-manager-webhook
    tag: v1.19.2
cainjector:
  image:
    repository: quay.io/jetstack/cert-manager-cainjector
    tag: v1.19.2
startupAPICheck:
  image:
    repository: quay.io/jetstack/cert-manager-startupapicheck
    tag: v1.19.2
`
	images := []string{
		"quay.io/jetstack/cert-manager-controller:v1.19.2",
		"quay.io/jetstack/cert-manager-webhook:v1.19.2",
		"quay.io/jetstack/cert-manager-cainjector:v1.19.2",
		"quay.io/jetstack/cert-manager-startupapicheck:v1.19.2",
	}

	dir := writeValues(t, original)
	if err := patchValues(context.Background(), dir, testRegistry, images); err != nil {
		t.Fatalf("patchValues: %v", err)
	}

	result := readValues(t, dir)

	for _, img := range images {
		lastColon := strings.LastIndex(img, ":")
		base := img[:lastColon]
		name := base[strings.LastIndex(base, "/")+1:]

		if strings.Contains(result, base) {
			t.Errorf("original path %q should be gone after patch", base)
		}
		if !strings.Contains(result, testRegistry+"/"+name) {
			t.Errorf("expected %q in patched values", testRegistry+"/"+name)
		}
	}
}

// patchValues for a cilium-style chart (org-prefix, suffix mechanism preserved)
func TestPatchValues_Cilium(t *testing.T) {
	original := `
image:
  repository: "quay.io/cilium/cilium"
  tag: "v1.19.1"
  digest: "sha256:abc123"
operator:
  image:
    repository: "quay.io/cilium/operator"
    tag: "v1.19.1"
    suffix: "-generic"
`
	// Rendered images: cilium agent (case 1) + operator-generic (case 2)
	images := []string{
		"quay.io/cilium/cilium:v1.19.1",
		"quay.io/cilium/operator-generic:v1.19.1",
	}

	dir := writeValues(t, original)
	if err := patchValues(context.Background(), dir, testRegistry, images); err != nil {
		t.Fatalf("patchValues: %v", err)
	}

	result := readValues(t, dir)

	if strings.Contains(result, "quay.io/cilium/") {
		t.Errorf("all quay.io/cilium/ references should be rewritten\ngot:\n%s", result)
	}
	if !strings.Contains(result, testRegistry+"/cilium") {
		t.Errorf("expected rewritten cilium image\ngot:\n%s", result)
	}
	if !strings.Contains(result, testRegistry+"/operator") {
		t.Errorf("expected rewritten operator image\ngot:\n%s", result)
	}
	// The runtime suffix mechanism must survive
	if !strings.Contains(result, `suffix: "-generic"`) {
		t.Errorf("operator suffix must be preserved\ngot:\n%s", result)
	}
}

// patchValues for a kube-state-metrics-style chart (split registry/repository)
func TestPatchValues_KubeStateMetrics(t *testing.T) {
	original := `
image:
  registry: registry.k8s.io
  repository: kube-state-metrics/kube-state-metrics
  tag: v2.10.0
  pullPolicy: IfNotPresent
`
	images := []string{
		"registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.10.0",
	}

	dir := writeValues(t, original)
	if err := patchValues(context.Background(), dir, testRegistry, images); err != nil {
		t.Fatalf("patchValues: %v", err)
	}

	result := readValues(t, dir)

	if strings.Contains(result, "registry: registry.k8s.io") {
		t.Errorf("original registry field should be rewritten\ngot:\n%s", result)
	}
	if !strings.Contains(result, "registry: "+testRegistry) {
		t.Errorf("expected registry field to use testRegistry\ngot:\n%s", result)
	}
	if strings.Contains(result, "kube-state-metrics/kube-state-metrics") {
		t.Errorf("org/name path in repository should be simplified\ngot:\n%s", result)
	}
	if !strings.Contains(result, "repository: kube-state-metrics") {
		t.Errorf("expected simplified repository name\ngot:\n%s", result)
	}
}

// patchValues for ingress-nginx — multiple images sharing the same domain with
// split registry/repository fields. The second image's repository must still be
// patched even though the first image already replaced the registry: field.
func TestPatchValues_IngressNginx(t *testing.T) {
	original := `
controller:
  image:
    registry: registry.k8s.io
    repository: ingress-nginx/controller
    tag: v1.15.1
  admissionWebhooks:
    patch:
      image:
        registry: registry.k8s.io
        repository: ingress-nginx/kube-webhook-certgen
        tag: v1.6.9
`
	images := []string{
		"registry.k8s.io/ingress-nginx/controller:v1.15.1",
		"registry.k8s.io/ingress-nginx/kube-webhook-certgen:v1.6.9",
	}

	dir := writeValues(t, original)
	if err := patchValues(context.Background(), dir, testRegistry, images); err != nil {
		t.Fatalf("patchValues: %v", err)
	}

	result := readValues(t, dir)

	// Both registry: fields must be rewritten
	if strings.Contains(result, "registry: registry.k8s.io") {
		t.Errorf("original registry fields should be rewritten\ngot:\n%s", result)
	}
	if !strings.Contains(result, "registry: "+testRegistry) {
		t.Errorf("expected registry field to use testRegistry\ngot:\n%s", result)
	}
	// Both repository: fields must be simplified
	if strings.Contains(result, "ingress-nginx/controller") {
		t.Errorf("controller repository should be simplified\ngot:\n%s", result)
	}
	if strings.Contains(result, "ingress-nginx/kube-webhook-certgen") {
		t.Errorf("kube-webhook-certgen repository should be simplified\ngot:\n%s", result)
	}
	if !strings.Contains(result, "repository: controller") {
		t.Errorf("expected simplified controller repository\ngot:\n%s", result)
	}
	if !strings.Contains(result, "repository: kube-webhook-certgen") {
		t.Errorf("expected simplified kube-webhook-certgen repository\ngot:\n%s", result)
	}
}

// patchValues when there is no values.yaml — must not error
func TestPatchValues_MissingValuesYaml(t *testing.T) {
	dir := t.TempDir() // no values.yaml written
	if err := patchValues(context.Background(), dir, testRegistry, []string{"some.registry/img:v1"}); err != nil {
		t.Errorf("expected no error for missing values.yaml, got: %v", err)
	}
}
