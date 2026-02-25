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
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/hetzner/hcloud-go/hcloud"
	podutils "k8s.io/autoscaler/cluster-autoscaler/utils/pod"
	"k8s.io/autoscaler/cluster-autoscaler/utils/units"
	"k8s.io/klog/v2"
)

// hetznerPricingModel implements cloudprovider.PricingModel using hourly
// pricing data already embedded in Hetzner server type metadata. All prices
// are in EUR (gross) which is Hetzner's native billing currency.
type hetznerPricingModel struct {
	manager *hetznerManager

	cpuPricePerHour    float64
	memPricePerGiBHour float64
	ratesOnce          sync.Once
	ratesErr           error
}

func newHetznerPricingModel(manager *hetznerManager) *hetznerPricingModel {
	return &hetznerPricingModel{manager: manager}
}

// NodePrice returns the cost of running the given node for [startTime, endTime).
func (m *hetznerPricingModel) NodePrice(node *apiv1.Node, startTime time.Time, endTime time.Time) (float64, error) {
	instanceType, location := nodeTypeAndLocation(node)
	if instanceType == "" {
		return 0, fmt.Errorf("instance type label not found on node %s", node.Name)
	}

	serverType, err := m.manager.cachedServerType.getServerType(instanceType)
	if err != nil {
		return 0, fmt.Errorf("failed to get server type %s: %v", instanceType, err)
	}

	hourly, err := serverTypeHourlyPrice(serverType, location)
	if err != nil {
		return 0, err
	}

	return hourly * hoursInPeriod(startTime, endTime), nil
}

// PodPrice returns a theoretical minimum cost of running the pod on a perfectly
// matching machine. Base CPU/memory rates are derived once from the full server
// type catalog via ordinary least-squares regression.
func (m *hetznerPricingModel) PodPrice(pod *apiv1.Pod, startTime time.Time, endTime time.Time) (float64, error) {
	cpuRate, memRate, err := m.baseRates()
	if err != nil {
		return 0, err
	}

	requests := podutils.PodRequests(pod)
	cpu := requests[apiv1.ResourceCPU]
	mem := requests[apiv1.ResourceMemory]

	hours := hoursInPeriod(startTime, endTime)
	price := float64(cpu.MilliValue())/1000.0*cpuRate*hours +
		float64(mem.Value())/float64(units.GiB)*memRate*hours

	return price, nil
}

// baseRates derives per-vCPU-hour and per-GiB-hour rates from the Hetzner
// server type catalog. Uses OLS: price ≈ a·cores + b·memoryGiB (no intercept).
// Hetzner reports server memory in binary GiB (e.g. 4, 8, 16), matching the
// GiB unit used to normalize pod memory requests in PodPrice.
func (m *hetznerPricingModel) baseRates() (float64, float64, error) {
	m.ratesOnce.Do(func() {
		serverTypes, err := m.manager.cachedServerType.getAllServerTypes()
		if err != nil {
			m.ratesErr = err
			return
		}

		type sample struct {
			cores, memGiB, price float64
		}
		var samples []sample

		for _, st := range serverTypes {
			if len(st.Pricings) == 0 || st.Cores == 0 {
				continue
			}
			price, err := strconv.ParseFloat(st.Pricings[0].Hourly.Gross, 64)
			if err != nil || price <= 0 {
				continue
			}
			samples = append(samples, sample{
				cores:  float64(st.Cores),
				memGiB: float64(st.Memory),
				price:  price,
			})
		}

		if len(samples) == 0 {
			m.ratesErr = fmt.Errorf("no server types with pricing data available")
			return
		}

		// Normal equations for price = a·cores + b·mem (no intercept):
		//   a·Σ(c²) + b·Σ(cm) = Σ(cp)
		//   a·Σ(cm) + b·Σ(m²) = Σ(mp)
		var cc, cm, mm, cp, mp float64
		for _, s := range samples {
			cc += s.cores * s.cores
			cm += s.cores * s.memGiB
			mm += s.memGiB * s.memGiB
			cp += s.cores * s.price
			mp += s.memGiB * s.price
		}

		det := cc*mm - cm*cm
		if math.Abs(det) < 1e-12 {
			m.cpuPricePerHour = cp / cc
			m.memPricePerGiBHour = 0
			klog.V(4).Infof("Hetzner pricing (degenerate): cpu=%.6f EUR/vCPU/hr", m.cpuPricePerHour)
			return
		}

		a := (mm*cp - cm*mp) / det
		b := (cc*mp - cm*cp) / det

		// Prices cannot be negative. When the unconstrained fit drives one
		// coefficient below zero, pin it to zero and re-fit the other as a
		// single-variable OLS so it stays optimal under the constraint.
		// Single-variable fits are always non-negative (sums of non-negative
		// products), so no second clamp is needed.
		if a < 0 {
			a = 0
			if mm > 0 {
				b = mp / mm
			}
		} else if b < 0 {
			b = 0
			a = cp / cc
		}

		m.cpuPricePerHour = a
		m.memPricePerGiBHour = b
		klog.V(4).Infof("Hetzner pricing base rates: cpu=%.6f EUR/vCPU/hr, mem=%.6f EUR/GiB/hr", a, b)
	})

	return m.cpuPricePerHour, m.memPricePerGiBHour, m.ratesErr
}

func nodeTypeAndLocation(node *apiv1.Node) (instanceType, location string) {
	if node.Labels == nil {
		return
	}
	instanceType = node.Labels[apiv1.LabelInstanceTypeStable]
	if instanceType == "" {
		instanceType = node.Labels[apiv1.LabelInstanceType]
	}
	location = node.Labels[apiv1.LabelTopologyRegion]
	if location == "" {
		location = node.Labels["csi.hetzner.cloud/location"]
	}
	return
}

// serverTypeHourlyPrice returns the gross hourly price for a server type at the
// given location. Falls back to the first available location when unspecified.
func serverTypeHourlyPrice(st *hcloud.ServerType, location string) (float64, error) {
	if len(st.Pricings) == 0 {
		return 0, fmt.Errorf("no pricing data for server type %s", st.Name)
	}

	for _, p := range st.Pricings {
		if location != "" && p.Location != nil && p.Location.Name == location {
			return parseGrossHourly(st.Name, location, p.Hourly.Gross)
		}
	}

	return parseGrossHourly(st.Name, "default", st.Pricings[0].Hourly.Gross)
}

func parseGrossHourly(serverType, location, raw string) (float64, error) {
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse hourly price for %s at %s: %v", serverType, location, err)
	}
	return price, nil
}

func hoursInPeriod(startTime, endTime time.Time) float64 {
	minutes := math.Ceil(float64(endTime.Sub(startTime)) / float64(time.Minute))
	return minutes / 60.0
}
