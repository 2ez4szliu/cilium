// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package api

import (
	"fmt"
	"testing"

	"github.com/cilium/hive/hivetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cilium/cilium/pkg/endpoint"
	endpointmetadata "github.com/cilium/cilium/pkg/endpoint/metadata"
	slim_corev1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/api/core/v1"
	slim_metav1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	"github.com/cilium/cilium/pkg/labelsfilter"
	"github.com/cilium/cilium/pkg/time"
)

func TestHandleOutdatedPodInformer(t *testing.T) {
	defer func(current time.Duration) { handleOutdatedPodInformerRetryPeriod = current }(handleOutdatedPodInformerRetryPeriod)
	handleOutdatedPodInformerRetryPeriod = 1 * time.Millisecond

	require.NoError(t, labelsfilter.ParseLabelPrefixCfg(hivetest.Logger(t), nil, nil, ""))

	notFoundErr := k8sErrors.NewNotFound(schema.GroupResource{Group: "core", Resource: "pod"}, "foo")

	tests := []struct {
		name    string
		epUID   string
		fetcher fetcherFn
		err     func(uid string) error
		retries uint
	}{
		{
			name: "pod not found",
			fetcher: func(run uint, nsName, podName string) (*slim_corev1.Pod, *endpoint.K8sMetadata, error) {
				return nil, nil, notFoundErr
			},
			err: func(string) error { return notFoundErr },
		},
		{
			name: "uid mismatch",
			fetcher: func(run uint, nsName, podName string) (*slim_corev1.Pod, *endpoint.K8sMetadata, error) {
				return &slim_corev1.Pod{ObjectMeta: slim_metav1.ObjectMeta{
					Name: podName, Namespace: nsName, UID: "other",
				}}, &endpoint.K8sMetadata{}, nil
			},
			err: func(uid string) error {
				if uid == "" {
					return nil
				}
				return endpointmetadata.ErrPodStoreOutdated
			},
			retries: 20,
		},
		{
			name: "uid mismatch, then resolved",
			fetcher: func(run uint, nsName, podName string) (*slim_corev1.Pod, *endpoint.K8sMetadata, error) {
				uid := types.UID("uid")
				if run < 5 {
					uid = types.UID("other")
				}

				return &slim_corev1.Pod{ObjectMeta: slim_metav1.ObjectMeta{
					Name: podName, Namespace: nsName, UID: uid,
				}}, &endpoint.K8sMetadata{}, nil
			},
			err:     func(string) error { return nil },
			retries: 6,
		},
		{
			name: "pod found",
			fetcher: func(run uint, nsName, podName string) (*slim_corev1.Pod, *endpoint.K8sMetadata, error) {
				return &slim_corev1.Pod{ObjectMeta: slim_metav1.ObjectMeta{
					Name: podName, Namespace: nsName, UID: "uid",
				}}, &endpoint.K8sMetadata{}, nil
			},
			err: func(string) error { return nil },
		},
	}

	for _, epUID := range []string{"", "uid"} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s (epUID: %s)", tt.name, epUID), func(t *testing.T) {
				k8sPodFetcher := &fetcher{fn: tt.fetcher}
				apiManager := endpointAPIManager{logger: hivetest.Logger(t), endpointMetadata: k8sPodFetcher}
				ep := endpoint.Endpoint{K8sPodName: "foo", K8sNamespace: "bar", K8sUID: epUID}

				pod, meta, err := apiManager.handleOutdatedPodInformer(t.Context(), &ep)
				assert.Equal(t, tt.err(epUID), err)
				if tt.err(epUID) == nil {
					assert.NotNil(t, pod)
					assert.NotNil(t, meta)
				}

				retries := uint(1)
				if tt.retries > 0 && epUID != "" {
					retries = tt.retries
				}
				assert.Equal(t, retries, k8sPodFetcher.runs, "Incorrect number of retries")
			})
		}
	}
}

type fetcherFn func(run uint, nsName, podName string) (*slim_corev1.Pod, *endpoint.K8sMetadata, error)

type fetcher struct {
	fn   fetcherFn
	runs uint
}

// FetchK8sMetadataForEndpoint implements metadata.EndpointMetadataFetcher.
func (f *fetcher) FetchK8sMetadataForEndpoint(nsName string, podName string, uid string) (*slim_corev1.Pod, *endpoint.K8sMetadata, error) {
	defer func() { f.runs++ }()

	pod, m, err := f.fn(f.runs, nsName, podName)
	if uid != "" && err == nil && pod != nil && string(pod.GetUID()) != uid {
		return nil, nil, endpointmetadata.ErrPodStoreOutdated
	}
	return pod, m, err
}

// FetchK8sMetadataForEndpointFromPod implements metadata.EndpointMetadataFetcher.
func (f *fetcher) FetchK8sMetadataForEndpointFromPod(p *slim_corev1.Pod) (*endpoint.K8sMetadata, error) {
	_, m, err := f.FetchK8sMetadataForEndpoint(p.Namespace, p.Name, string(p.UID))
	return m, err
}

var _ endpointmetadata.EndpointMetadataFetcher = &fetcher{}
