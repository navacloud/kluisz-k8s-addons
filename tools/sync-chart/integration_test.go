//go:build integration

package main

// Integration tests require internet access to pull real Helm charts.
// Run with: go test -tags integration -v -timeout 10m ./...
//
// These tests verify the full pipeline short of the actual registry push:
//   pullChart → renderChart/extractImages → patchValues
//
// They confirm that our image extraction and values-patching logic works
// correctly against the real chart versions declared in the addon YAML files.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const integTestRegistry = "test.registry.io/kluisz/test"

type chartCase struct {
	// Addon metadata from the addon YAML files
	name    string
	repo    string
	chart   string
	version string

	// Assertions on extracted images
	wantImgPrefixes []string // at least one image per prefix must be extracted
	wantImgCount    int      // minimum number of images expected

	// Assertions on the patched values.yaml
	upstreamRegistries []string // these strings must NOT appear after patching
}

var chartCases = []chartCase{
	{
		name:    "cert-manager v1.19.2",
		repo:    "https://charts.jetstack.io",
		chart:   "cert-manager",
		version: "v1.19.2",
		// Deploys controller, webhook, cainjector, startupapicheck
		wantImgPrefixes:    []string{"quay.io/jetstack/cert-manager-"},
		wantImgCount:       3,
		upstreamRegistries: []string{"quay.io/jetstack/cert-manager-"},
	},
	{
		name:    "cert-manager v1.16.2",
		repo:    "https://charts.jetstack.io",
		chart:   "cert-manager",
		version: "v1.16.2",
		wantImgPrefixes:    []string{"quay.io/jetstack/cert-manager-"},
		wantImgCount:       3,
		upstreamRegistries: []string{"quay.io/jetstack/cert-manager-"},
	},
	{
		name:    "cilium 1.19.1",
		repo:    "https://helm.cilium.io",
		chart:   "cilium",
		version: "1.19.1",
		// Renders at minimum: cilium agent DaemonSet + operator Deployment
		wantImgPrefixes:    []string{"quay.io/cilium/"},
		wantImgCount:       2,
		upstreamRegistries: []string{"quay.io/cilium/"},
	},
	{
		name:    "kube-state-metrics 6.4.1",
		repo:    "https://prometheus-community.github.io/helm-charts",
		chart:   "kube-state-metrics",
		version: "6.4.1",
		wantImgPrefixes:    []string{"registry.k8s.io/kube-state-metrics/"},
		wantImgCount:       1,
		upstreamRegistries: []string{"registry.k8s.io"},
	},
	{
		name:    "metrics-server 3.13.0",
		repo:    "https://kubernetes-sigs.github.io/metrics-server/",
		chart:   "metrics-server",
		version: "3.13.0",
		wantImgPrefixes:    []string{"registry.k8s.io/metrics-server/"},
		wantImgCount:       1,
		upstreamRegistries: []string{"registry.k8s.io/metrics-server/"},
	},
}

func TestIntegration_ExtractImages(t *testing.T) {
	for _, tc := range chartCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			chartDir, err := pullChart(tc.repo, tc.chart, tc.version, dir)
			if err != nil {
				t.Fatalf("pullChart: %v", err)
			}

			images, err := extractImages(chartDir)
			if err != nil {
				t.Fatalf("extractImages: %v", err)
			}

			if len(images) < tc.wantImgCount {
				t.Errorf("want >= %d images, got %d: %v", tc.wantImgCount, len(images), images)
			}

			for _, prefix := range tc.wantImgPrefixes {
				found := false
				for _, img := range images {
					if strings.HasPrefix(img, prefix) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no image with prefix %q found in %v", prefix, images)
				}
			}

			t.Logf("extracted %d images: %v", len(images), images)
		})
	}
}

func TestIntegration_PatchValues(t *testing.T) {
	for _, tc := range chartCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			chartDir, err := pullChart(tc.repo, tc.chart, tc.version, dir)
			if err != nil {
				t.Fatalf("pullChart: %v", err)
			}

			images, err := extractImages(chartDir)
			if err != nil {
				t.Fatalf("extractImages: %v", err)
			}
			if len(images) == 0 {
				t.Skip("no images extracted — skipping patch test")
			}

			if err := patchValues(chartDir, integTestRegistry, images); err != nil {
				t.Fatalf("patchValues: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
			if err != nil {
				t.Fatalf("read patched values.yaml: %v", err)
			}
			result := string(data)

			// The test registry must now appear somewhere in the patched values
			if !strings.Contains(result, integTestRegistry) {
				t.Errorf("expected %q in patched values.yaml", integTestRegistry)
			}

			// Each extracted image's base path must no longer reference the upstream registry
			for _, img := range images {
				lastColon := strings.LastIndex(img, ":")
				if lastColon < 0 {
					continue
				}
				base := img[:lastColon] // e.g. quay.io/jetstack/cert-manager-controller

				// Check whether the full base path or any upstream registry prefix from
				// tc.upstreamRegistries is still present for this specific image.
				for _, upstreamReg := range tc.upstreamRegistries {
					if strings.HasPrefix(base, strings.TrimSuffix(upstreamReg, "/")) {
						if strings.Contains(result, base) {
							t.Errorf("upstream path %q still present after patch", base)
						}
					}
				}
			}

			t.Logf("patched values.yaml for %s (%d images replaced)", tc.name, len(images))
		})
	}
}

// TestIntegration_RenderProducesValidYAML checks that the rendered manifest
// from each chart is parseable YAML that contains at least one workload resource.
func TestIntegration_RenderProducesValidYAML(t *testing.T) {
	for _, tc := range chartCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			chartDir, err := pullChart(tc.repo, tc.chart, tc.version, dir)
			if err != nil {
				t.Fatalf("pullChart: %v", err)
			}

			manifest, err := renderChart(chartDir)
			if err != nil {
				t.Fatalf("renderChart: %v", err)
			}
			if manifest == "" {
				t.Fatal("renderChart returned empty manifest")
			}

			// Confirm we got at least one workload kind
			foundWorkload := false
			for kind := range workloadKinds {
				if strings.Contains(manifest, "kind: "+kind) {
					foundWorkload = true
					break
				}
			}
			if !foundWorkload {
				t.Errorf("rendered manifest contains no workload resources\nmanifest prefix:\n%.500s", manifest)
			}
		})
	}
}
