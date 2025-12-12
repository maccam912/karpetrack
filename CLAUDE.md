# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Karpetrack is a Kubernetes controller inspired by Karpenter that automates the management of Rackspace Spot instances. It watches for unschedulable pods, provisions optimal spot instances via cost optimization, monitors prices for cost-saving opportunities, and consolidates underutilized nodes.

## Build Commands

```bash
make build            # Build manager binary to ./bin/controller
make test             # Run tests with coverage (includes fmt + vet)
make run              # Run controller locally (requires credentials or --mock-mode)
make fmt              # Format code with go fmt
make vet              # Run go vet
make lint             # Run golangci-lint
make manifests        # Generate CRDs from code annotations
make generate         # Generate DeepCopy methods
make install          # Install CRDs to cluster
make deploy           # Deploy controller to cluster
make docker-build     # Build Docker image
```

Run a single test:
```bash
go test -v ./internal/spot/... -run TestFlexInt
```

Run locally without credentials:
```bash
go run ./cmd/controller --mock-mode
```

## Architecture

Four reconciliation controllers in `internal/controller/`:

1. **ProvisionerController** (`provisioner.go`) - Watches for unschedulable pods, batches them (10s window), uses CostOptimizer to find optimal node configurations, creates SpotNode resources

2. **SpotNodeController** (`node_controller.go`) - Manages SpotNode lifecycle through phases: Pending → Provisioning → Running → Draining → Terminating. Calls Spot API to create/terminate instances

3. **OptimizerController** (`optimizer.go`) - Monitors spot prices every 30s, replaces nodes if cheaper alternatives save >= 20%

4. **ScalerController** (`scaler.go`) - Removes empty/underutilized nodes after 5m grace period

Supporting packages:

- `internal/spot/` - Spot API client (with mock mode), pricing provider (fetches from S3, caches 30s)
- `internal/scheduler/` - CostOptimizer engine and BinPacker with FFD/Best-Fit strategies
- `api/v1alpha1/` - CRD definitions for SpotNodePool (cluster-scoped) and SpotNode

## Key Patterns

- Uses controller-runtime with finalizers for cleanup
- SpotNode phases managed via status subresource
- PricingProvider caches S3 pricing data with TTL
- BinPacker tries multiple strategies (FFD, Best-Fit, SingleLargeNode) and picks lowest cost
- Category aliases in pricing: gp→General Purpose, ch→Compute Heavy, mh→Memory Heavy, gpu→GPU

## Environment Variables

```bash
RACKSPACE_SPOT_REFRESH_TOKEN    # Spot API token
RACKSPACE_SPOT_CLOUDSPACE_ID    # Spot cloudspace ID
```

## Deployment

Helm chart in `charts/karpetrack/`:
```bash
helm install karpetrack ./charts/karpetrack -n karpetrack-system --create-namespace
```

Key Helm values: `credentials.refreshToken`, `credentials.cloudspaceId`, `controller.mockMode`
