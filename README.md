# Karpetrack

A Kubernetes controller for managing Rackspace Spot instances with automatic
cost optimization, inspired by [Karpenter](https://karpenter.sh/).

## Features

- **Automatic Node Provisioning**: Watches for unschedulable pods and provisions
  optimal spot instances
- **Cost Optimization**: Continuously monitors spot prices and replaces nodes
  with cheaper alternatives (configurable threshold, default 20% savings)
- **Node Consolidation**: Removes empty or underutilized nodes to reduce costs
- **Multi-Region Support**: Can provision nodes across multiple Rackspace Spot
  regions
- **Instance Categories**: Supports gp (general purpose), ch (compute heavy), mh
  (memory heavy), and gpu categories

## Prerequisites

- Kubernetes cluster (1.28+)
- [Rackspace Spot](https://spot.rackspace.com/) account with:
  - Refresh Token
  - Cloudspace ID
- Helm 3.x (for Helm installation)
- [Flux](https://fluxcd.io/) v2 (for GitOps installation)

## Installation

### Option 1: Helm

1. **Create the namespace:**

   ```bash
   kubectl create namespace karpetrack-system
   ```

2. **Create a secret with your Rackspace Spot credentials:**

   ```bash
   kubectl create secret generic karpetrack-credentials \
     --namespace karpetrack-system \
     --from-literal=refresh-token=YOUR_REFRESH_TOKEN \
     --from-literal=cloudspace-id=YOUR_CLOUDSPACE_ID
   ```

3. **Install the Helm chart:**

   ```bash
   helm install karpetrack ./charts/karpetrack \
     --namespace karpetrack-system \
     --set credentials.create=false \
     --set credentials.existingSecret=karpetrack-credentials
   ```

   Or, install directly with credentials (less secure, stores in Helm release):

   ```bash
   helm install karpetrack ./charts/karpetrack \
     --namespace karpetrack-system \
     --set credentials.refreshToken=YOUR_REFRESH_TOKEN \
     --set credentials.cloudspaceId=YOUR_CLOUDSPACE_ID
   ```

### Option 2: Flux (GitOps)

1. **Add the credentials secret to your cluster:**

   Create a `SealedSecret` or `ExternalSecret` for your credentials, or create
   manually:

   ```yaml
   # karpetrack-credentials.yaml (encrypt this with SealedSecrets or SOPS!)
   apiVersion: v1
   kind: Secret
   metadata:
       name: karpetrack-credentials
       namespace: karpetrack-system
   type: Opaque
   stringData:
       refresh-token: YOUR_REFRESH_TOKEN
       cloudspace-id: YOUR_CLOUDSPACE_ID
   ```

2. **Create a `HelmRepository` source (if using OCI registry):**

   ```yaml
   # helmrepository.yaml
   apiVersion: source.toolkit.fluxcd.io/v1
   kind: HelmRepository
   metadata:
       name: karpetrack
       namespace: flux-system
   spec:
       type: oci
       interval: 5m
       url: oci://ghcr.io/karpetrack
   ```

   Or for Git-based installation:

   ```yaml
   # gitrepository.yaml
   apiVersion: source.toolkit.fluxcd.io/v1
   kind: GitRepository
   metadata:
       name: karpetrack
       namespace: flux-system
   spec:
       interval: 5m
       url: https://github.com/karpetrack/karpetrack
       ref:
           branch: main
   ```

3. **Create a `HelmRelease`:**

   ```yaml
   # helmrelease.yaml
   apiVersion: helm.toolkit.fluxcd.io/v2
   kind: HelmRelease
   metadata:
       name: karpetrack
       namespace: karpetrack-system
   spec:
       interval: 30m
       chart:
           spec:
               # For OCI registry:
               chart: karpetrack
               version: ">=0.1.0"
               sourceRef:
                   kind: HelmRepository
                   name: karpetrack
                   namespace: flux-system
               # For Git repository:
               # chart: ./charts/karpetrack
               # sourceRef:
               #   kind: GitRepository
               #   name: karpetrack
               #   namespace: flux-system
       values:
           credentials:
               create: false
               existingSecret: karpetrack-credentials
           controller:
               optimizationThreshold: "0.15" # 15% savings threshold
               pricingRefreshInterval: "60s"
   ```

4. **Apply the manifests to your Fleet repository and let Flux sync:**

   ```bash
   git add helmrepository.yaml helmrelease.yaml
   git commit -m "Add karpetrack"
   git push
   flux reconcile source git flux-system
   ```

## Configuration

### Helm Values

| Parameter                           | Description                          | Default                         |
| ----------------------------------- | ------------------------------------ | ------------------------------- |
| `replicaCount`                      | Number of controller replicas        | `2`                             |
| `image.repository`                  | Container image repository           | `ghcr.io/karpetrack/karpetrack` |
| `image.tag`                         | Container image tag                  | `appVersion`                    |
| `credentials.create`                | Create credentials secret            | `true`                          |
| `credentials.existingSecret`        | Use existing secret                  | `""`                            |
| `credentials.refreshToken`          | Rackspace Spot refresh token         | `""`                            |
| `credentials.cloudspaceId`          | Rackspace Spot cloudspace ID         | `""`                            |
| `controller.leaderElect`            | Enable leader election               | `true`                          |
| `controller.pricingRefreshInterval` | Price check interval                 | `30s`                           |
| `controller.optimizationThreshold`  | Min savings to trigger replacement   | `0.20`                          |
| `controller.consolidationEnabled`   | Enable node consolidation            | `true`                          |
| `controller.batchWindow`            | Pod batching window                  | `10s`                           |
| `controller.emptyNodeGracePeriod`   | Time before removing empty nodes     | `5m`                            |
| `controller.mockMode`               | Enable mock mode (no real API calls) | `false`                         |
| `installCRDs`                       | Install CRDs with Helm               | `true`                          |

## Usage

### Create a SpotNodePool

After installation, create a `SpotNodePool` to define how nodes should be
provisioned:

```yaml
apiVersion: karpetrack.io/v1alpha1
kind: SpotNodePool
metadata:
    name: default
spec:
    # Allowed regions (empty = all regions)
    regions:
        - us-east-iad-1
        - us-central-dfw-1

    # Instance categories: gp, ch, mh, gpu
    categories:
        - gp
        - ch

    # Maximum hourly price per node (optional)
    maxPrice: "0.50"

    # Resource limits for the entire pool
    limits:
        cpu: "100"
        memory: "200Gi"

    # Node disruption settings
    disruption:
        consolidationPolicy: WhenUnderutilized
        consolidateAfter: "30m"
        expireAfter: "24h"

    # Labels to apply to provisioned nodes
    labels:
        team: platform
        environment: production
```

### View Managed Resources

```bash
# List SpotNodePools
kubectl get spotnodepools
# or
kubectl get snp

# List SpotNodes
kubectl get spotnodes
# or
kubectl get sn

# Detailed view
kubectl describe snp default
kubectl describe sn <node-name>
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Karpetrack Controller                       │
├─────────────┬─────────────────┬─────────────────┬───────────────┤
│ Provisioner │    Optimizer    │     Scaler      │   Spot Client │
│             │                 │                 │               │
│ Watches     │ Monitors prices │ Consolidates    │ Rackspace     │
│ pending     │ Replaces nodes  │ empty nodes     │ Spot API      │
│ pods        │ for savings     │ Removes unused  │               │
└─────────────┴─────────────────┴─────────────────┴───────────────┘
                           │
                           ▼
              ┌────────────────────────┐
              │   Rackspace Spot API   │
              └────────────────────────┘
```

## Troubleshooting

### Check Controller Logs

```bash
kubectl logs -n karpetrack-system -l app.kubernetes.io/name=karpetrack -f
```

### Common Issues

1. **Nodes not being provisioned:**
   - Check if SpotNodePool exists and has valid configuration
   - Verify credentials are correct
   - Check controller logs for API errors

2. **"Rackspace Spot refresh token is required" error:**
   - Ensure the secret exists and has correct keys
   - Verify secret is in the same namespace as the controller

3. **Nodes stuck in Pending phase:**
   - Check Rackspace Spot console for quota limits
   - Verify the requested instance types are available in the selected regions

## Development

```bash
# Run locally (mock mode)
make run ARGS="--mock-mode"

# Build Docker image
make docker-build

# Run tests
make test

# Generate CRDs
make manifests
```

## License

Apache License 2.0
