// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/kube"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	"helm.sh/helm/v4/pkg/strvals"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"antrea.io/antrea-ui/test/benchmark/flowvisibility/fakefa"
)

const (
	// The fake Flow Aggregator is deployed where the real one would be, under the names the
	// antrea-ui chart defaults to, so the chart is installed with flowAggregator.enabled=true and
	// nothing else.
	faNamespace   = "flow-aggregator"
	faName        = "flow-aggregator"
	faCAConfigMap = "flow-aggregator-ca"
	// faRole is the Role the real Flow Aggregator chart creates, and the antrea-ui chart binds its
	// ServiceAccount to so that it can read faCAConfigMap.
	faRole = "flow-aggregator-exporter-role"
	// #nosec G101: not credentials, the name of the Secret holding the fake server's certificate
	faTLSSecret   = "fakefa-tls"
	faScaleConfig = "fakefa-scale"
	faGRPCPort    = 14740
	faStatsPort   = 8080
	// flowStreamServiceLabel is set on the Flow Aggregator Service when its FlowStreamService is
	// enabled (FlowStreamServiceLabelKey in Antrea's pkg/apis), for clients that discover it with a
	// label selector. antrea-ui finds it by name instead.
	flowStreamServiceLabel = "observability.antrea.io/flow-stream-service"

	benchSA  = "flowbench"
	benchCRB = "flowbench-cluster-admin"

	// syntheticLabel marks the Namespaces flowbench creates for the synthetic cluster, so that a
	// smaller scale, or teardown, can delete them again.
	syntheticLabel = "flowbench.antrea.io/synthetic"
	partOfLabel    = "app.kubernetes.io/part-of"
	partOfValue    = "antrea-ui-flowbench"
)

type deployOptions struct {
	cluster
	repoRoot    string
	fakeFAImage string
	uiImageTag  string
	uiNamespace string
	uiRelease   string
	nodePort    int
	helmSets    []string
	scale       fakefa.Scale
}

func deploy(ctx context.Context, o *deployOptions) error {
	cs := o.clientset
	scaleJSON, err := json.MarshalIndent(o.scale, "", "  ")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(scaleJSON)
	scaleHash := hex.EncodeToString(sum[:8])
	uiVals, err := uiValues(o, scaleHash)
	if err != nil {
		return err
	}
	helmCfg, err := o.helmConfig(o.uiNamespace)
	if err != nil {
		return err
	}
	exists, owned, err := uiReleaseOwnership(helmCfg, o.uiRelease)
	if err != nil {
		return err
	}
	if exists && !owned {
		return fmt.Errorf("helm release %s/%s exists and flowbench did not install it: flowbench would replace it, "+
			"use a cluster without antrea-ui", o.uiNamespace, o.uiRelease)
	}
	if err := ensureFakeFANamespace(ctx, cs); err != nil {
		return err
	}
	caPEM, err := ensureFakeFATLS(ctx, cs)
	if err != nil {
		return err
	}
	if err := apply(ctx, cs.CoreV1().ConfigMaps(faNamespace), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: faCAConfigMap, Labels: map[string]string{partOfLabel: partOfValue}},
		Data:       map[string]string{"ca.crt": string(caPEM)},
	}); err != nil {
		return err
	}
	if err := apply(ctx, cs.RbacV1().Roles(faNamespace), &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: faRole, Labels: map[string]string{partOfLabel: partOfValue}},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{faCAConfigMap}, Verbs: []string{"get"},
		}},
	}); err != nil {
		return err
	}
	if err := apply(ctx, cs.CoreV1().ConfigMaps(faNamespace), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: faScaleConfig, Labels: map[string]string{partOfLabel: partOfValue}},
		Data:       map[string]string{"scale.json": string(scaleJSON)},
	}); err != nil {
		return err
	}
	if err := apply(ctx, cs.CoreV1().Services(faNamespace), fakeFAService()); err != nil {
		return err
	}
	if err := apply(ctx, cs.AppsV1().Deployments(faNamespace), fakeFADeployment(o.fakeFAImage, scaleHash)); err != nil {
		return err
	}
	if err := syncSyntheticNamespaces(ctx, cs, o.scale.Namespaces); err != nil {
		return err
	}
	if err := ensureBenchIdentity(ctx, cs); err != nil {
		return err
	}
	logf("Waiting for the fake Flow Aggregator to be ready")
	if err := waitForDeployment(ctx, cs, faNamespace, faName); err != nil {
		return err
	}
	if err := helmInstallUI(ctx, o, helmCfg, exists, uiVals); err != nil {
		return err
	}
	logf("Waiting for antrea-ui to be ready")
	return waitForDeployment(ctx, cs, o.uiNamespace, "antrea-ui")
}

func teardown(ctx context.Context, c *cluster, uiNamespace, uiRelease string) error {
	cs := c.clientset
	helmCfg, err := c.helmConfig(uiNamespace)
	if err != nil {
		return err
	}
	exists, owned, err := uiReleaseOwnership(helmCfg, uiRelease)
	switch {
	case err != nil:
		return err
	case !exists:
	case !owned:
		logf("Keeping Helm release %s/%s: flowbench did not install it", uiNamespace, uiRelease)
	default:
		logf("Uninstalling Helm release %s/%s", uiNamespace, uiRelease)
		uninstall := action.NewUninstall(helmCfg)
		uninstall.IgnoreNotFound = true
		uninstall.WaitStrategy = kube.HookOnlyStrategy
		uninstall.Timeout = 5 * time.Minute
		if _, err := uninstall.Run(uiRelease); err != nil {
			return fmt.Errorf("failed to uninstall Helm release %s/%s: %w", uiNamespace, uiRelease, err)
		}
	}
	if err := syncSyntheticNamespaces(ctx, cs, 0); err != nil {
		return err
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, faNamespace, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case ns.Labels[partOfLabel] != partOfValue:
		logf("Keeping Namespace %s: flowbench did not create it", faNamespace)
	default:
		logf("Deleting Namespace %s", faNamespace)
		if err := cs.CoreV1().Namespaces().Delete(ctx, faNamespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, benchCRB, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := cs.CoreV1().ServiceAccounts(metav1.NamespaceSystem).Delete(ctx, benchSA, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// uiReleaseOwnership reports whether the Helm release exists, and whether flowbench installed it:
// helmInstallUI labels the release with partOfLabel. An uninstalled release whose history was
// kept does not exist: it has no objects left, and installing over it replaces it.
func uiReleaseOwnership(helmCfg *action.Configuration, name string) (exists, owned bool, err error) {
	r, err := action.NewGet(helmCfg).Run(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("failed to get Helm release %s: %w", name, err)
	}
	var rel *releasev1.Release
	switch r := r.(type) {
	case *releasev1.Release:
		rel = r
	case releasev1.Release:
		rel = &r
	default:
		return false, false, fmt.Errorf("unsupported Helm release type %T", r)
	}
	if rel.Info != nil && rel.Info.Status == releasecommon.StatusUninstalled {
		return false, false, nil
	}
	return true, rel.Labels[partOfLabel] == partOfValue, nil
}

// ensureFakeFANamespace creates the Namespace the fake Flow Aggregator runs in. It refuses one that
// flowbench did not create: deploying there would replace a real Flow Aggregator.
func ensureFakeFANamespace(ctx context.Context, cs kubernetes.Interface) error {
	ns, err := cs.CoreV1().Namespaces().Get(ctx, faNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ensureNamespace(ctx, cs, faNamespace, map[string]string{partOfLabel: partOfValue})
	}
	if err != nil {
		return err
	}
	if ns.Labels[partOfLabel] != partOfValue {
		return fmt.Errorf("namespace %s exists and flowbench did not create it: is a Flow Aggregator installed? "+
			"flowbench would replace it, use a cluster without one", faNamespace)
	}
	return nil
}

func ensureNamespace(ctx context.Context, cs kubernetes.Interface, name string, labels map[string]string) error {
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Namespace %s: %w", name, err)
	}
	return nil
}

// syncSyntheticNamespaces makes the cluster's synthetic Namespaces match the scale: the UI's
// Namespace pickers list the cluster's Namespaces, so they have to exist for the synthetic flows'
// Namespaces to be offered.
func syncSyntheticNamespaces(ctx context.Context, cs kubernetes.Interface, count int) error {
	want := map[string]bool{}
	for i := range count {
		want[fakefa.NamespaceName(i)] = true
	}
	existing, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: syntheticLabel + "=true"})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	deleted := 0
	for _, ns := range existing.Items {
		if want[ns.Name] {
			have[ns.Name] = true
			continue
		}
		if err := cs.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		deleted++
	}
	created := 0
	for name := range want {
		if have[name] {
			continue
		}
		if err := ensureNamespace(ctx, cs, name, map[string]string{syntheticLabel: "true", partOfLabel: partOfValue}); err != nil {
			return err
		}
		created++
	}
	if created > 0 || deleted > 0 {
		logf("Synthetic Namespaces: %d created, %d deleted", created, deleted)
	}
	return nil
}

// ensureBenchIdentity creates the ServiceAccount the benchmark logs in as: a cluster admin, so the
// UI defaults to All Namespaces, and so it can read the backend's /metrics.
func ensureBenchIdentity(ctx context.Context, cs kubernetes.Interface) error {
	if err := apply(ctx, cs.CoreV1().ServiceAccounts(metav1.NamespaceSystem), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: benchSA, Labels: map[string]string{partOfLabel: partOfValue}},
	}); err != nil {
		return err
	}
	return apply(ctx, cs.RbacV1().ClusterRoleBindings(), &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: benchCRB, Labels: map[string]string{partOfLabel: partOfValue}},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: benchSA, Namespace: metav1.NamespaceSystem}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
	})
}

func benchToken(ctx context.Context, cs kubernetes.Interface) (string, error) {
	tr, err := cs.CoreV1().ServiceAccounts(metav1.NamespaceSystem).CreateToken(ctx, benchSA, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](24 * 3600)},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to mint a token for %s: %w", benchSA, err)
	}
	return tr.Status.Token, nil
}

// ensureFakeFATLS returns the fake Flow Aggregator's CA, generating it and the server certificate
// on first use. They are kept in a Secret, so that redeploying with another scale keeps the CA the
// antrea-ui backend read at startup.
func ensureFakeFATLS(ctx context.Context, cs kubernetes.Interface) ([]byte, error) {
	secret, err := cs.CoreV1().Secrets(faNamespace).Get(ctx, faTLSSecret, metav1.GetOptions{})
	if err == nil {
		return secret.Data["ca.crt"], nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	caPEM, certPEM, keyPEM, err := generateCerts()
	if err != nil {
		return nil, err
	}
	_, err = cs.CoreV1().Secrets(faNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: faTLSSecret, Labels: map[string]string{partOfLabel: partOfValue}},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": caPEM, corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
	}, metav1.CreateOptions{})
	return caPEM, err
}

func generateCerts() (caPEM, certPEM, keyPEM []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "flowbench-fake-flow-aggregator-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: faName},
		DNSNames: []string{
			faName, faName + "." + faNamespace, faName + "." + faNamespace + ".svc",
			faName + "." + faNamespace + ".svc.cluster.local",
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func fakeFAService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: faName, Labels: map[string]string{
			partOfLabel:            partOfValue,
			flowStreamServiceLabel: "true",
		}},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": faName},
			Ports: []corev1.ServicePort{
				{Name: "grpc", Port: faGRPCPort, TargetPort: intstr.FromInt32(faGRPCPort)},
				{Name: "stats", Port: faStatsPort, TargetPort: intstr.FromInt32(faStatsPort)},
			},
		},
	}
}

// fakeFADeployment runs the fake Flow Aggregator. scaleHash goes into the Pod template, so that
// a new scale rolls the Pod: the generator reads its scale once, at startup.
func fakeFADeployment(image, scaleHash string) *appsv1.Deployment {
	labels := map[string]string{"app": faName}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: faName, Labels: map[string]string{partOfLabel: partOfValue}},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{"flowbench.antrea.io/scale-hash": scaleHash},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "fakefa",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args: []string{
							"--scale=/etc/fakefa/scale/scale.json",
							"--tls-cert=/etc/fakefa/tls/tls.crt",
							"--tls-key=/etc/fakefa/tls/tls.key",
							fmt.Sprintf("--listen=:%d", faGRPCPort),
							fmt.Sprintf("--stats-listen=:%d", faStatsPort),
						},
						Ports: []corev1.ContainerPort{{Name: "grpc", ContainerPort: faGRPCPort}, {Name: "stats", ContainerPort: faStatsPort}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/stats", Port: intstr.FromInt32(faStatsPort)}},
							PeriodSeconds: 2,
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "scale", MountPath: "/etc/fakefa/scale", ReadOnly: true},
							{Name: "tls", MountPath: "/etc/fakefa/tls", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "scale", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: faScaleConfig},
						}}},
						{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: faTLSSecret}}},
					},
				},
			},
		},
	}
}

// uiValues returns the values antrea-ui is installed with: flowbench's own, then the --helm-set ones
// over them. scaleHash goes into the Pod template, so that each scale of a run is measured with a
// fresh backend rather than one that served the previous scale.
func uiValues(o *deployOptions, scaleHash string) (map[string]any, error) {
	vals := map[string]any{
		"podAnnotations": map[string]any{"flowbench.antrea.io/scale-hash": scaleHash},
		"flowAggregator": map[string]any{"enabled": true},
		"frontend":       map[string]any{"image": map[string]any{"tag": o.uiImageTag}},
		"backend":        map[string]any{"image": map[string]any{"tag": o.uiImageTag}},
		"service":        map[string]any{"type": "NodePort", "nodePort": o.nodePort},
	}
	for _, s := range o.helmSets {
		if err := strvals.ParseInto(s, vals); err != nil {
			return nil, fmt.Errorf("--helm-set %q: %w", s, err)
		}
	}
	return vals, nil
}

// helmInstallUI installs antrea-ui from the repository's chart, or upgrades the release if it
// exists, and waits for its objects to be ready.
func helmInstallUI(ctx context.Context, o *deployOptions, helmCfg *action.Configuration, exists bool, vals map[string]any) error {
	chart, err := loader.Load(filepath.Join(o.repoRoot, "build/charts/antrea-ui"))
	if err != nil {
		return fmt.Errorf("failed to load the antrea-ui chart: %w", err)
	}
	labels := map[string]string{partOfLabel: partOfValue}
	if exists {
		logf("Upgrading Helm release %s/%s", o.uiNamespace, o.uiRelease)
		upgrade := action.NewUpgrade(helmCfg)
		upgrade.Namespace = o.uiNamespace
		upgrade.Labels = labels
		// The helm CLI's default: each deploy, and each scale of a run, adds a revision.
		upgrade.MaxHistory = 10
		upgrade.WaitStrategy = kube.StatusWatcherStrategy
		upgrade.Timeout = 5 * time.Minute
		_, err = upgrade.RunWithContext(ctx, o.uiRelease, chart, vals)
	} else {
		logf("Installing Helm release %s/%s", o.uiNamespace, o.uiRelease)
		install := action.NewInstall(helmCfg)
		install.ReleaseName = o.uiRelease
		install.Namespace = o.uiNamespace
		install.Labels = labels
		// Reuses the name of an uninstalled release whose history was kept, like helm upgrade
		// --install does.
		install.Replace = true
		install.WaitStrategy = kube.StatusWatcherStrategy
		install.Timeout = 5 * time.Minute
		_, err = install.RunWithContext(ctx, chart, vals)
	}
	if err != nil {
		return fmt.Errorf("failed to install Helm release %s/%s: %w", o.uiNamespace, o.uiRelease, err)
	}
	return nil
}

func waitForDeployment(ctx context.Context, cs kubernetes.Interface, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		d, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return d.Status.ObservedGeneration >= d.Generation &&
			d.Status.UpdatedReplicas == *d.Spec.Replicas &&
			d.Status.AvailableReplicas == *d.Spec.Replicas &&
			d.Status.Replicas == *d.Spec.Replicas, nil
	})
}
