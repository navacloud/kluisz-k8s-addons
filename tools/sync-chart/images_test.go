package main

import (
	"sort"
	"testing"
)

// assertImages is a t.Helper that checks got equals want (order-insensitive).
func assertImages(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Errorf("image count: got %d %v, want %d %v", len(got), got, len(want), want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("image[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// ── Deployment ────────────────────────────────────────────────────────────────

func TestParseImages_Deployment_ContainersAndInitContainers(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: quay.io/jetstack/cert-manager-controller:v1.19.2
      - image: quay.io/jetstack/cert-manager-webhook:v1.19.2
      initContainers:
      - image: quay.io/jetstack/cert-manager-ctl:v1.19.2
`)
	assertImages(t, got,
		"quay.io/jetstack/cert-manager-controller:v1.19.2",
		"quay.io/jetstack/cert-manager-webhook:v1.19.2",
		"quay.io/jetstack/cert-manager-ctl:v1.19.2",
	)
}

// ── DaemonSet — cilium style: quoted image with @sha256 digest ─────────────

func TestParseImages_DaemonSet_QuotedDigestImage(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: DaemonSet
spec:
  template:
    spec:
      containers:
      - image: "quay.io/cilium/cilium:v1.19.1@sha256:abcdef1234567890"
      initContainers:
      - image: "quay.io/cilium/cilium:v1.19.1@sha256:abcdef1234567890"
`)
	// Digest stripped; deduplication applied
	assertImages(t, got, "quay.io/cilium/cilium:v1.19.1")
}

func TestParseImages_DaemonSet_OperatorWithSuffix(t *testing.T) {
	// Cilium renders operator-generic as the actual image name (suffix appended by chart)
	got := parseImages(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: "quay.io/cilium/operator-generic:v1.19.1@sha256:aabbcc"
`)
	assertImages(t, got, "quay.io/cilium/operator-generic:v1.19.1")
}

// ── StatefulSet ───────────────────────────────────────────────────────────────

func TestParseImages_StatefulSet(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: StatefulSet
spec:
  template:
    spec:
      containers:
      - image: registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.10.0
`)
	assertImages(t, got, "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.10.0")
}

// ── ReplicaSet ────────────────────────────────────────────────────────────────

func TestParseImages_ReplicaSet(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: ReplicaSet
spec:
  template:
    spec:
      containers:
      - image: registry.k8s.io/metrics-server/metrics-server:v0.7.2
`)
	assertImages(t, got, "registry.k8s.io/metrics-server/metrics-server:v0.7.2")
}

// ── Pod ───────────────────────────────────────────────────────────────────────

func TestParseImages_Pod_FlatSpec(t *testing.T) {
	got := parseImages(`
apiVersion: v1
kind: Pod
spec:
  containers:
  - image: some.registry.io/myapp:v1.0.0
  initContainers:
  - image: some.registry.io/init:v1.0.0
`)
	assertImages(t, got,
		"some.registry.io/myapp:v1.0.0",
		"some.registry.io/init:v1.0.0",
	)
}

// ── Job ───────────────────────────────────────────────────────────────────────

func TestParseImages_Job(t *testing.T) {
	got := parseImages(`
apiVersion: batch/v1
kind: Job
spec:
  template:
    spec:
      containers:
      - image: some.registry.io/job-runner:v2.0.0
`)
	assertImages(t, got, "some.registry.io/job-runner:v2.0.0")
}

// ── CronJob ───────────────────────────────────────────────────────────────────

func TestParseImages_CronJob_NestedSpec(t *testing.T) {
	got := parseImages(`
apiVersion: batch/v1
kind: CronJob
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - image: some.registry.io/cron-task:v3.0.0
          initContainers:
          - image: some.registry.io/cron-init:v3.0.0
`)
	assertImages(t, got,
		"some.registry.io/cron-task:v3.0.0",
		"some.registry.io/cron-init:v3.0.0",
	)
}

// ── Filtering ─────────────────────────────────────────────────────────────────

func TestParseImages_SkipsGoTemplateExpressions(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
      - image: real.registry.io/app:v1.0.0
`)
	// Only the concrete image should appear
	assertImages(t, got, "real.registry.io/app:v1.0.0")
}

func TestParseImages_SkipsImageWithoutTag(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: notag-image
      - image: real.registry.io/app:v1.0.0
`)
	assertImages(t, got, "real.registry.io/app:v1.0.0")
}

func TestParseImages_SkipsNonWorkloadKinds(t *testing.T) {
	got := parseImages(`
apiVersion: v1
kind: Service
metadata:
  name: my-service
---
apiVersion: v1
kind: ConfigMap
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
`)
	if len(got) != 0 {
		t.Errorf("expected no images from non-workload kinds, got %v", got)
	}
}

func TestParseImages_DeduplicatesAcrossDocuments(t *testing.T) {
	got := parseImages(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: quay.io/jetstack/cert-manager-controller:v1.19.2
---
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: quay.io/jetstack/cert-manager-controller:v1.19.2
      - image: quay.io/jetstack/cert-manager-webhook:v1.19.2
`)
	assertImages(t, got,
		"quay.io/jetstack/cert-manager-controller:v1.19.2",
		"quay.io/jetstack/cert-manager-webhook:v1.19.2",
	)
}

func TestParseImages_EmptyManifest(t *testing.T) {
	got := parseImages("")
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestParseImages_MultipleWorkloadKinds(t *testing.T) {
	// One image per workload kind — all should be extracted
	got := parseImages(`
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - image: reg.io/deploy:v1
---
apiVersion: apps/v1
kind: DaemonSet
spec:
  template:
    spec:
      containers:
      - image: reg.io/ds:v1
---
apiVersion: apps/v1
kind: StatefulSet
spec:
  template:
    spec:
      containers:
      - image: reg.io/sts:v1
---
apiVersion: v1
kind: Pod
spec:
  containers:
  - image: reg.io/pod:v1
---
apiVersion: batch/v1
kind: Job
spec:
  template:
    spec:
      containers:
      - image: reg.io/job:v1
---
apiVersion: batch/v1
kind: CronJob
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - image: reg.io/cron:v1
`)
	assertImages(t, got,
		"reg.io/deploy:v1",
		"reg.io/ds:v1",
		"reg.io/sts:v1",
		"reg.io/pod:v1",
		"reg.io/job:v1",
		"reg.io/cron:v1",
	)
}
