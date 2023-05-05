# E2E Tests for Helm chart

These tests are meant to validate the antrea-ui Helm chart for different
configurations (i.e., by specifying different sets of Helm values).

In addition to a valid Golang installation, you will need to [install
Helm](https://helm.sh/docs/intro/install/) in order to run these tests.

The tests need to run on a K8s cluster with Antrea installed, but without Antrea
UI.

To run the tests:

```bash
go test -v ./test/e2e-helm
```
