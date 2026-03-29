# kluisz-k8s-addons

Centralized catalog and sync pipeline for Kubernetes addons. Upstream Helm charts and their container images are mirrored to a private OCI registry so clusters can install addons without reaching the public internet.

## How it works

```
addons/**/*.yaml   ──>   GitHub Actions   ──>   sync-chart (Go)   ──>   OCI Registry
                         detects new                                    (Artifact Registry)
                         chartVersions
```

When an addon file is pushed to `main` with a new `chartVersion`, the CI pipeline:

1. **Pulls** the upstream Helm chart (OCI or HTTP repo)
2. **Renders** templates and extracts container images from workload specs
3. **Mirrors** each image to the private registry via [crane](https://github.com/google/go-containerregistry)
4. **Patches** `values.yaml` to point at the mirrored images
5. **Pushes** the modified chart to `oci://asia-south1-docker.pkg.dev/.../kluisz-managed-k8s-public-repository`

## Addon catalog

| Category | Addon | Chart | Versions | Source |
|----------|-------|-------|----------|--------|
| Networking | [cilium](addons/networking/cilium.yaml) | `cilium` | 1.19.1 | helm.cilium.io |
| Networking | [coredns](addons/networking/coredns.yaml) | `coredns` | 1.45.2 | ghcr.io/coredns |
| Networking | [traefik](addons/networking/traefik.yaml) | `traefik` | 39.0.0 | ghcr.io/traefik |
| Networking | [kluisz-multus](addons/networking/kluisz-multus.yaml) | `kluisz-multus` | 0.1.0 | internal |
| Monitoring | [metrics-server](addons/monitoring/metrics-server.yaml) | `metrics-server` | 3.12.0 | kubernetes-sigs |
| Monitoring | [kube-state-metrics](addons/monitoring/kube-state-metrics.yaml) | `kube-state-metrics` | 7.0.0 | prometheus-community |
| Security | [cert-manager](addons/security/cert-manager.yaml) | `cert-manager` | v1.20.0, v1.19.2 | quay.io/jetstack |
| Storage | [kluisz-csi-driver](addons/storage/kluisz-csi-driver.yaml) | `kluisz-csi-driver` | v0.1.0-alpha.1 | internal |
| Other | [kluisz-cluster-autoscaler](addons/other/kluisz-cluster-autoscaler.yaml) | `kluisz-cluster-autoscaler` | 0.1.0 | internal |
| Other | [kluisz-nodepool-provider](addons/other/kluisz-nodepool-provider.yaml) | `kluisz-nodepool-provider` | v0.1.0-rc1 | internal |

## Adding a new addon

Create a YAML file under the appropriate `addons/<category>/` directory:

```yaml
name: my-addon
description: "What this addon does"
category: NETWORKING          # NETWORKING | SECURITY | MONITORING | STORAGE | OTHER
namespace: my-namespace
chart: my-addon
originalRepo: "oci://ghcr.io/org/charts/my-addon"   # or https:// for HTTP repos
versions:
  - version: "1.0.0"
    chartVersion: "1.0.0"
    minK8sVersion: "1.31"
    maxK8sVersion: "1.35"
```

Open a PR. The **validate-addon-charts** workflow will verify that the chart and version are pullable from the upstream repo. Once merged, the **sync-addon-charts** workflow mirrors everything to the private registry.

For internal charts (no upstream), omit `originalRepo` -- these are pushed to the registry through other means and the sync pipeline skips them.

See [`schema/addon.schema.json`](schema/addon.schema.json) for the full schema reference.

## Installing an addon

```bash
helm install <addon-name> \
  oci://asia-south1-docker.pkg.dev/<project>/kluisz-managed-k8s-public-repository/<chart> \
  --version <version> \
  --namespace <namespace> \
  --create-namespace
```

Example:

```bash
helm install cilium \
  oci://asia-south1-docker.pkg.dev/<project>/kluisz-managed-k8s-public-repository/cilium \
  --version 1.19.1 \
  --namespace kube-system
```

## Repository structure

```
addons/
  monitoring/          kube-state-metrics, metrics-server
  networking/          cilium, coredns, traefik, kluisz-multus
  security/            cert-manager
  storage/             kluisz-csi-driver
  other/               kluisz-cluster-autoscaler, kluisz-nodepool-provider

tools/sync-chart/      Go application (5-step sync pipeline)
  main.go              Entry point and orchestration
  helm.go              Chart pull (step 1) and push (step 5)
  images.go            Image extraction (step 2) and mirroring (step 3)
  patch.go             values.yaml patching (step 4)

.github/workflows/
  sync-addon-charts.yml       Syncs new versions on push to main
  validate-addon-charts.yml   Validates chart pullability on PRs

schema/addon.schema.json      JSON schema for addon definition files
config.yaml                   OCI registry URL
```

## CI/CD workflows

### sync-addon-charts (push to main / manual)

Detects addon files with new `chartVersion` entries, then runs the sync-chart pipeline for each. Uses GCP Workload Identity Federation for registry authentication. Jobs run in parallel with `fail-fast: false`.

Can also be triggered manually via `workflow_dispatch` to re-sync all addons.

### validate-addon-charts (pull requests)

Same detection logic, but only validates that the upstream chart exists and is pullable -- no GCP auth or image mirroring needed. Catches typos and non-existent versions before they reach `main`.

## Development

### Running sync-chart locally

```bash
cd tools/sync-chart

# Set required env vars
export REGISTRY=asia-south1-docker.pkg.dev/<project>/kluisz-managed-k8s-public-repository
export ADDON_CHART=cilium
export ADDON_REPO=https://helm.cilium.io
export ADDON_VERSION=1.19.1

# Authenticate to Artifact Registry
gcloud auth configure-docker asia-south1-docker.pkg.dev --quiet

go run .
```

### Running tests

```bash
cd tools/sync-chart
go test ./...
```
