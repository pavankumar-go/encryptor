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
	"time"

	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	encryptoriov1beta1 "github.com/encryptor/api/v1beta1"
	"github.com/encryptor/internal/conn"
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
)

//+kubebuilder:rbac:groups=encryptor.dev,resources=pushencryptedsecrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=encryptor.dev,resources=pushencryptedsecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=encryptor.dev,resources=pushencryptedsecrets/finalizers,verbs=update

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

	hash, _ := hashstructure.Hash(encryptedSecret.Spec.Data, nil)
	resourceHash := strconv.FormatUint(hash, 10)

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
			for _, keyToDel := range encryptedSecret.Spec.Data {
				keysTodelete = append(keysTodelete, keyToDel.RemoteRefKey)
			}

			// Deletion was already in progress
			// slow down to avoid request throttling to AWS
			if encryptedSecret.Status.ErrorOnOperation == OP_DELETE {
				time.Sleep(60 * time.Second)
			}

			if err := r.deleteSSMKeys(ctx, keysTodelete); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				if _, err := r.handleErrorStatus(logger, ctx, encryptedSecret, err.Error(), resourceHash, OP_DELETE); err != nil {
					logger.Error(err, "Failed to update error status")
					return ctrl.Result{}, err
				}
				logger.Error(err, "Failed to delete parameters", "Name", encryptedSecret.Name)
				return ctrl.Result{}, err
			}

			// remove our finalizer from the list and update it.
			if controllerutil.RemoveFinalizer(encryptedSecret, finalizerName) {
				if err := r.Update(ctx, encryptedSecret); err != nil {
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

	// Skip reconciliation if object's "status.hash" is unchanged
	if r.changesDetected(encryptedSecret) {
		logger.Info("Starting create or update on resource", "Name", encryptedSecret.Name)
		err := r.createOrUpdateSSM(ctx, encryptedSecret.Spec.Data)
		if err != nil {
			if _, err := r.handleErrorStatus(logger, ctx, encryptedSecret, err.Error(), resourceHash, OP_UPDATE); err != nil {
				logger.Error(err, "Failed to update error status")
				return ctrl.Result{}, err
			}
			logger.Error(err, "Failed to create or update parameters", "Name", encryptedSecret.Name)
			// Errors will be related to only AWS
			// returning with error will cause immediate requeue and too many requests made to AWS
			// only way to slow down the queueing & avoid getting request throlling on AWS and also return error to reconciler is to sleep the execution
			// till i find a better approach
			time.Sleep(60 * time.Second)
			return ctrl.Result{}, err
		}

		logger.Info("Create Or Update Complete", "Name", encryptedSecret.Name)
		return r.handleSuccessStatus(logger, ctx, encryptedSecret, resourceHash)
	}

	logger.Info("Reconcile complete. No changes", "Name", encryptedSecret.Name)
	return requeueWithTimeout(60)
}

// During reconcile check if there's any changes made to the object and Skip Reconcile if no changes required
func (r *PushEncryptedSecretReconciler) changesDetected(obj *encryptoriov1beta1.PushEncryptedSecret) bool {
	oldHash := obj.Status.Hash
	newHash, _ := hashstructure.Hash(obj.Spec.Data, nil)
	newHashStr := strconv.FormatUint(newHash, 10)

	if oldHash == "" {
		return true // empty hash means new resource
	}

	// reconcile if hash dont match OR if previous create/update as failed
	if newHashStr == oldHash && obj.Status.Status == "ERROR" {
		return true
	}

	if newHashStr != oldHash && obj.Status.Status == "ERROR" {
		return true
	}

	if newHashStr != oldHash {
		return true
	}

	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *PushEncryptedSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&encryptoriov1beta1.PushEncryptedSecret{}).
		WithEventFilter(
			predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldObject := e.ObjectOld.(*encryptoriov1beta1.PushEncryptedSecret)
					newObject := e.ObjectNew.(*encryptoriov1beta1.PushEncryptedSecret)
					return r.compareAndDeleteKeysOnUpdate(oldObject, newObject)
				},
				CreateFunc: func(e event.CreateEvent) bool {
					_, _ = reconcile()
					return true
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					_, _ = reconcile()
					return true
				},
			}).
		Complete(r)
}

func (r *PushEncryptedSecretReconciler) createOrUpdateSSM(ctx context.Context, secretdata []encryptoriov1beta1.PushEncryptedSecretData) error {
	ssmconn := conn.SSMConnection()
	kmsconn := conn.KMSConnection()
	sectype := "SecureString"

	for _, data := range secretdata {
		payload, err := base64.StdEncoding.DecodeString(data.EncryptedSecret)
		if err != nil {
			return err
		}

		decryptInput := &kms.DecryptInput{
			CiphertextBlob:      []byte(payload),
			EncryptionAlgorithm: "SYMMETRIC_DEFAULT",
			KeyId:               conn.GetKMSKeyID(),
		}

		out, err := kmsconn.Decrypt(ctx, decryptInput)
		if err != nil {
			return err
		}

		ssmInput := &ssm.PutParameterInput{
			KeyId:     conn.GetKMSKeyID(),
			Name:      aws.String(data.RemoteRefKey),
			Value:     aws.String(string(out.Plaintext)),
			Type:      types.ParameterType(sectype),
			Overwrite: aws.Bool(true),
		}
		_, err = ssmconn.PutParameter(ctx, ssmInput)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *PushEncryptedSecretReconciler) deleteSSMKeys(ctx context.Context, keysToDelete []string) error {
	ssmconn := conn.SSMConnection()
	for _, key := range keysToDelete {
		_, err := ssmconn.DeleteParameter(ctx, &ssm.DeleteParameterInput{
			Name: aws.String(key),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *PushEncryptedSecretReconciler) compareAndDeleteKeysOnUpdate(oldObject, newObject *encryptoriov1beta1.PushEncryptedSecret) bool {
	logger := log.Log.WithName("PushEncryptedSecret").WithValues("Namespace", newObject.Namespace)
	ssmKeysToDelete := []string{}
	updatedKeysMap := map[string]bool{}

	for _, new := range newObject.Spec.Data {
		updatedKeysMap[new.RemoteRefKey] = true
	}

	for _, old := range oldObject.Spec.Data {
		if !updatedKeysMap[old.RemoteRefKey] {
			ssmKeysToDelete = append(ssmKeysToDelete, old.RemoteRefKey)
		}
	}

	// compare old and new object changes,
	// delete keys which were removed on latest apply
	if len(ssmKeysToDelete) != 0 {
		if err := r.deleteSSMKeys(context.Background(), ssmKeysToDelete); err != nil {
			logger.Error(err, "Error deleting missing ssm keys on update")
			return true // log & reconcile anyway
		}
		logger.Info("Deleted missing SSM parameters on resource update", "Name", newObject.Name, "Keys Under deletion", ssmKeysToDelete)
		newHash, _ := hashstructure.Hash(newObject.Spec.Data, nil)
		newHashStr := strconv.FormatUint(newHash, 10)
		r.handleSuccessStatus(logger, context.Background(), newObject, newHashStr)
	}

	return oldObject.Status != newObject.Status
}
