// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver
// +build kubeapiserver

package helm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/DataDog/datadog-agent/pkg/aggregator/mocksender"
	coreMetrics "github.com/DataDog/datadog-agent/pkg/metrics"
)

func TestRun(t *testing.T) {
	releases := []release{
		{
			Name: "my_datadog",
			Info: &info{
				Status: "deployed",
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "datadog",
					Version:    "2.30.5",
					AppVersion: "7",
				},
			},
			Version:   1,
			Namespace: "default",
		},
		{
			Name: "my_app",
			Info: &info{
				Status: "deployed",
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "some_app",
					Version:    "1.1.0",
					AppVersion: "1",
				},
			},
			Version:   2,
			Namespace: "app",
		},
		{ // Release with a nil chart reference
			Name: "release_without_chart",
			Info: &info{
				Status: "deployed",
			},
			Chart:     nil,
			Version:   1,
			Namespace: "default",
		},
		{ // Release with a nil info reference
			Name: "release_without_info",
			Info: nil,
			Chart: &chart{
				Metadata: &metadata{
					Name:       "example_app",
					Version:    "2.0.0",
					AppVersion: "1",
				},
			},
			Version:   1,
			Namespace: "default",
		},
	}

	// Same order as "releases" array
	var secretsForReleases []*v1.Secret
	for _, rel := range releases {
		secret, err := secretForRelease(&rel, time.Now())
		assert.NoError(t, err)
		secretsForReleases = append(secretsForReleases, secret)
	}

	// Same order as "releases" array
	var configmapsForReleases []*v1.ConfigMap
	for _, rel := range releases {
		configMap, err := configMapForRelease(&rel)
		assert.NoError(t, err)
		configmapsForReleases = append(configmapsForReleases, configMap)
	}

	// Same order as "releases" array
	expectedTagsForReleases := [][]string{
		{
			"helm_release:my_datadog",
			"helm_chart_name:datadog",
			"kube_namespace:default",
			"helm_namespace:default",
			"helm_revision:1",
			"helm_status:deployed",
			"helm_chart_version:2.30.5",
			"helm_app_version:7",
		},
		{
			"helm_release:my_app",
			"helm_chart_name:some_app",
			"kube_namespace:app",
			"helm_namespace:app",
			"helm_revision:2",
			"helm_status:deployed",
			"helm_chart_version:1.1.0",
			"helm_app_version:1",
		},
		{
			"helm_release:release_without_chart",
			"kube_namespace:default",
			"helm_namespace:default",
			"helm_revision:1",
			"helm_status:deployed",
		},
		{
			"helm_release:release_without_info",
			"helm_chart_name:example_app",
			"kube_namespace:default",
			"helm_namespace:default",
			"helm_revision:1",
			"helm_chart_version:2.0.0",
			"helm_app_version:1",
		},
	}

	tests := []struct {
		name         string
		secrets      []*v1.Secret
		configmaps   []*v1.ConfigMap
		expectedTags [][]string
	}{
		{
			name:         "using secrets",
			secrets:      secretsForReleases,
			expectedTags: expectedTagsForReleases,
		},
		{
			name:         "using configmaps",
			configmaps:   configmapsForReleases,
			expectedTags: expectedTagsForReleases,
		},
		{
			name:         "using secrets and configmaps",
			secrets:      []*v1.Secret{secretsForReleases[0]},
			configmaps:   configmapsForReleases[1:],
			expectedTags: expectedTagsForReleases,
		},
		{
			name: "no secrets or configmaps owned by Helm",
			secrets: []*v1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "some_secret",
						Labels: map[string]string{"owner": "not-helm"},
					},
				},
			},
			configmaps: []*v1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "some_configmap",
						Labels: map[string]string{"owner": "not-helm"},
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stopCh := make(chan struct{})
			defer close(stopCh)

			var kubeObjects []runtime.Object
			for _, secret := range test.secrets {
				kubeObjects = append(kubeObjects, secret)
			}
			for _, configMap := range test.configmaps {
				kubeObjects = append(kubeObjects, configMap)
			}

			check := factory().(*HelmCheck)
			check.runLeaderElection = false

			check.informerFactory = informers.NewSharedInformerFactory(
				fake.NewSimpleClientset(kubeObjects...),
				time.Minute,
			)

			mockedSender := mocksender.NewMockSender(checkName)
			mockedSender.SetupAcceptAll()

			err := check.Run()
			assert.NoError(t, err)

			for _, tags := range test.expectedTags {
				mockedSender.AssertMetric(t, "Gauge", "helm.release", 1, "", tags)
			}
		})
	}
}

func TestRun_withCollectEvents(t *testing.T) {
	check := factory().(*HelmCheck)
	check.runLeaderElection = false
	check.instance.CollectEvents = true
	check.startTS = time.Now()

	rel := release{
		Name: "my_datadog",
		Info: &info{
			Status: "deployed",
		},
		Chart: &chart{
			Metadata: &metadata{
				Name:       "datadog",
				Version:    "2.30.5",
				AppVersion: "7",
			},
		},
		Version:   1,
		Namespace: "default",
	}

	secret, err := secretForRelease(&rel, time.Now().Add(10))
	assert.NoError(t, err)

	k8sClient := fake.NewSimpleClientset()
	check.informerFactory = informers.NewSharedInformerFactory(k8sClient, time.Minute)

	mockedSender := mocksender.NewMockSender(checkName)
	mockedSender.SetupAcceptAll()

	// Create a new release and check that it creates the appropriate event.
	_, err = k8sClient.CoreV1().Secrets("default").Create(context.TODO(), secret, metav1.CreateOptions{})
	assert.NoError(t, err)
	assert.Eventually(t, func() bool {
		err = check.Run()
		assert.NoError(t, err)
		return mockedSender.AssertEvent(
			t,
			eventForRelease(&rel, k8sSecrets, "New Helm release \"my_datadog\" has been deployed in \"default\" namespace. Its status is \"deployed\".", false),
			10*time.Second,
		)
	}, 5*time.Second, time.Millisecond*100)

	// Upgrade the release and check that it creates the appropriate event.
	upgradedRel := rel
	upgradedRel.Version = 2
	secretUpgradedRel, err := secretForRelease(&upgradedRel, time.Now().Add(10))
	assert.NoError(t, err)
	_, err = k8sClient.CoreV1().Secrets("default").Create(context.TODO(), secretUpgradedRel, metav1.CreateOptions{})
	assert.NoError(t, err)
	assert.Eventually(t, func() bool {
		err = check.Run()
		assert.NoError(t, err)
		return mockedSender.AssertEvent(
			t,
			eventForRelease(&rel, k8sSecrets, "Helm release \"my_datadog\" in \"default\" namespace upgraded to revision 2. Its status is \"deployed\".", false),
			10*time.Second,
		)
	}, 5*time.Second, time.Millisecond*100)

	// Delete the release (all revisions) and check that it creates the
	// appropriate event.
	err = k8sClient.CoreV1().Secrets("default").Delete(context.TODO(), rel.Name+".1", metav1.DeleteOptions{})
	assert.NoError(t, err)
	err = k8sClient.CoreV1().Secrets("default").Delete(context.TODO(), rel.Name+".2", metav1.DeleteOptions{})
	assert.NoError(t, err)
	assert.Eventually(t, func() bool {
		err = check.Run()
		assert.NoError(t, err)
		return mockedSender.AssertEvent(
			t,
			eventForRelease(&rel, k8sSecrets, "Helm release \"my_datadog\" in \"default\" namespace has been deleted.", true),
			10*time.Second,
		)
	}, 5*time.Second, time.Millisecond*100)
}

func TestRun_skipEventForExistingRelease(t *testing.T) {
	check := factory().(*HelmCheck)
	check.runLeaderElection = false
	check.instance.CollectEvents = true
	check.startTS = time.Now()

	rel := release{
		Name: "my_datadog",
		Info: &info{
			Status: "deployed",
		},
		Chart: &chart{
			Metadata: &metadata{
				Name:       "datadog",
				Version:    "2.30.5",
				AppVersion: "7",
			},
		},
		Version:   1,
		Namespace: "default",
	}

	secret, err := secretForRelease(&rel, time.Now().Add(-10))
	assert.NoError(t, err)

	k8sClient := fake.NewSimpleClientset()
	check.informerFactory = informers.NewSharedInformerFactory(k8sClient, time.Minute)

	mockedSender := mocksender.NewMockSender(checkName)
	mockedSender.SetupAcceptAll()

	// Create a new release and check that we never send an event for it
	_, err = k8sClient.CoreV1().Secrets("default").Create(context.TODO(), secret, metav1.CreateOptions{})
	assert.NoError(t, err)
	err = check.Run()
	assert.NoError(t, err)
	mockedSender.AssertNotCalled(t, "Event")
}

func TestRun_ServiceCheck(t *testing.T) {
	// Releases used for this test:
	// - "my_datadog" has 2 revisions without failures.
	// - "my_app" has 2 revisions but the latest one is not in "failed" state.
	// - "my_proxy" has 2 revisions and the latest one is in "failed" state.
	//
	// Only "my_proxy" should be marked as failed by the service check.
	releases := []*release{
		{
			Name: "my_datadog",
			Info: &info{
				Status: "superseded",
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "datadog",
					Version:    "2.30.5",
					AppVersion: "7",
				},
			},
			Version:   1,
			Namespace: "default",
		},
		{
			Name: "my_datadog",
			Info: &info{
				Status: "deployed",
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "datadog",
					Version:    "2.30.5",
					AppVersion: "7",
				},
			},
			Version:   2,
			Namespace: "default",
		},
		{
			Name: "my_app",
			Info: &info{
				Status: "failed", // Notice that it's failed, but it's not the latest release
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "some_app",
					Version:    "1.0.0",
					AppVersion: "1",
				},
			},
			Version:   1,
			Namespace: "default",
		},
		{
			Name: "my_app",
			Info: &info{
				Status: "deployed",
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "some_app",
					Version:    "1.0.0",
					AppVersion: "1",
				},
			},
			Version:   2,
			Namespace: "default",
		},
		{
			Name: "my_proxy",
			Info: &info{
				Status: "deployed",
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "nginx",
					Version:    "1.0.0",
					AppVersion: "1",
				},
			},
			Version:   10,
			Namespace: "default",
		},
		{
			Name: "my_proxy",
			Info: &info{
				Status: "failed", // Notice that this is the latest release, and it's failed
			},
			Chart: &chart{
				Metadata: &metadata{
					Name:       "nginx",
					Version:    "1.0.0",
					AppVersion: "1",
				},
			},
			Version:   12,
			Namespace: "default",
		},
	}

	tests := []struct {
		name    string
		storage helmStorage
	}{
		{
			name:    "using secrets storage",
			storage: k8sSecrets,
		},
		{
			name:    "using configmaps storage",
			storage: k8sConfigmaps,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := factory().(*HelmCheck)
			check.runLeaderElection = false

			for _, rel := range releases {
				check.store.add(rel, test.storage)
			}

			mockedSender := mocksender.NewMockSender(checkName)
			mockedSender.SetupAcceptAll()

			k8sClient := fake.NewSimpleClientset()
			check.informerFactory = informers.NewSharedInformerFactory(k8sClient, time.Minute)

			err := check.Run()
			assert.NoError(t, err)

			// "my_datadog" release should report OK.
			mockedSender.AssertServiceCheck(
				t,
				"helm.release_state",
				coreMetrics.ServiceCheckOK,
				"",
				[]string{
					"helm_release:my_datadog",
					"kube_namespace:default",
					"helm_namespace:default",
					fmt.Sprintf("helm_storage:%s", test.storage),
					"helm_chart_name:datadog",
				},
				"",
			)

			// "my_app" release should report OK.
			mockedSender.AssertServiceCheck(
				t,
				"helm.release_state",
				coreMetrics.ServiceCheckOK,
				"",
				[]string{
					"helm_release:my_app",
					"kube_namespace:default",
					"helm_namespace:default",
					fmt.Sprintf("helm_storage:%s", test.storage),
					"helm_chart_name:some_app",
				},
				"",
			)

			// "my_proxy" release should report a failure.
			mockedSender.AssertServiceCheck(
				t,
				"helm.release_state",
				coreMetrics.ServiceCheckCritical,
				"",
				[]string{
					"helm_release:my_proxy",
					"kube_namespace:default",
					"helm_namespace:default",
					fmt.Sprintf("helm_storage:%s", test.storage),
					"helm_chart_name:nginx",
				},
				"Release in \"failed\" state",
			)
		})
	}

}

// secretForRelease returns a Kubernetes secret that contains the info of the
// given Helm release.
func secretForRelease(rls *release, creationTS time.Time) (*v1.Secret, error) {
	encodedRel, err := encodeRelease(rls)
	if err != nil {
		return nil, err
	}

	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			// The name is not important for this test. We only need to make
			// sure that there are no collisions.
			Name:              fmt.Sprintf("%s.%d", rls.Name, rls.Version),
			Labels:            map[string]string{"owner": "helm"},
			CreationTimestamp: metav1.NewTime(creationTS),
		},
		Data: map[string][]byte{"release": []byte(encodedRel)},
	}, nil
}

// configMapForRelease returns a configmap that contains the info of the given
// Helm release.
func configMapForRelease(rls *release) (*v1.ConfigMap, error) {
	encodedRel, err := encodeRelease(rls)
	if err != nil {
		return nil, err
	}

	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			// The name is not important for this test. We only need to make
			// sure that there are no collisions.
			Name:   fmt.Sprintf("%s.%d", rls.Name, rls.Version),
			Labels: map[string]string{"owner": "helm"},
		},
		Data: map[string]string{"release": encodedRel},
	}, nil
}

// Note: the encodeRelease function has been copied from the Helm library.
// It's private, so we can't reuse it.
// Ref: https://github.com/helm/helm/blob/v3.8.0/pkg/storage/driver/util.go#L35

// encodeRelease encodes a release returning a base64 encoded
// gzipped string representation, or error.
func encodeRelease(rls *release) (string, error) {
	b, err := json.Marshal(rls)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err = w.Write(b); err != nil {
		return "", err
	}
	w.Close()

	return b64.EncodeToString(buf.Bytes()), nil
}
