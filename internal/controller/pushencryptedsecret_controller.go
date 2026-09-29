/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	encryptoriov1beta1 "github.com/encrypted-secrets/api/v1beta1"
	"github.com/encrypted-secrets/internal/conn"
	"github.com/mitchellh/hashstructure"
)

// PushEncryptedSecretReconciler reconciles a PushEncryptedSecret object
type PushEncryptedSecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	finalizerName = "pushencryptedsecrets.io/finalizer"
	OP_DELETE     = "DELETE"
	OP_UPDATE     = "UPDATE"
	OP_CREATE     = "CREATE"

	// reconcileIntervalSeconds is the periodic recheck interval used to compare
	// spec hash against status.hash without decrypting via KMS, so a resync is
	// still guaranteed even if an Update event predicate is missed, without
	// paying the KMS decrypt cost on every reconcile.
	reconcileIntervalSeconds = 60

	// failedSyncRetryInterval bounds how often a resource whose last sync failed
	// is retried while its spec is unchanged. Most such failures are permanent
	// (e.g. InvalidCiphertextException) and would fail identically, but some are
	// transient (throttling, IAM propagation) and need an eventual retry.
	failedSyncRetryInterval = 15 * time.Minute
)

//+kubebuilder:rbac:groups=encryptor.dev,resources=pushencryptedsecrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=encryptor.dev,resources=pushencryptedsecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=encryptor.dev,resources=pushencryptedsecrets/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PushEncryptedSecret object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.0/pkg/reconcile
func (r *PushEncryptedSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.Log.WithName("PushEncryptedSecret").WithValues("Namespace", req.Namespace)

	encryptedSecret := &encryptoriov1beta1.PushEncryptedSecret{}

	if err := r.Get(ctx, req.NamespacedName, encryptedSecret); err != nil {
		if apierrs.IsNotFound(err) {
			logger.Info("PushEncryptedSecret resource not found. Ignoring since object must be deleted", "Name", encryptedSecret.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch PushEncryptedSecret resource", "Name", encryptedSecret.Name)
		return ctrl.Result{}, err
	}
	logger.Info("Got PushEncryptedSecret resource", "Name", encryptedSecret.Name)

	h, _ := hashstructure.Hash(encryptedSecret.Spec, nil)
	resourceHash := strconv.FormatUint(h, 10)

	// examine DeletionTimestamp to determine if object is under deletion
	if encryptedSecret.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !controllerutil.ContainsFinalizer(encryptedSecret, finalizerName) {
			controllerutil.AddFinalizer(encryptedSecret, finalizerName)
			if err := r.Update(ctx, encryptedSecret); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("Added finalizer to resource", "Name", encryptedSecret.Name)
		}
	} else {
		if controllerutil.ContainsFinalizer(encryptedSecret, finalizerName) {
			// our finalizer is present, so lets handle deletion
			logger.Info("Resource is being deleted", "Name", encryptedSecret.Name)

			keysTodelete := []string{}
			if prefix := parameterStorePrefix(encryptedSecret.Spec); prefix != "" {
				for _, keyToDel := range encryptedSecret.Spec.Data {
					keysTodelete = append(keysTodelete, ssmParameterName(prefix, keyToDel.SecretKey))
				}
			}
			// the managed Kubernetes Secret is owned by this resource and is
			// garbage collected by Kubernetes on deletion; no explicit cleanup needed.

			// deleteSSMKeys already ignores per-key ParameterNotFound errors and
			// attempts every key regardless of earlier failures, so any error
			// returned here is a genuine failure that needs a retry.
			if err := r.deleteSSMKeys(ctx, keysTodelete); err != nil {
				logger.Error(err, "Failed to delete parameters", "Name", encryptedSecret.Name)
				// if fail to delete the external dependency here, return with error
				// so that it can be retried. Returning a non-nil error already causes
				// controller-runtime's workqueue to back off exponentially before the
				// next attempt, without blocking this worker from processing other
				// resources in the meantime.
				if _, err := r.handleErrorStatus(logger, ctx, encryptedSecret, err.Error(), resourceHash, OP_DELETE); err != nil {
					logger.Error(err, "Failed to update resource status on deletion")
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, err
			}

			// remove our finalizer from the list and update it.
			if controllerutil.RemoveFinalizer(encryptedSecret, finalizerName) {
				if err := r.Update(ctx, encryptedSecret); err != nil {
					logger.Error(err, "Failed to update resource on finalizer removal", "Name", encryptedSecret.Name)
					return ctrl.Result{}, err
				}
			} else {
				err := errors.New("unable to remove finalizer")
				logger.Error(err, "Name", encryptedSecret.Name)
				return ctrl.Result{}, err
			}
			logger.Info("Resource successfully deleted", "Name", encryptedSecret.Name)
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// Skip reconciliation if object's "status.hash" is unchanged and the owned Secret hasn't drifted
	if r.changesDetected(ctx, encryptedSecret) {
		logger.Info("Syncing secrets of resource", "Name", encryptedSecret.Name)

		secrets, err := r.decryptSecrets(ctx, encryptedSecret.Spec)
		var secretResourceVersion string
		if err == nil {
			secretResourceVersion, err = r.applyKubernetesSecret(ctx, encryptedSecret, secrets)
		}
		if err == nil {
			err = r.putSSMKeys(ctx, encryptedSecret.Spec, secrets)
		}
		if err != nil {
			if _, err := r.handleErrorStatus(logger, ctx, encryptedSecret, err.Error(), resourceHash, OP_UPDATE); err != nil {
				logger.Error(err, "Failed to update error status")
				return ctrl.Result{}, err
			}
			logger.Error(err, "Failed to sync secrets", "Name", encryptedSecret.Name)
			// Errors will be related to only AWS. Returning a non-nil error here
			// already causes controller-runtime's workqueue to back off
			// exponentially before retrying this item - no manual sleep needed,
			// and sleeping here would block this worker from processing every
			// other resource in the meantime.
			return ctrl.Result{}, err
		}

		logger.Info("Secret sync complete", "Name", encryptedSecret.Name)
		return r.handleSuccessStatus(logger, ctx, encryptedSecret, resourceHash, secretResourceVersion)
	}

	// Resources last synced before lastSyncTime existed won't sync again until
	// their spec changes; for a SUCCESS status, lastUpdate is that sync time.
	if encryptedSecret.Status.Status == "SUCCESS" && encryptedSecret.Status.LastSyncTime.IsZero() {
		if err := r.updateStatusWithRetry(ctx, encryptedSecret, func(obj *encryptoriov1beta1.PushEncryptedSecret) {
			obj.Status.LastSyncTime = obj.Status.LastUpdate
		}); err != nil {
			logger.Error(err, "Failed to backfill lastSyncTime", "Name", encryptedSecret.Name)
		}
	}

	logger.Info("Reconcile complete. No changes", "Name", encryptedSecret.Name)
	return requeueWithTimeout(reconcileIntervalSeconds)
}

// During reconcile check if there's any changes made to the object and Skip Reconcile if no changes required
func (r *PushEncryptedSecretReconciler) changesDetected(ctx context.Context, obj *encryptoriov1beta1.PushEncryptedSecret) bool {
	oldHash := obj.Status.Hash
	newHash, _ := hashstructure.Hash(obj.Spec, nil)
	newHashStr := strconv.FormatUint(newHash, 10)

	if oldHash == "" || newHashStr != oldHash {
		return true // new resource, or spec changed since the last attempt
	}

	if obj.Status.Status == "ERROR" {
		// Same spec as an attempt that already failed, so decrypting again would
		// almost certainly fail the same way. Retry only after a cooldown in case
		// the failure was transient. This must return before ownedSecretDrifted:
		// a never-synced resource has no Secret, which would read as drift and
		// force a decrypt on every reconcile.
		return time.Since(obj.Status.LastUpdate.Time) >= failedSyncRetryInterval
	}

	// spec is unchanged and the last sync succeeded, but the owned Secret could
	// have been modified by hand since (a key deleted or its value edited).
	// Check it directly so it gets restored.
	return r.ownedSecretDrifted(ctx, obj)
}

// ownedSecretDrifted reports whether the K8s Secret owned by obj is missing
// entirely, or has been modified (by us or by hand) since our last successful
// write. It compares live resourceVersion against the one we recorded at that
// write - any mismatch means the Secret's actual content is no longer known
// to match the CRD, without needing to inspect (or store a fingerprint of)
// the secret values themselves.
func (r *PushEncryptedSecretReconciler) ownedSecretDrifted(ctx context.Context, obj *encryptoriov1beta1.PushEncryptedSecret) bool {
	secret := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}, secret)
	if err != nil {
		return apierrs.IsNotFound(err)
	}

	return secret.ResourceVersion != obj.Status.SecretResourceVersion
}

// deletionStartedPredicate passes only the update that first sets deletionTimestamp,
// so deletion is picked up without relying on the API server bumping generation.
// Later updates to an object already being deleted (e.g. our own status writes
// after a failed SSM delete) don't pass, leaving retries to the rate limiter.
var deletionStartedPredicate = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectOld.GetDeletionTimestamp() == nil && e.ObjectNew.GetDeletionTimestamp() != nil
	},
}

// SetupWithManager sets up the controller with the Manager.
func (r *PushEncryptedSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&encryptoriov1beta1.PushEncryptedSecret{}, builder.WithPredicates(
			// Only spec changes (generation bumps) or the start of deletion may
			// trigger a reconcile. Status-only updates - including the ones this
			// controller writes itself - must not, or every failed attempt's status
			// write requeues the object immediately and bypasses the rate limiter.
			predicate.Or(predicate.GenerationChangedPredicate{}, deletionStartedPredicate),
			predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					// kubectl delete adds deletionTimeStamp which is an update event.
					// ignore this event and perform reconcile.
					if e.ObjectNew.(*encryptoriov1beta1.PushEncryptedSecret).GetDeletionTimestamp() != nil {
						_, _ = reconcile()
						return true
					}

					// keys will be decrypted and pushed every time on reconcile.
					// hence the hash is added in the status, skip reconcilation if old hash == new hash
					oldObject := e.ObjectOld.(*encryptoriov1beta1.PushEncryptedSecret)
					newObject := e.ObjectNew.(*encryptoriov1beta1.PushEncryptedSecret)

					// TODO: rename the function.
					return r.decideOnUpdate(oldObject, newObject)
				},
				CreateFunc: func(e event.CreateEvent) bool {
					_, _ = reconcile()
					return true
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					_, _ = reconcile()
					return true
				},
			},
		)).
		// Owned Secret's events (edits, key deletions) enqueue the parent so
		// manual drift on the Secret gets caught immediately, not just on the
		// next periodic hash-recheck.
		Owns(&corev1.Secret{}).
		Complete(r)
}

// decryptSecrets decrypts every entry in spec.Data via KMS, keyed by SecretKey.
func (r *PushEncryptedSecretReconciler) decryptSecrets(ctx context.Context, spec encryptoriov1beta1.PushEncryptedSecretSpec) (map[string]string, error) {
	kmsconn := conn.KMSConnection()
	secrets := make(map[string]string, len(spec.Data))

	for _, data := range spec.Data {
		payload, err := base64.StdEncoding.DecodeString(data.EncryptedSecret)
		if err != nil {
			return nil, errors.New(data.SecretKey + "\n" + err.Error())
		}

		decryptInput := &kms.DecryptInput{
			CiphertextBlob:      payload,
			EncryptionAlgorithm: "SYMMETRIC_DEFAULT",
			KeyId:               conn.GetKMSKeyID(spec.KMSKeyID),
		}

		out, err := kmsconn.Decrypt(ctx, decryptInput)
		if err != nil {
			return nil, errors.New(data.SecretKey + "\n" + err.Error())
		}

		secrets[data.SecretKey] = string(out.Plaintext)
	}

	return secrets, nil
}

// applyKubernetesSecret creates or updates the Kubernetes Secret owned by encryptedSecret,
// keyed by SecretKey. It is always kept in sync, regardless of whether ParameterStore is set.
// It returns the resulting Secret's resourceVersion, used to detect later drift.
func (r *PushEncryptedSecretReconciler) applyKubernetesSecret(ctx context.Context, encryptedSecret *encryptoriov1beta1.PushEncryptedSecret, secrets map[string]string) (string, error) {
	data := make(map[string][]byte, len(secrets))
	for key, value := range secrets {
		data[key] = []byte(value)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      encryptedSecret.Name,
			Namespace: encryptedSecret.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		secret.Labels = encryptedSecret.Spec.Labels
		return controllerutil.SetControllerReference(encryptedSecret, secret, r.Scheme)
	})
	if err != nil {
		return "", err
	}
	return secret.ResourceVersion, nil
}

// putSSMKeys syncs secrets to AWS SSM Parameter Store. It is a no-op unless
// spec.ParameterStore is set.
func (r *PushEncryptedSecretReconciler) putSSMKeys(ctx context.Context, spec encryptoriov1beta1.PushEncryptedSecretSpec, secrets map[string]string) error {
	prefix := parameterStorePrefix(spec)
	if prefix == "" {
		return nil
	}

	ssmconn := conn.SSMConnection()

	for _, data := range spec.Data {
		name := ssmParameterName(prefix, data.SecretKey)
		ssmInput := &ssm.PutParameterInput{
			KeyId:     conn.GetKMSKeyID(spec.KMSKeyID),
			Name:      aws.String(name),
			Value:     aws.String(secrets[data.SecretKey]),
			Type:      types.ParameterTypeSecureString,
			Overwrite: aws.Bool(true),
		}
		if _, err := ssmconn.PutParameter(ctx, ssmInput); err != nil {
			return errors.New(name + "\n" + err.Error())
		}
	}

	return nil
}

// parameterStorePrefix returns the configured SSM key path prefix, or "" if
// ParameterStore syncing is disabled for this resource.
func parameterStorePrefix(spec encryptoriov1beta1.PushEncryptedSecretSpec) string {
	if spec.ParameterStore == nil {
		return ""
	}
	return spec.ParameterStore.KeyPathPrefix
}

// ssmParameterName joins a key path prefix and a secretKey into the full SSM parameter name.
func ssmParameterName(prefix, secretKey string) string {
	return strings.TrimSuffix(prefix, "/") + "/" + secretKey
}

// deleteSSMKeys attempts to delete every key in keysToDelete, even if an earlier
// one fails, so one already-missing or bad key can't leave the rest orphaned.
// Per-key ParameterNotFound errors are ignored (already gone is the goal state);
// any other errors are aggregated and returned together.
func (r *PushEncryptedSecretReconciler) deleteSSMKeys(ctx context.Context, keysToDelete []string) error {
	ssmconn := conn.SSMConnection()
	var errs []string
	for _, key := range keysToDelete {
		_, err := ssmconn.DeleteParameter(ctx, &ssm.DeleteParameterInput{
			Name: aws.String(key),
		})
		if err != nil && !strings.Contains(err.Error(), "ParameterNotFound") {
			errs = append(errs, key+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (r *PushEncryptedSecretReconciler) decideOnUpdate(oldObject, newObject *encryptoriov1beta1.PushEncryptedSecret) bool {
	logger := log.Log.WithName("PushEncryptedSecret").WithValues("Namespace", newObject.Namespace)

	ssmKeysToDelete := []string{}
	if oldPrefix := parameterStorePrefix(oldObject.Spec); oldPrefix != "" {
		if newPrefix := parameterStorePrefix(newObject.Spec); newPrefix == oldPrefix {
			// parameterStore still enabled with the same prefix: only remove
			// SecretKeys that are no longer present in the new spec.
			newObjectKeysMap := map[string]bool{}
			for _, data := range newObject.Spec.Data {
				newObjectKeysMap[data.SecretKey] = true
			}
			for _, data := range oldObject.Spec.Data {
				if !newObjectKeysMap[data.SecretKey] {
					ssmKeysToDelete = append(ssmKeysToDelete, ssmParameterName(oldPrefix, data.SecretKey))
				}
			}
		} else {
			// parameterStore was disabled or its prefix changed: everything
			// previously stored under the old prefix must be removed.
			for _, data := range oldObject.Spec.Data {
				ssmKeysToDelete = append(ssmKeysToDelete, ssmParameterName(oldPrefix, data.SecretKey))
			}
		}
	}

	// compare old and new object changes,
	// delete keys which were removed on latest apply
	h, _ := hashstructure.Hash(newObject.Spec, nil)
	hashStr := strconv.FormatUint(h, 10)

	if len(ssmKeysToDelete) != 0 {
		if err := r.deleteSSMKeys(context.Background(), ssmKeysToDelete); err != nil {
			logger.Error(err, "Error deleting ssm keys on update")
			return true // log & reconcile anyway
		}
		logger.Info("Deleted missing SSM parameters on resource update", "Name", newObject.Name, "Keys Under deletion", ssmKeysToDelete)
	}

	// prevent immediate reconcilation of same resource after its creation
	if oldObject.Status.Hash == "" && newObject.Status.Hash != "" {
		return false
	}

	// reconcile immediately if any new key is added or updated
	if newObject.Status.Hash != "" && newObject.Status.Hash != hashStr {
		_, _ = reconcile()
		return true
	}

	// Anything else reaching here is a status-only echo of a write this
	// controller just made itself (success or error), with an unchanged spec.
	// Reconciling again immediately is never correct for either case: on
	// success there's nothing left to do (the periodic recheck in Reconcile
	// already covers future drift), and on error it would race ahead of - and
	// defeat - controller-runtime's exponential backoff on the returned error,
	// since an immediate requeue from this predicate is not rate-limited the
	// way a requeue from a Reconcile error is.
	return false
}
