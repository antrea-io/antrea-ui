package readwriter

import (
	"context"
	"encoding/base64"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	k8sSecretGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
)

type K8sSecret struct {
	secretNamespace string
	secretName      string
	k8sClient       dynamic.Interface
}

func (rw *K8sSecret) readSecret(ctx context.Context) (*unstructured.Unstructured, bool, error) {
	secret, err := rw.k8sClient.Resource(k8sSecretGVR).Namespace(rw.secretNamespace).Get(ctx, rw.secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return secret, true, nil
}

func (rw *K8sSecret) Read(ctx context.Context) (bool, []byte, []byte, error) {
	secret, ok, err := rw.readSecret(ctx)
	if !ok {
		return false, nil, nil, nil
	}
	if err != nil {
		return false, nil, nil, fmt.Errorf("error when retrieving K8s secret '%s/%s': %w", rw.secretNamespace, rw.secretName, err)
	}

	readData := func() ([]byte, []byte, error) {
		data, ok, err := unstructured.NestedMap(secret.Object, "data")
		if err != nil {
			// should not be possible
			return nil, nil, err
		}
		if !ok {
			// should not be possible
			return nil, nil, fmt.Errorf("no data in secret")
		}
		hashI, ok := data["hash"]
		if !ok {
			return nil, nil, fmt.Errorf("hash is missing from data")
		}
		saltI, ok := data["salt"]
		if !ok {
			return nil, nil, fmt.Errorf("salt is missing from data")
		}
		// unfortunately, using the dynamic client means that we get this data as strings,
		// and that we have to decode the base64 data ourselves.
		hashS := hashI.(string)
		saltS := saltI.(string)
		hash, err := base64.StdEncoding.DecodeString(hashS)
		if err != nil {
			return nil, nil, fmt.Errorf("error when base64-decoding hash: %w", err)
		}
		salt, err := base64.StdEncoding.DecodeString(saltS)
		if err != nil {
			return nil, nil, fmt.Errorf("error when base64-decoding salt: %w", err)
		}
		return hash, salt, nil
	}

	hash, salt, err := readData()
	if err != nil {
		return false, nil, nil, fmt.Errorf("error when reading data from K8s secret '%s/%s': %w", rw.secretNamespace, rw.secretName, err)
	}

	return true, hash, salt, nil
}

func (rw *K8sSecret) Write(ctx context.Context, hash []byte, salt []byte) error {
	secret, ok, err := rw.readSecret(ctx)
	if err != nil {
		return err
	}
	hashS := base64.StdEncoding.EncodeToString(hash)
	saltS := base64.StdEncoding.EncodeToString(salt)
	data := map[string]interface{}{
		// using hash and salt directly seems to work with the
		// regular client, but not with the fake client (used for
		// unit tests)
		"hash": hashS,
		"salt": saltS,
	}
	if !ok {
		// create
		secret := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": k8sSecretGVR.Group + "/" + k8sSecretGVR.Version,
				"kind":       "Secret",
				"metadata": map[string]interface{}{
					"namespace": rw.secretNamespace,
					"name":      rw.secretName,
				},
				"data": data,
			},
		}
		if _, err := rw.k8sClient.Resource(k8sSecretGVR).Namespace(rw.secretNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error when creating K8s secret '%s/%s': %w", rw.secretNamespace, rw.secretName, err)
		}
		return nil
	}
	// update
	secret.Object["data"] = data
	if _, err := rw.k8sClient.Resource(k8sSecretGVR).Namespace(rw.secretNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		// we do not handle update conflicts, as we should be the only writer
		return fmt.Errorf("error when updating K8s secret '%s/%s': %w", rw.secretNamespace, rw.secretName, err)
	}
	return nil
}

func NewK8sSecret(secretNamespace string, secretName string, k8sClient dynamic.Interface) *K8sSecret {
	return &K8sSecret{
		secretNamespace: secretNamespace,
		secretName:      secretName,
		k8sClient:       k8sClient,
	}
}
