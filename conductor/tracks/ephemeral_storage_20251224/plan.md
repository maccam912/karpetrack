# Track Plan: Ephemeral Storage Awareness

## Phase 1: Research & Definition [checkpoint: 316f37f]
- [x] Task: Analyze current resource extraction logic in `binpacker.go` and `requirements.go`. 18742ab
- [x] Task: Investigate Rackspace Spot SDK/API capabilities regarding disk size reporting. 359364b
- [x] Task: Define the "Default Storage Capacity" constant (40GB) in a central configuration or constants file. ac4797c
- [x] Task: Conductor - User Manual Verification 'Research & Definition' (Protocol in workflow.md) 316f37f

## Phase 2: Implementation - Resource Modeling [checkpoint: fd6fdf4]
- [x] Task: Update internal node representation to include `EphemeralStorage` in available resources. 76b3f71
    - [x] Sub-task: Write Tests: Verify storage resource parsing from Pod specs. 76b3f71
    - [x] Sub-task: Implement Feature: Add storage extraction to `requirements.go` or equivalent. 76b3f71
- [x] Task: Implement storage capacity lookup for instance types. d674136
    - [x] Sub-task: Write Tests: Mock API responses and verify fallback to 40GB. d674136
    - [x] Sub-task: Implement Feature: Add logic to fetch or default storage size for candidate instance types. d674136
- [x] Task: Conductor - User Manual Verification 'Implementation - Resource Modeling' (Protocol in workflow.md) fd6fdf4

## Phase 3: Implementation - Binpacking Logic [checkpoint: 8220616]
- [x] Task: Update `Binpacker` to enforce storage constraints. 553c379
    - [x] Sub-task: Write Tests: Create scenarios where CPU/RAM fit but Storage fails. 553c379
    - [x] Sub-task: Implement Feature: Add `storage` check to the `Fit` function in `binpacker.go`. 553c379
- [x] Task: Update `Provisioner` to use the new binpacking logic for new node creation. 8220616
    - [x] Sub-task: Write Tests: Verify a new node is provisioned when existing nodes lack storage. 8220616
    - [x] Sub-task: Implement Feature: Ensure provisioner respects the hard constraint. 8220616
- [~] Task: Conductor - User Manual Verification 'Implementation - Binpacking Logic' (Protocol in workflow.md)

## Phase 4: Verification
- [ ] Task: Run full suite of unit tests to ensure no regressions in CPU/RAM scheduling.
- [ ] Task: Conductor - User Manual Verification 'Verification' (Protocol in workflow.md)
