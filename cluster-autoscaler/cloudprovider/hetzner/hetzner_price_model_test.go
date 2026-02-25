/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hetzner

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/hetzner/hcloud-go/hcloud"
	podutils "k8s.io/autoscaler/cluster-autoscaler/utils/pod"
)

func newTestPricingModel(serverTypes []*hcloud.ServerType) *hetznerPricingModel {
	client := hcloud.NewClient(hcloud.WithToken("dummy"))
	cache := newServerTypeCache(context.Background(), client)

	_ = cache.Add(serverTypeCachedObject{
		name:        serverTypeCacheKey,
		serverTypes: serverTypes,
	})

	manager := &hetznerManager{
		cachedServerType: cache,
	}
	return newHetznerPricingModel(manager)
}

func testServerTypes() []*hcloud.ServerType {
	return []*hcloud.ServerType{
		{
			ID: 1, Name: "cx22", Cores: 2, Memory: 4,
			Pricings: []hcloud.ServerTypeLocationPricing{
				{Location: &hcloud.Location{Name: "fsn1"}, Hourly: hcloud.Price{Gross: "0.0065"}},
				{Location: &hcloud.Location{Name: "nbg1"}, Hourly: hcloud.Price{Gross: "0.0065"}},
				{Location: &hcloud.Location{Name: "hel1"}, Hourly: hcloud.Price{Gross: "0.0065"}},
			},
		},
		{
			ID: 2, Name: "cx32", Cores: 4, Memory: 8,
			Pricings: []hcloud.ServerTypeLocationPricing{
				{Location: &hcloud.Location{Name: "fsn1"}, Hourly: hcloud.Price{Gross: "0.0119"}},
				{Location: &hcloud.Location{Name: "nbg1"}, Hourly: hcloud.Price{Gross: "0.0119"}},
				{Location: &hcloud.Location{Name: "hel1"}, Hourly: hcloud.Price{Gross: "0.0119"}},
			},
		},
		{
			ID: 3, Name: "cx42", Cores: 8, Memory: 16,
			Pricings: []hcloud.ServerTypeLocationPricing{
				{Location: &hcloud.Location{Name: "fsn1"}, Hourly: hcloud.Price{Gross: "0.0235"}},
				{Location: &hcloud.Location{Name: "nbg1"}, Hourly: hcloud.Price{Gross: "0.0235"}},
				{Location: &hcloud.Location{Name: "hel1"}, Hourly: hcloud.Price{Gross: "0.0235"}},
			},
		},
		{
			ID: 4, Name: "cax11", Cores: 2, Memory: 4, Architecture: hcloud.ArchitectureARM,
			Pricings: []hcloud.ServerTypeLocationPricing{
				{Location: &hcloud.Location{Name: "fsn1"}, Hourly: hcloud.Price{Gross: "0.0044"}},
			},
		},
	}
}

func TestNodePrice(t *testing.T) {
	model := newTestPricingModel(testServerTypes())
	now := time.Now()
	oneHourLater := now.Add(time.Hour)

	t.Run("known server type and location", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceTypeStable: "cx22",
					apiv1.LabelTopologyRegion:     "fsn1",
				},
			},
		}

		price, err := model.NodePrice(node, now, oneHourLater)
		require.NoError(t, err)
		assert.InDelta(t, 0.0065, price, 1e-6)
	})

	t.Run("two hours", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceTypeStable: "cx32",
					apiv1.LabelTopologyRegion:     "nbg1",
				},
			},
		}

		twoHoursLater := now.Add(2 * time.Hour)
		price, err := model.NodePrice(node, now, twoHoursLater)
		require.NoError(t, err)
		assert.InDelta(t, 0.0119*2, price, 1e-6)
	})

	t.Run("fallback to legacy instance type label", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceType:   "cx42",
					apiv1.LabelTopologyRegion: "hel1",
				},
			},
		}

		price, err := model.NodePrice(node, now, oneHourLater)
		require.NoError(t, err)
		assert.InDelta(t, 0.0235, price, 1e-6)
	})

	t.Run("missing instance type label", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelTopologyRegion: "fsn1",
				},
			},
		}

		_, err := model.NodePrice(node, now, oneHourLater)
		assert.Error(t, err)
	})

	t.Run("unknown server type", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceTypeStable: "nonexistent",
					apiv1.LabelTopologyRegion:     "fsn1",
				},
			},
		}

		_, err := model.NodePrice(node, now, oneHourLater)
		assert.Error(t, err)
	})

	t.Run("fallback location when not found", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceTypeStable: "cax11",
					apiv1.LabelTopologyRegion:     "ash",
				},
			},
		}

		price, err := model.NodePrice(node, now, oneHourLater)
		require.NoError(t, err)
		assert.InDelta(t, 0.0044, price, 1e-6)
	})
}

func TestPodPrice(t *testing.T) {
	model := newTestPricingModel(testServerTypes())
	now := time.Now()
	oneHourLater := now.Add(time.Hour)

	t.Run("pod with cpu and memory requests", func(t *testing.T) {
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{
						Resources: apiv1.ResourceRequirements{
							Requests: apiv1.ResourceList{
								apiv1.ResourceCPU:    resource.MustParse("1"),
								apiv1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		price, err := model.PodPrice(pod, now, oneHourLater)
		require.NoError(t, err)
		assert.Greater(t, price, 0.0)
	})

	t.Run("larger pod costs more", func(t *testing.T) {
		smallPod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{
						Resources: apiv1.ResourceRequirements{
							Requests: apiv1.ResourceList{
								apiv1.ResourceCPU:    resource.MustParse("500m"),
								apiv1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
		}

		largePod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{
						Resources: apiv1.ResourceRequirements{
							Requests: apiv1.ResourceList{
								apiv1.ResourceCPU:    resource.MustParse("4"),
								apiv1.ResourceMemory: resource.MustParse("8Gi"),
							},
						},
					},
				},
			},
		}

		smallPrice, err := model.PodPrice(smallPod, now, oneHourLater)
		require.NoError(t, err)

		largePrice, err := model.PodPrice(largePod, now, oneHourLater)
		require.NoError(t, err)

		assert.Greater(t, largePrice, smallPrice)
	})

	t.Run("zero requests yields zero price", func(t *testing.T) {
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{}},
				},
			},
		}

		price, err := model.PodPrice(pod, now, oneHourLater)
		require.NoError(t, err)
		assert.Equal(t, 0.0, price)
	})

	t.Run("longer period costs more", func(t *testing.T) {
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{
						Resources: apiv1.ResourceRequirements{
							Requests: apiv1.ResourceList{
								apiv1.ResourceCPU:    resource.MustParse("1"),
								apiv1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		price1h, err := model.PodPrice(pod, now, oneHourLater)
		require.NoError(t, err)

		price3h, err := model.PodPrice(pod, now, now.Add(3*time.Hour))
		require.NoError(t, err)

		assert.InDelta(t, price1h*3, price3h, 1e-9)
	})
}

func TestBaseRates(t *testing.T) {
	model := newTestPricingModel(testServerTypes())

	cpuRate, memRate, err := model.baseRates()
	require.NoError(t, err)

	assert.Greater(t, cpuRate, 0.0, "cpu rate must be positive")
	assert.GreaterOrEqual(t, memRate, 0.0, "mem rate must be non-negative")

	// Verify the rates roughly reconstruct known prices.
	// cx22: 2 cores, 4 GB → ~0.0065
	predicted := cpuRate*2 + memRate*4
	assert.InDelta(t, 0.0065, predicted, 0.003, "cx22 price prediction should be reasonable")
}

func TestServerTypeHourlyPrice(t *testing.T) {
	st := &hcloud.ServerType{
		Name: "cx22",
		Pricings: []hcloud.ServerTypeLocationPricing{
			{Location: &hcloud.Location{Name: "fsn1"}, Hourly: hcloud.Price{Gross: "0.0065"}},
			{Location: &hcloud.Location{Name: "nbg1"}, Hourly: hcloud.Price{Gross: "0.0070"}},
		},
	}

	t.Run("exact location match", func(t *testing.T) {
		price, err := serverTypeHourlyPrice(st, "nbg1")
		require.NoError(t, err)
		assert.InDelta(t, 0.0070, price, 1e-6)
	})

	t.Run("fallback to first when location not found", func(t *testing.T) {
		price, err := serverTypeHourlyPrice(st, "us-east")
		require.NoError(t, err)
		assert.InDelta(t, 0.0065, price, 1e-6)
	})

	t.Run("empty location uses first", func(t *testing.T) {
		price, err := serverTypeHourlyPrice(st, "")
		require.NoError(t, err)
		assert.InDelta(t, 0.0065, price, 1e-6)
	})

	t.Run("no pricings returns error", func(t *testing.T) {
		empty := &hcloud.ServerType{Name: "empty"}
		_, err := serverTypeHourlyPrice(empty, "fsn1")
		assert.Error(t, err)
	})
}

func TestNodeTypeAndLocation(t *testing.T) {
	t.Run("stable labels", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceTypeStable: "cx22",
					apiv1.LabelTopologyRegion:     "fsn1",
				},
			},
		}
		it, loc := nodeTypeAndLocation(node)
		assert.Equal(t, "cx22", it)
		assert.Equal(t, "fsn1", loc)
	})

	t.Run("legacy labels", func(t *testing.T) {
		node := &apiv1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apiv1.LabelInstanceType:      "cx32",
					"csi.hetzner.cloud/location": "nbg1",
				},
			},
		}
		it, loc := nodeTypeAndLocation(node)
		assert.Equal(t, "cx32", it)
		assert.Equal(t, "nbg1", loc)
	})

	t.Run("nil labels", func(t *testing.T) {
		node := &apiv1.Node{}
		it, loc := nodeTypeAndLocation(node)
		assert.Empty(t, it)
		assert.Empty(t, loc)
	})
}

func TestHoursInPeriod(t *testing.T) {
	now := time.Now()

	assert.InDelta(t, 1.0, hoursInPeriod(now, now.Add(time.Hour)), 1e-9)
	assert.InDelta(t, 0.5, hoursInPeriod(now, now.Add(30*time.Minute)), 1e-9)
	assert.InDelta(t, 24.0, hoursInPeriod(now, now.Add(24*time.Hour)), 1e-9)
}

func TestPodRequests(t *testing.T) {
	t.Run("sum of regular containers", func(t *testing.T) {
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("500m"),
							apiv1.ResourceMemory: resource.MustParse("1Gi"),
						},
					}},
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("300m"),
							apiv1.ResourceMemory: resource.MustParse("512Mi"),
						},
					}},
				},
			},
		}
		reqs := podutils.PodRequests(pod)
		assert.Equal(t, int64(800), reqs.Cpu().MilliValue())
		assert.Equal(t, int64(1536*1024*1024), reqs.Memory().Value())
	})

	t.Run("init container trumps when larger", func(t *testing.T) {
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				InitContainers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("2"),
							apiv1.ResourceMemory: resource.MustParse("4Gi"),
						},
					}},
				},
				Containers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("500m"),
							apiv1.ResourceMemory: resource.MustParse("1Gi"),
						},
					}},
				},
			},
		}
		reqs := podutils.PodRequests(pod)
		assert.Equal(t, int64(2000), reqs.Cpu().MilliValue())
		assert.Equal(t, int64(4*1024*1024*1024), reqs.Memory().Value())
	})

	t.Run("regular containers trump when larger", func(t *testing.T) {
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				InitContainers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU: resource.MustParse("100m"),
						},
					}},
				},
				Containers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1"),
							apiv1.ResourceMemory: resource.MustParse("2Gi"),
						},
					}},
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1"),
							apiv1.ResourceMemory: resource.MustParse("2Gi"),
						},
					}},
				},
			},
		}
		reqs := podutils.PodRequests(pod)
		assert.Equal(t, int64(2000), reqs.Cpu().MilliValue())
		assert.Equal(t, int64(4*1024*1024*1024), reqs.Memory().Value())
	})

	t.Run("restartable init container (sidecar) adds to regular requests", func(t *testing.T) {
		alwaysRestart := apiv1.ContainerRestartPolicyAlways
		pod := &apiv1.Pod{
			Spec: apiv1.PodSpec{
				InitContainers: []apiv1.Container{
					{RestartPolicy: &alwaysRestart, Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("500m"),
							apiv1.ResourceMemory: resource.MustParse("1Gi"),
						},
					}},
				},
				Containers: []apiv1.Container{
					{Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1"),
							apiv1.ResourceMemory: resource.MustParse("2Gi"),
						},
					}},
				},
			},
		}
		// Sidecar requests are additive, not max'd: 1000m + 500m, 2Gi + 1Gi.
		reqs := podutils.PodRequests(pod)
		assert.Equal(t, int64(1500), reqs.Cpu().MilliValue())
		assert.Equal(t, int64(3*1024*1024*1024), reqs.Memory().Value())
	})
}

func TestNodePriceScalesLinearly(t *testing.T) {
	model := newTestPricingModel(testServerTypes())
	now := time.Now()

	node := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				apiv1.LabelInstanceTypeStable: "cx22",
				apiv1.LabelTopologyRegion:     "fsn1",
			},
		},
	}

	price1h, err := model.NodePrice(node, now, now.Add(time.Hour))
	require.NoError(t, err)

	price10h, err := model.NodePrice(node, now, now.Add(10*time.Hour))
	require.NoError(t, err)

	assert.InDelta(t, price1h*10, price10h, 1e-9)
}

func TestPricingModelConsistency(t *testing.T) {
	model := newTestPricingModel(testServerTypes())
	now := time.Now()
	oneHourLater := now.Add(time.Hour)

	node := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				apiv1.LabelInstanceTypeStable: "cx22",
				apiv1.LabelTopologyRegion:     "fsn1",
			},
		},
	}

	nodePrice, err := model.NodePrice(node, now, oneHourLater)
	require.NoError(t, err)

	// A pod requesting all node resources should cost less than or equal to
	// the node (the OLS fit may not be exact, but should be in the right ballpark).
	fullPod := &apiv1.Pod{
		Spec: apiv1.PodSpec{
			Containers: []apiv1.Container{
				{
					Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("2"),
							apiv1.ResourceMemory: resource.MustParse("4Gi"),
						},
					},
				},
			},
		},
	}

	podPrice, err := model.PodPrice(fullPod, now, oneHourLater)
	require.NoError(t, err)

	assert.Greater(t, podPrice, 0.0)
	assert.Less(t, math.Abs(podPrice-nodePrice)/nodePrice, 0.5,
		"pod price for full node resources should be within 50%% of node price")
}
