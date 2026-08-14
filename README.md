# encrypted-secrets
Decrypt secrets encrypted with an AWS KMS key (SYMMETRIC_DEFAULT) and sync them to a Kubernetes `Secret`, optionally also pushing them to AWS SSM Parameter Store.

## Description
external-secrets/external-secrets has a CRD which is called `PushSecret` used for pushing secrets from Kubernetes to a list of supported secret stores,
But it relies on Kubernetes kind Secret to retreive the secret values and refers them in its CRD to push the secrets. i.e, it is not independent

Goal of this project is to be able to store the secrets/credentials in encrypted format in git repositories and use CRD PushEncryptedSecret to push those secrets to Parameter store.

See [CHANGELOG.md](CHANGELOG.md) for what's changed between releases.

## Usage

```yaml
apiVersion: encryptor.dev/v1beta1
kind: PushEncryptedSecret
metadata:
  name: pushencryptedsecret-sample
spec:
  # optional: overrides the controller's default KMS_KEY_ID for decrypting this resource's data
  kmsKeyId: ""
  # optional: when set, also syncs every key below to AWS SSM Parameter Store
  # under <keyPathPrefix>/<secretKey>. When omitted, secrets are only synced
  # to the Kubernetes Secret managed by this resource.
  parameterStore:
    keyPathPrefix: /prod/auth-service
  data:
  - secretKey: TEST_PASSWORD
    encryptedSecret: <base64-encoded KMS ciphertext>
```

The controller always creates/maintains a Kubernetes `Secret` named and namespaced like the `PushEncryptedSecret`, keyed by `secretKey`. If that Secret is later modified or has a key deleted by hand, the controller detects the drift and resyncs it automatically.

## Build and Deploy 
1. Install CRD
```sh
make install
```

2. Build and deploy manager
```sh
export TAG=<>
 ocker buildx build --platform linux/amd64 -t $TAG -f Dockerfile . --push
```

4. Setup AWS Access with below actions for using KMS Key ID and Parameter store
```sh
kms:Encrypt
kms:Decrypt
ssm:GetParameter
ssm:PutParameter
ssm:DeleteParameter
```

3. Set `KMS_KEY_ID` and Image for manager deployment
```sh
vi config/manager/manager.yaml
kustomize build . | k apply -f -
```

**NOTE:** If Integrating with ArgoCD - Enable Health Checks for CRD
Check `argocd/custom-healthcheck.yaml` folder
UPDATE the lua scipt whenever you make changes to CRD Status subresource.

**NOTE:** You can also run this in one step by running: `make install run`

### Modifying the API definitions
If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

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

