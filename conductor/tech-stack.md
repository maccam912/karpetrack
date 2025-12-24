# Tech Stack - Karpetrack

## Core Technologies
- **Language:** Go (v1.23.5)
- **Framework:** Kubernetes Controller-Runtime (v0.18.0)
- **API Integration:** Rackspace Spot Go SDK (v0.1.0)
- **Target Platform:** Kubernetes (v1.28+)

## Development & Operations
- **Build System:** Makefile
- **Containerization:** Docker
- **Deployment:** Helm, Flux (GitOps)
- **CI/CD:** GitHub Actions (defined in `.github/workflows/`)

## Infrastructure
- **Cloud Provider:** Rackspace Spot
- **CRDs:** `SpotNodePool`, `SpotNode` (v1alpha1)
