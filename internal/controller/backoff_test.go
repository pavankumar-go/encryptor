package controller

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/mitchellh/hashstructure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	encryptoriov1beta1 "github.com/encrypted-secrets/api/v1beta1"
)

func TestSameErrorIgnoresAWSRequestID(t *testing.T) {
	a := "GMAIL_IMAP_APP_PASSWORD\noperation error KMS: Decrypt, https response error StatusCode: 400, RequestID: 1f2e-aaaa, InvalidCiphertextException: "
	b := "GMAIL_IMAP_APP_PASSWORD\noperation error KMS: Decrypt, https response error StatusCode: 400, RequestID: 9c8d-bbbb, InvalidCiphertextException: "
	if !sameError(a, b) {
		t.Fatal("errors differing only by RequestID should compare equal")
	}
	c := "GMAIL_IMAP_APP_PASSWORD\noperation error KMS: Decrypt, https response error StatusCode: 400, RequestID: 9c8d-bbbb, ThrottlingException: "
	if sameError(a, c) {
		t.Fatal("errors with different causes should not compare equal")
	}
}

func erroredResource(t *testing.T, lastUpdate time.Time) *encryptoriov1beta1.PushEncryptedSecret {
	t.Helper()
	obj := &encryptoriov1beta1.PushEncryptedSecret{
		Spec: encryptoriov1beta1.PushEncryptedSecretSpec{
			Data: []encryptoriov1beta1.PushEncryptedSecretData{{SecretKey: "K", EncryptedSecret: "bad"}},
		},
	}
	h, err := hashstructure.Hash(obj.Spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj.Status = encryptoriov1beta1.PushEncryptedSecretStatus{
		Status:     "ERROR",
		Hash:       strconv.FormatUint(h, 10),
		LastUpdate: metav1.NewTime(lastUpdate),
	}
	return obj
}

// A nil client is safe here: the ERROR branch must return before
// ownedSecretDrifted, which is the only part that reads from the cluster.
func TestChangesDetectedSkipsDecryptForRecentIdenticalFailure(t *testing.T) {
	r := &PushEncryptedSecretReconciler{}
	if r.changesDetected(context.Background(), erroredResource(t, time.Now())) {
		t.Fatal("unchanged spec that just failed should not be decrypted again")
	}
}

func TestChangesDetectedRetriesFailureAfterCooldown(t *testing.T) {
	r := &PushEncryptedSecretReconciler{}
	if !r.changesDetected(context.Background(), erroredResource(t, time.Now().Add(-failedSyncRetryInterval-time.Minute))) {
		t.Fatal("failure older than the cooldown should be retried")
	}
}

func TestChangesDetectedRetriesFailureWhenSpecChanges(t *testing.T) {
	r := &PushEncryptedSecretReconciler{}
	obj := erroredResource(t, time.Now())
	obj.Spec.Data[0].EncryptedSecret = "fixed"
	if !r.changesDetected(context.Background(), obj) {
		t.Fatal("a spec change should be synced immediately even while in ERROR")
	}
}

func TestDeletionStartedPredicateOnlyPassesTransition(t *testing.T) {
	now := metav1.Now()
	live := &encryptoriov1beta1.PushEncryptedSecret{}
	deleting := &encryptoriov1beta1.PushEncryptedSecret{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &now}}

	if !deletionStartedPredicate.Update(event.UpdateEvent{ObjectOld: live, ObjectNew: deleting}) {
		t.Fatal("the update that sets deletionTimestamp should pass")
	}
	if deletionStartedPredicate.Update(event.UpdateEvent{ObjectOld: deleting, ObjectNew: deleting}) {
		t.Fatal("later updates to an object already being deleted should not pass")
	}
}
