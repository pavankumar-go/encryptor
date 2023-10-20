package conn

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

func SSMConnection() *ssm.Client {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatal(err)
	}
	return ssm.NewFromConfig(cfg)
}

func KMSConnection() *kms.Client {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatal(err)
	}
	return kms.NewFromConfig(cfg)
}

func GetKMSKeyID() *string {
	kmsKeyID := os.Getenv("KMS_KEY_ID")
	if len(kmsKeyID) == 0 {
		log.Fatal("KMS_KEY_ID unset")
	}
	return &kmsKeyID
}
