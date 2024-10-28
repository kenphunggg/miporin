package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func License() {
	for {
		targetDate, _ := time.Parse("02-01-2006", "15-11-2024")
		now := time.Now()
		if !now.Before(targetDate) {
			bonalib.Warn("This image is expired, contact to daodaihiep22ussr@gmail.com for extending license")
			panic("This image is expired, contact to daodaihiep22ussr@gmail.com for extending license")
		}
		time.Sleep(86400 * time.Second)
	}
}

// Query data from [Prometheus] and return result in [map]
func Query(query string) map[string]interface{} {
	// This ensures that the query string is formatted correctly
	// Input:  "hello world & friends"
	// Output: "hello%20world%20%26%20friends"
	query = url.QueryEscape(query)

	// Make an HTTP GET request to Prometheus server
	resp, err := http.Get(PROMSERVER + query)
	// Log Error
	if err != nil {
		bonalib.Warn("err", err)
	}

	// Ensure that the response body closed once it is no longer needed
	defer resp.Body.Close()

	// [io.ReadAll] reads data from resp.Body
	body, err := io.ReadAll(resp.Body)
	// Log Error
	if err != nil {
		bonalib.Warn("err", err)
	}

	// Creare a [map] named "result"
	var result map[string]interface{}
	// [json.Unmarshal] convert [body] in [json] to [&result]
	err = json.Unmarshal(body, &result)
	if err != nil {
		bonalib.Warn("err", err)
	}

	return result
}

func weightedNegative(array []int32) []int32 {
	weightedArray := make([]int32, len(array))
	var sum float64
	for _, value := range array {
		if value == 0 {
			sum += 1.0 / float64(0.1)
		} else {
			sum += 1.0 / float64(value)
		}
	}
	weightedArray[len(array)-1] = 100
	var _w float64
	for i := range weightedArray {
		if i != len(array)-1 {
			if array[i] == 0 {
				_w = math.Round((1.0 / float64(0.1)) / float64(sum) * 100)
			} else {
				_w = math.Round((1.0 / float64(array[i])) / float64(sum) * 100)
			}
			if math.IsNaN(_w) {
				weightedArray[i] = 0.0
			} else {
				weightedArray[i] = int32(_w)
			}
			weightedArray[len(array)-1] -= weightedArray[i]
		}
	}
	return weightedArray
}

func weightedPositive(array []int32) []int32 {
	weightedArray := make([]int32, len(array))
	var sum float64
	for _, value := range array {
		sum += float64(value)
	}
	weightedArray[len(array)-1] = 100
	var _w float64
	for i := range weightedArray {
		if i != len(array)-1 {
			_w = math.Round((float64(array[i])) / float64(sum) * 100)
			weightedArray[i] = int32(_w)
			weightedArray[len(array)-1] -= weightedArray[i]
		}
	}
	if reflect.DeepEqual(array, make([]int32, len(NODENAMES))) {
		weightedArray = make([]int32, len(NODENAMES))
	}
	return weightedArray
}

func createServiceMonitor(ksvcName string) {
	bonalib.Info("start creating ServiceMonitor")
	smonGVR := schema.GroupVersionResource{
		Group:    "monitoring.coreos.com",
		Version:  "v1",
		Resource: "servicemonitors",
	}

	smonInstance := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ServiceMonitor",
			"metadata": map[string]interface{}{
				"name":      ksvcName,
				"namespace": "default",
				"labels": map[string]interface{}{
					"app": ksvcName,
				},
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"honorLabels": true,
						"interval":    "1s",
						"path":        "/metrics",
						"port":        "http-usermetric",
					},
				},
				"namespaceSelector": map[string]interface{}{
					"matchNames": []interface{}{
						"default",
					},
				},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"serving.knative.dev/service": ksvcName,
					},
				},
			},
		},
	}

	// create seika instance
	result, err := DYNCLIENT.
		Resource(smonGVR).
		Namespace("default").
		Create(context.TODO(), smonInstance, metav1.CreateOptions{})
	if err != nil {
		fmt.Println(err)
	} else {
		bonalib.Info("Created ServiceMonitor instance", result.GetName())
	}
}

func deleteServiceMonitor(ksvcName string) {
	bonalib.Info("start deleting ServiceMonitor")
	namespace := "default"

	smonGVR := schema.GroupVersionResource{
		Group:    "monitoring.coreos.com",
		Version:  "v1",
		Resource: "servicemonitors",
	}

	deletePolicy := metav1.DeletePropagationBackground
	err := DYNCLIENT.
		Resource(smonGVR).
		Namespace(namespace).
		Delete(context.TODO(), ksvcName, metav1.DeleteOptions{
			PropagationPolicy: &deletePolicy,
		})
	if err != nil {
		bonalib.Warn("Failed to delete ServiceMonitor instance")
	} else {
		bonalib.Info("Deleted ServiceMonitor instance", ksvcName)
	}
}
