# encryptor
// TODO(user): Add simple overview of use/purpose

## Description
// TODO(user): An in-depth paragraph about your project and overview of use

## Build and Deploy 
1. Install CRD
```sh
make install
```

2. Build and deploy manager
```sh
export TAG=<>
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o manager -mod=vendor cmd/main.go && docker buildx build --platform linux/amd64 -t $TAG -f Dockerfile . --push
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

