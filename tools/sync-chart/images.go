package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"gopkg.in/yaml.v3"
)

// Minimal Kubernetes types needed to navigate to container specs.
type k8sResource struct {
	Kind string  `yaml:"kind"`
	Spec k8sSpec `yaml:"spec"`
}

type k8sSpec struct {
	// Pod: containers live directly under spec
	Containers     []k8sContainer `yaml:"containers"`
	InitContainers []k8sContainer `yaml:"initContainers"`
	// Deployment / DaemonSet / StatefulSet / ReplicaSet / Job
	Template k8sPodTemplate `yaml:"template"`
	// CronJob
	JobTemplate struct {
		Spec struct {
			Template k8sPodTemplate `yaml:"template"`
		} `yaml:"spec"`
	} `yaml:"jobTemplate"`
}

type k8sPodTemplate struct {
	Spec struct {
		Containers     []k8sContainer `yaml:"containers"`
		InitContainers []k8sContainer `yaml:"initContainers"`
	} `yaml:"spec"`
}

type k8sContainer struct {
	Image string `yaml:"image"`
}

var workloadKinds = map[string]bool{
	"Pod": true, "Deployment": true, "DaemonSet": true,
	"StatefulSet": true, "ReplicaSet": true, "Job": true, "CronJob": true,
}

// extractImages renders the chart via the Helm SDK and parses the manifest,
// collecting unique container images from all workload specs.
func extractImages(ctx context.Context, chartDir string) ([]string, error) {
	manifest, err := renderChart(ctx, chartDir)
	if err != nil {
		return nil, err
	}
	images := parseImages(manifest)
	return images, nil
}

// parseImages parses a multi-document Helm manifest YAML string and returns
// a sorted, deduplicated list of container images found in workload specs.
// Digest suffixes (@sha256:...) are stripped; images without a tag are ignored.
func parseImages(manifest string) []string {
	seen := map[string]bool{}
	dec := yaml.NewDecoder(strings.NewReader(manifest))

	docCount := 0
	workloadCount := 0
	for {
		var obj k8sResource
		if err := dec.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			continue // skip unparseable documents
		}
		docCount++

		if !workloadKinds[obj.Kind] {
			continue
		}
		workloadCount++

		var containers []k8sContainer
		switch obj.Kind {
		case "Pod":
			containers = append(obj.Spec.Containers, obj.Spec.InitContainers...)
		case "CronJob":
			tpl := obj.Spec.JobTemplate.Spec.Template.Spec
			containers = append(tpl.Containers, tpl.InitContainers...)
		default:
			tpl := obj.Spec.Template.Spec
			containers = append(tpl.Containers, tpl.InitContainers...)
		}

		for _, c := range containers {
			img := c.Image
			if img == "" || strings.Contains(img, "{{") {
				continue
			}
			// Strip digest suffix (e.g. @sha256:abc...)
			if idx := strings.Index(img, "@"); idx >= 0 {
				img = img[:idx]
			}
			if strings.Contains(img, ":") {
				seen[img] = true
			}
		}
	}
	fmt.Printf("  parsed %d YAML documents — %d workload resource(s) found\n", docCount, workloadCount)

	images := make([]string, 0, len(seen))
	for img := range seen {
		images = append(images, img)
	}
	sort.Strings(images)
	return images
}

// mirrorImages copies each upstream image to the kluisz registry using crane.
// crane reads GCP credentials from ~/.docker/config.json (set by gcloud auth configure-docker).
// Respects ctx — each copy call is cancelled if the deadline is exceeded.
func mirrorImages(ctx context.Context, images []string, registry string) error {
	opts := []crane.Option{
		crane.WithAuthFromKeychain(authn.DefaultKeychain),
		crane.WithContext(ctx),
	}

	for i, src := range images {
		select {
		case <-ctx.Done():
			return fmt.Errorf("mirrorImages timed out after %d/%d images: %w", i, len(images), ctx.Err())
		default:
		}

		lastColon := strings.LastIndex(src, ":")
		if lastColon < 0 {
			continue
		}
		tag := src[lastColon+1:]
		base := src[:lastColon]
		name := base[strings.LastIndex(base, "/")+1:]
		dst := fmt.Sprintf("%s/%s:%s", registry, name, tag)

		fmt.Printf("  [%d/%d] %s\n         → %s\n", i+1, len(images), src, dst)
		t := time.Now()
		if err := crane.Copy(src, dst, opts...); err != nil {
			return fmt.Errorf("copy %s → %s: %w", src, dst, err)
		}
		fmt.Printf("         done in %s\n", time.Since(t).Round(time.Millisecond))
	}
	return nil
}
