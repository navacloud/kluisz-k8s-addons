package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/engine"
	helmregistry "helm.sh/helm/v3/pkg/registry"
)

// pullChart fetches the chart from the upstream HTTP repo and unpacks it into destDir.
// Returns the path to the unpacked chart directory (destDir/<chart>).
// Respects ctx — if the pull takes longer than the deadline, returns a timeout error.
func pullChart(ctx context.Context, repo, chart, version, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	pull := action.NewPullWithOpts(action.WithConfig(new(action.Configuration)))
	pull.Settings = cli.New()
	pull.RepoURL = repo
	pull.Version = version
	pull.Untar = true
	pull.UntarDir = destDir

	fmt.Printf("  fetching chart index from %s\n", repo)

	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		t := time.Now()
		out, err := pull.Run(chart)
		if err == nil {
			fmt.Printf("  download complete in %s\n", time.Since(t).Round(time.Millisecond))
		}
		ch <- result{out, err}
	}()

	select {
	case res := <-ch:
		if res.out != "" {
			fmt.Print(res.out)
		}
		if res.err != nil {
			return "", fmt.Errorf("helm pull: %w", res.err)
		}
		chartPath := filepath.Join(destDir, chart)
		fmt.Printf("  chart unpacked to %s\n", chartPath)
		return chartPath, nil
	case <-ctx.Done():
		return "", fmt.Errorf("pullChart exceeded 5-minute timeout: %w", ctx.Err())
	}
}

// renderChart loads the chart from disk and renders all templates using the
// Helm engine in LintMode. LintMode turns `required` and `fail` calls into
// warnings instead of hard errors, so charts that need cluster context
// (cert-manager, cilium) still produce workload output for image extraction.
// Respects ctx — if rendering takes longer than the deadline, returns a timeout error.
func renderChart(ctx context.Context, chartDir string) (string, error) {
	fmt.Printf("  loading chart from %s\n", chartDir)
	ch, err := loader.Load(chartDir)
	if err != nil {
		return "", fmt.Errorf("load chart: %w", err)
	}
	fmt.Printf("  loaded chart %q version %s (%d templates)\n",
		ch.Name(), ch.Metadata.Version, len(ch.Templates))

	vals, err := chartutil.ToRenderValues(ch, map[string]interface{}{},
		chartutil.ReleaseOptions{
			Name:      "probe",
			Namespace: "default",
			IsInstall: true,
		},
		chartutil.DefaultCapabilities,
	)
	if err != nil {
		return "", nil // non-fatal
	}

	fmt.Println("  rendering templates (LintMode — required/fail calls become warnings)")
	t := time.Now()

	type result struct {
		rendered map[string]string
		err      error
	}
	ch2 := make(chan result, 1)
	go func() {
		eng := engine.Engine{LintMode: true}
		r, e := eng.Render(ch, vals)
		ch2 <- result{r, e}
	}()

	var rendered map[string]string
	select {
	case res := <-ch2:
		if res.err != nil && len(res.rendered) == 0 {
			fmt.Printf("  render returned error with no output: %v\n", res.err)
			return "", nil
		}
		if res.err != nil {
			fmt.Printf("  render warnings: %v\n", res.err)
		}
		rendered = res.rendered
	case <-ctx.Done():
		return "", fmt.Errorf("renderChart exceeded 5-minute timeout: %w", ctx.Err())
	}

	var buf strings.Builder
	yamlCount := 0
	for name, content := range rendered {
		if strings.TrimSpace(content) == "" || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		buf.WriteString(content)
		yamlCount++
	}
	fmt.Printf("  rendered %d non-empty YAML template(s) in %s\n",
		yamlCount, time.Since(t).Round(time.Millisecond))
	return buf.String(), nil
}

// pushChart packages the chart directory into a .tgz and pushes it to the OCI registry.
// Authenticates using Google ADC (GOOGLE_APPLICATION_CREDENTIALS set by google-github-actions/auth).
// Respects ctx for the GCP token fetch and for the push operation itself.
func pushChart(ctx context.Context, chartDir, chart, version, dst string) error {
	fmt.Printf("  packaging chart from %s\n", chartDir)

	type packResult struct {
		path string
		err  error
	}
	packCh := make(chan packResult, 1)
	go func() {
		pkg := action.NewPackage()
		pkg.Destination = "."
		path, err := pkg.Run(chartDir, nil)
		packCh <- packResult{path, err}
	}()

	var tgzPath string
	select {
	case res := <-packCh:
		if res.err != nil {
			return fmt.Errorf("helm package: %w", res.err)
		}
		tgzPath = res.path
		fmt.Printf("  packaged to %s\n", tgzPath)
	case <-ctx.Done():
		return fmt.Errorf("helm package exceeded 5-minute timeout: %w", ctx.Err())
	}

	// Get a GCP access token from Application Default Credentials.
	fmt.Println("  fetching GCP access token (Application Default Credentials)")
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return fmt.Errorf("GCP credentials: %w", err)
	}
	token, err := creds.TokenSource.Token()
	if err != nil {
		return fmt.Errorf("get GCP token: %w", err)
	}
	fmt.Println("  GCP token obtained")

	rc, err := helmregistry.NewClient(helmregistry.ClientOptEnableCache(true))
	if err != nil {
		return fmt.Errorf("registry client: %w", err)
	}

	// Extract host from dst (e.g. asia-south1-docker.pkg.dev/project/repo → asia-south1-docker.pkg.dev)
	host := dst
	for i, c := range dst {
		if c == '/' {
			host = dst[:i]
			break
		}
	}
	fmt.Printf("  logging in to %s\n", host)
	if err := rc.Login(host,
		helmregistry.LoginOptBasicAuth("oauth2accesstoken", token.AccessToken),
	); err != nil {
		return fmt.Errorf("registry login: %w", err)
	}
	fmt.Printf("  logged in to %s\n", host)

	cfg := &action.Configuration{RegistryClient: rc}
	push := action.NewPushWithOpts(action.WithPushConfig(cfg))
	push.Settings = cli.New()

	fmt.Printf("  pushing %s → oci://%s\n", tgzPath, dst)
	t := time.Now()

	type pushResult struct {
		out string
		err error
	}
	pushCh := make(chan pushResult, 1)
	go func() {
		out, err := push.Run(tgzPath, "oci://"+dst)
		pushCh <- pushResult{out, err}
	}()

	select {
	case res := <-pushCh:
		if res.out != "" {
			fmt.Print(res.out)
		}
		if res.err != nil {
			return fmt.Errorf("helm push: %w", res.err)
		}
		fmt.Printf("  push complete in %s\n", time.Since(t).Round(time.Millisecond))
		return nil
	case <-ctx.Done():
		return fmt.Errorf("helm push exceeded 5-minute timeout: %w", ctx.Err())
	}
}
