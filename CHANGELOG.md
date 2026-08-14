# Changelog

## [0.2.0] - 2026-08-14

### Breaking

- `spec.data[].remoteRefKey` renamed to `spec.data[].secretKey`. Existing `PushEncryptedSecret` resources must be updated to use the new field name.
- AWS SSM Parameter Store sync is now opt-in via `spec.parameterStore.keyPathPrefix`, and the resulting parameter name is `keyPathPrefix + "/" + secretKey` rather than a full path supplied per key. Resources relying on the previous behavior (every key unconditionally pushed to SSM under a full path given as `remoteRefKey`) must add a `parameterStore` block to keep syncing to SSM.
- Module path renamed from `github.com/encryptor` to `github.com/encrypted-secrets`; Go bumped to 1.26 and dependencies updated accordingly.

### Added

- The controller now always creates/maintains a Kubernetes `Secret` (named/namespaced like the `PushEncryptedSecret`) with the decrypted values, independent of whether SSM Parameter Store sync is enabled.
- New `status.secretResourceVersion` field: the controller detects when the managed Secret has been modified or deleted out-of-band (a key manually removed or edited) and automatically resyncs it from the CRD, without needing to re-decrypt via KMS to check.
- `spec.kmsKeyId` can override the controller's default KMS key on a per-resource basis.

### Fixed

- The periodic reconcile loop previously stopped self-scheduling for any resource once it synced successfully, leaving only resources already sitting in a steady "no changes" state being rechecked. All resources are now rechecked on a fixed interval.
- `ssm:DeleteParameter` calls (on resource deletion, and when an individual key is removed from spec) no longer abort on the first failure — every key is attempted, and only genuine (non-"already deleted") errors are surfaced, preventing orphaned SSM parameters.
- Removed blocking `time.Sleep` calls used to throttle retries after AWS errors; with the default of one reconcile at a time, these serialized the entire controller behind a single failing resource. Retries now rely on controller-runtime's built-in exponential backoff.
- `PushEncryptedSecret.status` updates are now retried against a freshly-fetched copy on write conflicts (e.g. a GitOps tool such as ArgoCD re-applying the resource concurrently), instead of failing outright.
- The Helm chart's CRD template (`deploy/chart/templates/pushencryptedsecret-crd.yaml`) was significantly out of date (missing `kmsKeyId`, `parameterStore`, `secretResourceVersion`, and still using `remoteRefKey`) and is now regenerated to match the current schema.
- Removed duplicate/stale CRD manifest files under `config/crd/bases/` left over from an earlier API group rename.

### Known limitations

- Cleanup of an individual SSM parameter after its key is removed from `spec.data` (while the resource itself is kept) runs synchronously during event filtering and is not retried on failure; a transient AWS error at that moment can leave the parameter orphaned. Deletion of *all* parameters when the whole resource is deleted does not have this limitation. Planned for the next release.

## [0.1.0] - 2023-10-31

Initial release.
