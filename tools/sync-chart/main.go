package main

import (
	"fmt"
	"log"
	"os"
)

type config struct {
	registry string
	chart    string
	repo     string
	version  string
}

func main() {
	cfg := config{
		registry: mustEnv("REGISTRY"),
		chart:    mustEnv("ADDON_CHART"),
		repo:     mustEnv("ADDON_REPO"),
		version:  mustEnv("ADDON_VERSION"),
	}

	if err := run(cfg); err != nil {
		log.Fatalf("sync failed: %v", err)
	}
}

func run(cfg config) error {
	// 1. Pull upstream chart
	fmt.Printf("==> pulling %s@%s from %s\n", cfg.chart, cfg.version, cfg.repo)
	chartDir, err := pullChart(cfg.repo, cfg.chart, cfg.version, "./chart")
	if err != nil {
		return fmt.Errorf("pull chart: %w", err)
	}

	// 2. Render chart and extract images from workload specs
	fmt.Println("==> extracting images from rendered workload specs")
	images, err := extractImages(chartDir)
	if err != nil {
		return fmt.Errorf("extract images: %w", err)
	}
	if len(images) == 0 {
		fmt.Println("  warning: no images found in rendered output")
	} else {
		for _, img := range images {
			fmt.Printf("  found: %s\n", img)
		}
	}

	// 3. Mirror each image to kluisz registry
	fmt.Println("==> mirroring images to kluisz registry")
	if err := mirrorImages(images, cfg.registry); err != nil {
		return fmt.Errorf("mirror images: %w", err)
	}

	// 4. Patch values.yaml — replace upstream image refs with kluisz registry
	fmt.Println("==> patching values.yaml")
	if err := patchValues(chartDir, cfg.registry, images); err != nil {
		return fmt.Errorf("patch values: %w", err)
	}

	// 5. Package and push chart to kluisz OCI registry
	fmt.Printf("==> pushing chart oci://%s/%s:%s\n", cfg.registry, cfg.chart, cfg.version)
	if err := pushChart(chartDir, cfg.chart, cfg.version, cfg.registry); err != nil {
		return fmt.Errorf("push chart: %w", err)
	}

	fmt.Printf("done: oci://%s/%s:%s\n", cfg.registry, cfg.chart, cfg.version)
	return nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}
