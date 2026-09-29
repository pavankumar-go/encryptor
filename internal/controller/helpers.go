package controller

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	encryptoriov1beta1 "github.com/encrypted-secrets/api/v1beta1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateStatusWithRetry applies mutate to a freshly-fetched copy of encryptedSecret
// and writes it back, retrying on write conflicts (another actor - e.g. ArgoCD's
// self-heal re-applying the spec, or a manual edit - updated the object between
// our earlier Get and this write). Retrying against a fresh copy resolves this
// correctly; it is not the AWS-throttling case that warranted slowing down.
func (r *PushEncryptedSecretReconciler) updateStatusWithRetry(ctx context.Context, encryptedSecret *encryptoriov1beta1.PushEncryptedSecret, mutate func(*encryptoriov1beta1.PushEncryptedSecret)) error {
	key := client.ObjectKeyFromObject(encryptedSecret)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, key, encryptedSecret); err != nil {
			return err
		}
		mutate(encryptedSecret)
		return r.Status().Update(ctx, encryptedSecret)
	})
}

// awsRequestIDPattern matches the per-call request ID that AWS SDK v2 embeds in
// error messages, which would otherwise make every repeat of the same failure
// look like a new error.
var awsRequestIDPattern = regexp.MustCompile(`RequestID: [^,\s]*`)

func sameError(a, b string) bool {
	return awsRequestIDPattern.ReplaceAllString(a, "") == awsRequestIDPattern.ReplaceAllString(b, "")
}

func (r *PushEncryptedSecretReconciler) handleErrorStatus(logger logr.Logger, ctx context.Context, encryptedSecret *encryptoriov1beta1.PushEncryptedSecret, err string, hash string, operation string) (ctrl.Result, error) {
	// Skip the write when this failure repeats the one already recorded. The
	// exception is once failedSyncRetryInterval has passed: then LastUpdate is
	// refreshed, which restarts the retry cooldown in changesDetected.
	current := encryptedSecret.Status
	if current.Status == "ERROR" && current.Hash == hash && current.ErrorOnOperation == operation &&
		sameError(current.Reason, err) && time.Since(current.LastUpdate.Time) < failedSyncRetryInterval {
		return ctrl.Result{}, nil
	}

	updateErr := r.updateStatusWithRetry(ctx, encryptedSecret, func(obj *encryptoriov1beta1.PushEncryptedSecret) {
		obj.Status = encryptoriov1beta1.PushEncryptedSecretStatus{
			Status:                "ERROR",
			Reason:                err,
			Hash:                  hash,
			LastUpdate:            metav1.Now(),
			ErrorOnOperation:      operation,
			SecretResourceVersion: obj.Status.SecretResourceVersion,
			LastSyncTime:          obj.Status.LastSyncTime,
		}
	})
	if updateErr != nil {
		logger.Error(updateErr, "failed to update encryptedSecret status")
		return ctrl.Result{}, updateErr
	}
	return ctrl.Result{}, nil
}

func (r *PushEncryptedSecretReconciler) handleSuccessStatus(logger logr.Logger, ctx context.Context, encryptedSecret *encryptoriov1beta1.PushEncryptedSecret, hash string, secretResourceVersion string) (ctrl.Result, error) {
	now := metav1.Now()
	updateErr := r.updateStatusWithRetry(ctx, encryptedSecret, func(obj *encryptoriov1beta1.PushEncryptedSecret) {
		obj.Status = encryptoriov1beta1.PushEncryptedSecretStatus{
			Status:                "SUCCESS",
			Reason:                "",
			Hash:                  hash,
			LastUpdate:            now,
			SecretResourceVersion: secretResourceVersion,
			LastSyncTime:          now,
		}
	})
	if updateErr != nil {
		logger.Error(updateErr, "failed to update encryptedSecret status")
		return ctrl.Result{}, updateErr
	}
	return requeueWithTimeout(reconcileIntervalSeconds)
}

func ErrCodeEquals(err error, codes ...string) bool {
	var awsErr awserr.Error
	if errors.As(err, &awsErr) {
		for _, code := range codes {
			if awsErr.Code() == code {
				return true
			}
		}
	}
	return false
}
