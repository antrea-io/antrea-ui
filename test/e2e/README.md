# E2E Tests

The tests need to run on a K8s cluster with both Antrea and Antrea UI installed.

To run the tests:

```bash
go test -v ./test/e2e
```

## OIDC

`TestOIDC` logs in through a real identity provider and then makes an authenticated Kubernetes API
call with the resulting session. It skips unless the deployment under test has OIDC enabled.

Antrea UI presents the OIDC id_token to the kube-apiserver as the end user's own credential, so the
apiserver has to trust the same issuer. That means the provider cannot live inside the Pod, and the
cluster has to be created with matching `--oidc-*` flags. `ci/e2e-oidc.sh` sets this up with Dex
running as a container on the Kind network; see the comments at the top of that script for the
topology and why it is arranged that way.

Full local run, from a clean machine:

```bash
# 1. Dex first: it writes the CA that the Kind config mounts for the apiserver.
ci/e2e-oidc.sh start-dex

# 2. Cluster, with the apiserver configured to trust Dex.
kind create cluster --config ci/kind-config.yml

# 3. Antrea, for the CNI (the Kind config disables the default one).
helm repo add antrea https://charts.antrea.io && helm repo update
helm install --namespace kube-system antrea antrea/antrea
kubectl rollout status -n kube-system ds/antrea-agent --timeout=5m

# 4. Publish the Dex CA for the backend and grant the test identity its RBAC.
ci/e2e-oidc.sh configure-cluster

# 5. Antrea UI itself.
make
kind load docker-image antrea/antrea-ui-frontend:latest antrea/antrea-ui-backend:latest
# hostAliases points the backend at Dex; its address is only known once the container is running.
helm install --namespace kube-system antrea-ui \
  -f ci/antrea-ui-values.yml \
  --set hostAliases[0].ip="$(ci/e2e-oidc.sh dex-ip)" \
  --set hostAliases[0].hostnames[0]=dex.e2e \
  ./build/charts/antrea-ui
kubectl rollout status -n kube-system deployment/antrea-ui --timeout=5m

go test -v ./test/e2e -run TestOIDC
```

Tear down with `kind delete cluster && ci/e2e-oidc.sh stop`.

`TestPluginLoading` additionally needs the pod-counter example plugin built and installed as a
labeled ConfigMap — see the corresponding steps in `.github/workflows/kind_e2e.yml`.
