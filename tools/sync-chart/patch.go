package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// patchValues rewrites values.yaml so every mirrored image's upstream registry
// reference is replaced with the kluisz registry path.
//
// Three cases are handled in order:
//
//  1. Literal full path  — e.g. repository: quay.io/jetstack/cert-manager-controller
//     → direct string replace of the base path
//
//  2. Org-prefix match   — handles charts that append a suffix at runtime
//     e.g. cilium's operator: repository: "quay.io/cilium/operator" + suffix: "-generic"
//     → replace quay.io/cilium/ with $REGISTRY/ everywhere (suffix mechanism stays intact)
//
//  3. Split registry/repository fields — e.g. kube-state-metrics:
//     registry: registry.k8s.io
//     repository: kube-state-metrics/kube-state-metrics
//     → replace registry field with $REGISTRY, simplify repository to just the image name
func patchValues(ctx context.Context, chartDir, registry string, images []string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("patchValues: context already cancelled: %w", ctx.Err())
	default:
	}

	valuesPath := filepath.Join(chartDir, "values.yaml")
	fmt.Printf("  reading %s\n", valuesPath)
	data, err := os.ReadFile(valuesPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("  no values.yaml — skipping patch")
			return nil
		}
		return err
	}
	fmt.Printf("  values.yaml is %d bytes\n", len(data))

	text := string(data)
	fmt.Printf("  applying %d image replacement(s)\n", len(images))
	for _, img := range images {
		text = replaceImageRef(text, img, registry)
	}

	fmt.Printf("  writing patched values.yaml\n")
	return os.WriteFile(valuesPath, []byte(text), 0o644)
}

func replaceImageRef(text, src, registry string) string {
	lastColon := strings.LastIndex(src, ":")
	if lastColon < 0 {
		return text
	}

	base := src[:lastColon] // e.g. quay.io/cilium/cilium

	lastSlash := strings.LastIndex(base, "/")
	if lastSlash < 0 {
		return text
	}
	name := base[lastSlash+1:] // e.g. cilium

	domainEnd := strings.Index(base, "/")
	if domainEnd < 0 {
		return text
	}
	domain := base[:domainEnd]       // e.g. quay.io
	pathInRepo := base[domainEnd+1:] // e.g. cilium/cilium (after domain)
	orgPrefix := base[:lastSlash]    // e.g. quay.io/cilium
	dst := registry + "/" + name     // e.g. $REGISTRY/cilium

	switch {
	case strings.Contains(text, base):
		// Case 1: full path present literally
		fmt.Printf("    [literal] %s → %s\n", base, name)
		return strings.ReplaceAll(text, base, dst)

	case strings.Contains(text, orgPrefix+"/"):
		// Case 2: org prefix present — handles suffix mechanism
		fmt.Printf("    [org-pfx] %s/ → (registry)/\n", orgPrefix)
		return strings.ReplaceAll(text, orgPrefix+"/", registry+"/")

	default:
		// Case 3: split registry:/repository: fields
		registryRe := regexp.MustCompile(`(registry:\s*)` + regexp.QuoteMeta(domain))
		if !registryRe.MatchString(text) {
			fmt.Printf("    [no-match] %s — skipping\n", base)
			return text
		}
		text = registryRe.ReplaceAllStringFunc(text, func(m string) string {
			idx := strings.Index(m, domain)
			return m[:idx] + registry
		})
		repoRe := regexp.MustCompile(`(repository:\s*)` + regexp.QuoteMeta(pathInRepo) + `\b`)
		text = repoRe.ReplaceAllStringFunc(text, func(m string) string {
			idx := strings.Index(m, pathInRepo)
			return m[:idx] + name
		})
		fmt.Printf("    [split]   registry:%s + repo:%s → %s\n", domain, pathInRepo, dst)
		return text
	}
}
