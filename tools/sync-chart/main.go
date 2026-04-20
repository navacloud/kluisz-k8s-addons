package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

type config struct {
	registry    string
	chart       string
	repo        string
	version     string
	addonValues string // optional: defaultValues.valuesYAML from addon YAML
}

const stepTimeout = 5 * time.Minute

func main() {
	cfg := config{
		registry:    mustEnv("REGISTRY"),
		chart:       mustEnv("ADDON_CHART"),
		repo:        mustEnv("ADDON_REPO"),
		version:     mustEnv("ADDON_VERSION"),
		addonValues: os.Getenv("ADDON_COMPILE_VALUES"),
	}

	if err := run(cfg); err != nil {
		log.Fatalf("sync failed: %v", err)
	}
}

func run(cfg config) error {
	total := time.Now()

	// 1. Pull upstream chart
	fmt.Printf("\n==> [1/5] pulling %s@%s from %s\n", cfg.chart, cfg.version, cfg.repo)
	t := time.Now()
	ctx1, cancel1 := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel1()
	chartDir, err := pullChart(ctx1, cfg.repo, cfg.chart, cfg.version, "./chart")
	if err != nil {
		return fmt.Errorf("pull chart: %w", err)
	}
	fmt.Printf("    done in %s — unpacked to %s\n", time.Since(t).Round(time.Millisecond), chartDir)

	// 2. Render chart and extract images from workload specs
	fmt.Println("\n==> [2/5] rendering chart and extracting container images")
	t = time.Now()
	ctx2, cancel2 := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel2()
	images, err := extractImages(ctx2, chartDir, cfg.addonValues)
	if err != nil {
		return fmt.Errorf("extract images: %w", err)
	}
	if len(images) == 0 {
		fmt.Println("  warning: no images found in rendered output")
	} else {
		fmt.Printf("  found %d image(s) in %s:\n", len(images), time.Since(t).Round(time.Millisecond))
		for _, img := range images {
			fmt.Printf("    • %s\n", img)
		}
	}

	// 3. Mirror each image to kluisz registry
	fmt.Printf("\n==> [3/5] mirroring %d image(s) to %s\n", len(images), cfg.registry)
	t = time.Now()
	ctx3, cancel3 := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel3()
	if err := mirrorImages(ctx3, images, cfg.registry); err != nil {
		return fmt.Errorf("mirror images: %w", err)
	}
	fmt.Printf("    all images mirrored in %s\n", time.Since(t).Round(time.Millisecond))

	// 4. Patch values.yaml — replace upstream image refs with kluisz registry
	fmt.Println("\n==> [4/5] patching values.yaml")
	t = time.Now()
	ctx4, cancel4 := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel4()
	if err := patchValues(ctx4, chartDir, cfg.registry, images); err != nil {
		return fmt.Errorf("patch values: %w", err)
	}
	fmt.Printf("    done in %s\n", time.Since(t).Round(time.Millisecond))

	// 5. Package and push chart to kluisz OCI registry
	fmt.Printf("\n==> [5/5] pushing chart to oci://%s/%s:%s\n", cfg.registry, cfg.chart, cfg.version)
	t = time.Now()
	ctx5, cancel5 := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel5()
	if err := pushChart(ctx5, chartDir, cfg.chart, cfg.version, cfg.registry); err != nil {
		return fmt.Errorf("push chart: %w", err)
	}
	fmt.Printf("    done in %s\n", time.Since(t).Round(time.Millisecond))

	fmt.Printf("\ndone: oci://%s/%s:%s (total: %s)\n",
		cfg.registry, cfg.chart, cfg.version, time.Since(total).Round(time.Millisecond))
	return nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}
