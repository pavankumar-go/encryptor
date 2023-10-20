package controller

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go/aws/awserr"
	encryptoriov1beta1 "github.com/encryptor/api/v1beta1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *PushEncryptedSecretReconciler) handleErrorStatus(logger logr.Logger, ctx context.Context, encryptedSecret *encryptoriov1beta1.PushEncryptedSecret, err string, hash string, operation string) (ctrl.Result, error) {
	encryptedSecret.Status = encryptoriov1beta1.PushEncryptedSecretStatus{
		Status:           "ERROR",
		Reason:           err,
		Hash:             hash,
		LastUpdate:       metav1.Now(),
		ErrorOnOperation: operation,
	}
	if err := r.Status().Update(ctx, encryptedSecret); err != nil {
		logger.Error(err, "failed to update encryptedSecret status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *PushEncryptedSecretReconciler) handleSuccessStatus(logger logr.Logger, ctx context.Context, encryptedSecret *encryptoriov1beta1.PushEncryptedSecret, hash string) (ctrl.Result, error) {
	encryptedSecret.Status = encryptoriov1beta1.PushEncryptedSecretStatus{
		Status:     "SUCCESS",
		Reason:     "",
		Hash:       hash,
		LastUpdate: metav1.Now(),
	}
	if err := r.Status().Update(ctx, encryptedSecret); err != nil {
		logger.Error(err, "failed to update encryptedSecret status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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
