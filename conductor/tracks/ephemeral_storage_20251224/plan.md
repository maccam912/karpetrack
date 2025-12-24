# Track Plan: Ephemeral Storage Awareness

## Phase 1: Research & Definition
- [x] Task: Analyze current resource extraction logic in `binpacker.go` and `requirements.go`. 18742ab
- [x] Task: Investigate Rackspace Spot SDK/API capabilities regarding disk size reporting. 359364b
- [x] Task: Define the "Default Storage Capacity" constant (40GB) in a central configuration or constants file. ac4797c
- [~] Task: Conductor - User Manual Verification 'Research & Definition' (Protocol in workflow.md)

## Phase 2: Implementation - Resource Modeling
- [ ] Task: Update internal node representation to include `EphemeralStorage` in available resources.
    - [ ] Sub-task: Write Tests: Verify storage resource parsing from Pod specs.
    - [ ] Sub-task: Implement Feature: Add storage extraction to `requirements.go` or equivalent.
- [ ] Task: Implement storage capacity lookup for instance types.
    - [ ] Sub-task: Write Tests: Mock API responses and verify fallback to 40GB.
    - [ ] Sub-task: Implement Feature: Add logic to fetch or default storage size for candidate instance types.
- [ ] Task: Conductor - User Manual Verification 'Implementation - Resource Modeling' (Protocol in workflow.md)

## Phase 3: Implementation - Binpacking Logic
- [ ] Task: Update `Binpacker` to enforce storage constraints.
    - [ ] Sub-task: Write Tests: Create scenarios where CPU/RAM fit but Storage fails.
    - [ ] Sub-task: Implement Feature: Add `storage` check to the `Fit` function in `binpacker.go`.
- [ ] Task: Update `Provisioner` to use the new binpacking logic for new node creation.
    - [ ] Sub-task: Write Tests: Verify a new node is provisioned when existing nodes lack storage.
    - [ ] Sub-task: Implement Feature: Ensure provisioner respects the hard constraint.
- [ ] Task: Conductor - User Manual Verification 'Implementation - Binpacking Logic' (Protocol in workflow.md)

## Phase 4: Verification
- [ ] Task: Run full suite of unit tests to ensure no regressions in CPU/RAM scheduling.
- [ ] Task: Conductor - User Manual Verification 'Verification' (Protocol in workflow.md)
