package exporter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// Exercise HTTP parsing, the rule registry, device normalization, duplicate
// removal, allocation joins and public exposition together. Values are synthetic;
// native labels and allocation IDs follow the official DCU 2.4 sources.
func TestHygonRawEndpointProducesDeviceAndAllocationLayers(t *testing.T) {
	t.Parallel()
	const raw = `dcu_utilizationrate{node="node-dcu",device_id="SERIAL-BW",name="BW100",pcieBus_number="0000:f6:00.0",dcu_pod_name="untrusted"} 75
vdcu_usedmemory_bytes{node="node-dcu",device_id="SERIAL-BW",name="BW100",vdcu_minor_number="7",vdcu_computer_unit="8",vdcu_memory_cap="17179869184",container="a"} 4294967296
vdcu_usedmemory_bytes{node="node-dcu",device_id="SERIAL-BW",name="BW100",vdcu_minor_number="7",vdcu_computer_unit="8",vdcu_memory_cap="17179869184",container="b"} 4294967296
vdcu_utilizationrate{node="node-dcu",device_id="SERIAL-BW",name="BW100",vdcu_minor_number="7",vdcu_computer_unit="8",vdcu_memory_cap="17179869184"} 40
`
	scraper := NewHTTPScraper(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})})
	targets := fakeTargetProvider{targets: []accelerator.Source{{Name: "dcu", Address: "http://dcu/metrics", Rule: accelerator.RuleHygonDCU}}}
	allocations := fakeAllocationProvider{allocations: []accelerator.Allocation{
		{Namespace: "ns", Pod: "physical", PodUID: "puid", Container: "worker", NativeResource: "hygon.com/dcu", NativeDeviceID: "0000:f6:00.0"},
		{Namespace: "ns", Pod: "virtual", PodUID: "vuid", Container: "worker", NativeResource: "hygon.com/dcu-share-8c-16g", NativeDeviceID: "vdev7"},
	}}
	exporter, err := New(NewDefaultOptions(), scraper, targets, allocations, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(exporter)
	for _, want := range []string{
		`accelerator_core_utilization_percentage{device_id="SERIAL-BW",device_mode="physical",device_type="dcu",model="bw100",node="node-dcu",vendor="hygon"} 75`,
		`accelerator_memory_utilization_percentage{device_id="SERIAL-BW.7",device_mode="virtual",device_type="dcu",model="bw100",node="node-dcu",parent_device_id="SERIAL-BW",vendor="hygon"} 25`,
		`accelerator_pod_info{container="worker",device_id="SERIAL-BW",namespace="ns",node="node-dcu",pod="physical",pod_uid="puid"} 1`,
		`accelerator_pod_info{container="worker",device_id="SERIAL-BW.7",namespace="ns",node="node-dcu",pod="virtual",pod_uid="vuid"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s:\n%s", want, body)
		}
	}
	if got := strings.Count(body, "\naccelerator_memory_used_bytes{"); got != 1 {
		t.Errorf("duplicate native rows produced %d used-memory observations:\n%s", got, body)
	}
	if strings.Contains(body, "untrusted") || strings.Contains(body, "vdcu_memory_cap=") || strings.Contains(body, "dcu_pod_name=") {
		t.Errorf("vendor ownership/metadata leaked into the public contract:\n%s", body)
	}
}
