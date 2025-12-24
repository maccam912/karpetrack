# Track Specification: Ephemeral Storage Awareness

## Goal
Update the `Provisioner` and `Binpacker` to account for ephemeral storage requirements. The system must verify that candidate nodes (both existing and potential new ones) have sufficient free disk space to host pods with defined `ephemeral-storage` requests.

## Requirements
1. **Input Handling:**
   - Extract `ephemeral-storage` requests from Pod resource specifications.
   - Defaults to 0 if not specified (standard Kubernetes behavior), or honor any existing defaults in the project.

2. **Binpacking Logic:**
   - **Existing Nodes:** Check actual allocatable storage on existing nodes.
   - **New Node Templates:**
     - Attempt to retrieve storage capacity for Rackspace Spot instance types via SDK/API if available.
     - **Fallback:** If API data is missing, assume a default capacity of **40 GB** for all instance types.
   - Ensure a pod is NOT scheduled on a node if `pod.request.storage > node.allocatable.storage - node.used.storage`.

3. **Optimization:**
   - Prefer placing high-storage pods on new nodes if existing nodes are storage-constrained, even if CPU/RAM would technically fit.
   - Update scoring/ranking logic to include storage as a constraint (hard limit), not just a soft priority.

4. **Configuration:**
   - Add a constant or configuration for the 40GB default fallback.

## Context
- **Current State:** The provisioner likely checks CPU and Memory but ignores disk space.
- **Problem:** Pods with large images or scratch space requirements may be scheduled on nodes that are physically full, leading to `Evicted` pods or failed starts.
- **Assumptions:** Rackspace Spot instances generally have a consistent disk size if not specified.

## Design Notes
- Modify `internal/scheduler/binpacker.go` to include storage in the `Fit` check.
- Update `internal/scheduler/requirements.go` if it handles resource extraction.
- Verify `spotnodepool_types.go` or `spotnodes` doesn't need schema changes (likely doesn't, as this is a runtime check).
