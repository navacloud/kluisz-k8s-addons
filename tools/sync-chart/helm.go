package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
func pullChart(repo, chart, version, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	pull := action.NewPullWithOpts(action.WithConfig(new(action.Configuration)))
	pull.Settings = cli.New()
	pull.RepoURL = repo
	pull.Version = version
	pull.Untar = true
	pull.UntarDir = destDir

	out, err := pull.Run(chart)
	if out != "" {
		fmt.Print(out)
	}
	if err != nil {
		return "", fmt.Errorf("helm pull: %w", err)
	}
	return filepath.Join(destDir, chart), nil
}

// renderChart loads the chart from disk and renders all templates using the
// Helm engine in LintMode. LintMode turns `required` and `fail` calls into
// warnings instead of hard errors, so charts that need cluster context
// (cert-manager, cilium) still produce workload output for image extraction.
func renderChart(chartDir string) (string, error) {
	ch, err := loader.Load(chartDir)
	if err != nil {
		return "", fmt.Errorf("load chart: %w", err)
	}

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

	eng := engine.Engine{LintMode: true}
	rendered, err := eng.Render(ch, vals)
	if err != nil && len(rendered) == 0 {
		return "", nil
	}

	var buf strings.Builder
	for name, content := range rendered {
		if strings.TrimSpace(content) == "" || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		buf.WriteString(content)
	}
	return buf.String(), nil
}

// pushChart packages the chart directory into a .tgz and pushes it to the OCI registry.
// Authenticates using Google ADC (GOOGLE_APPLICATION_CREDENTIALS set by google-github-actions/auth).
func pushChart(chartDir, chart, version, dst string) error {
	pkg := action.NewPackage()
	pkg.Destination = "."
	tgzPath, err := pkg.Run(chartDir, nil)
	if err != nil {
		return fmt.Errorf("helm package: %w", err)
	}

	// Get a GCP access token from Application Default Credentials.
	// google-github-actions/auth@v2 sets GOOGLE_APPLICATION_CREDENTIALS, so
	// FindDefaultCredentials picks it up without any CLI involvement.
	ctx := context.Background()
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return fmt.Errorf("GCP credentials: %w", err)
	}
	token, err := creds.TokenSource.Token()
	if err != nil {
		return fmt.Errorf("get GCP token: %w", err)
	}

	// Create a Helm registry client and log in using the access token as
	// a password — identical to: helm registry login -u oauth2accesstoken ...
	rc, err := helmregistry.NewClient(helmregistry.ClientOptEnableCache(true))
	if err != nil {
		return fmt.Errorf("registry client: %w", err)
	}
	// dst is like asia-south1-docker.pkg.dev/project/repo — take just the host.
	host := dst
	if idx := len(dst); idx > 0 {
		for i, c := range dst {
			if c == '/' {
				host = dst[:i]
				break
			}
		}
	}
	if err := rc.Login(host,
		helmregistry.LoginOptBasicAuth("oauth2accesstoken", token.AccessToken),
	); err != nil {
		return fmt.Errorf("registry login: %w", err)
	}

	cfg := &action.Configuration{RegistryClient: rc}
	push := action.NewPushWithOpts(action.WithPushConfig(cfg))
	push.Settings = cli.New()

	out, err := push.Run(tgzPath, "oci://"+dst)
	if out != "" {
		fmt.Print(out)
	}
	if err != nil {
		return fmt.Errorf("helm push: %w", err)
	}
	return nil
}
