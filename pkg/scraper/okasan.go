package scraper

import (
	"context"
	"fmt"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	"github.com/bonavadeur/miporin/pkg/libs"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

var _ = bonalib.Baka()

type OkasanScraper struct {
	Name      string
	Latency   [][]int32 // Latency metric
	Window    string
	sleepTime int8
	Kodomo    map[string]*KodomoScraper
}

func NewOkasanScraper(
	name string,
	window string,
	sleepTime int8,
) *OkasanScraper {

	atarashiiOkasanScraper := &OkasanScraper{
		Name:      name,
		Latency:   [][]int32{},
		Window:    window,
		sleepTime: sleepTime,
		Kodomo:    map[string]*KodomoScraper{},
	}
	atarashiiOkasanScraper.init()

	// okasan scrape common metrics like: latency,
	go atarashiiOkasanScraper.scrape()

	// okasan watch ksvc create event to add or remove kodomo
	go atarashiiOkasanScraper.watchKsvcCreateEvent()

	return atarashiiOkasanScraper
}

// Initialize [OkasanScraper], for each [ksvc], create a [KodomoScraper]
func (o *OkasanScraper) init() {
	ksvcGVR := schema.GroupVersionResource{
		Group:    "serving.knative.dev",
		Version:  "v1",
		Resource: "services",
	}
	// List all ksvc in default namespace and hold the value in [ksvcList]
	ksvcList, err := DYNCLIENT.Resource(ksvcGVR).Namespace("default").List(context.TODO(), v1.ListOptions{})
	// If there is error
	if err != nil {
		bonalib.Warn("Error listing Knative services", err)
	}
	// Looping for all ksvc
	for _, ksvc := range ksvcList.Items {
		// Get ksvc name
		ksvcName := ksvc.GetName()
		// For each ksvc, create a new [KodomoScraper]
		// The value equal to [OkasanScraper]
		child := NewKodomoScraper(ksvcName, o.Window, o.sleepTime)
		// Add new kodomo
		o.addKodomo(child)
	}
}

// Scrape important informations
func (o *OkasanScraper) scrape() {
	// Scrape latency
	go o.scrapeLatency()
}

// Scrape latency
func (o *OkasanScraper) scrapeLatency() [][]int32 {
	for {
		// Query latency scraped from [Monlat] in [Prometheus] through [ServiceMonitor]
		latencyRaw := Query("avg_over_time(latency_between_nodes[" + o.Window + "s])")
		latencyResult := latencyRaw["data"].(map[string]interface{})["result"].([]interface{})

		// Initialize a 2-D slice
		// Size of the matrix equal to number of nodes
		latency := make([][]int32, len(NODENAMES))
		// For each row of latency matrix
		for i := range latency {
			latency[i] = make([]int32, len(NODENAMES))
		}

		nodeIndex := map[string]int32{}
		for i, node := range NODENAMES {
			nodeIndex[node] = int32(i)
		}

		for _, lr := range latencyResult {
			// Return metric map
			// a.k.a latency between nodes
			lrMetric := lr.(map[string]interface{})["metric"].(map[string]interface{})
			lrValue := libs.String2RoundedInt(lr.(map[string]interface{})["value"].([]interface{})[1].(string))
			// Return latency metric
			latency[nodeIndex[lrMetric["from"].(string)]][nodeIndex[lrMetric["to"].(string)]] = lrValue
		}

		// bonalib.Log("latency", latency)

		// Import value into [OkasanScraper]
		o.Latency = latency

		time.Sleep(time.Duration(o.sleepTime) * time.Second)
	}
}

func (o *OkasanScraper) watchKsvcCreateEvent() {
	// The below code equal to
	// kubectl get ksvc -n default --watch
	namespace := "default"

	ksvcGVR := schema.GroupVersionResource{
		Group:    "serving.knative.dev",
		Version:  "v1",
		Resource: "services",
	}
	watcher, err := DYNCLIENT.Resource(ksvcGVR).Namespace(namespace).Watch(context.TODO(), metav1.ListOptions{
		Watch: true,
	})

	if err != nil {
		fmt.Println(err)
		panic(err.Error())
	}

	// Event handling code
	for event := range watcher.ResultChan() {
		// Catch ksvc name for each event
		ksvc, _ := event.Object.(*unstructured.Unstructured)
		ksvcName, _, _ := unstructured.NestedString(ksvc.Object, "metadata", "name")

		// If new ksvc is created
		if event.Type == watch.Added {
			// Create new [KodomoScraper]
			child := NewKodomoScraper(ksvcName, o.Window, int8(2))
			// Add created [KodomoSraper] to [OkasanScraper]
			o.addKodomo(child)
			// Create new [ServiceMonitor]
			createServiceMonitor(ksvcName)
		}
		// If new ksvc is deleted
		if event.Type == watch.Deleted {
			// Delete Service Monitor
			deleteServiceMonitor(ksvcName)
			// Delete [KodomoScraper]
			o.deleteKodomo(ksvcName)
		}
	}
}

func (o *OkasanScraper) addKodomo(kodomo *KodomoScraper) {
	kodomo.Okasan = o
	o.Kodomo[kodomo.Name] = kodomo
}

func (o *OkasanScraper) deleteKodomo(kodomo string) {
	o.Kodomo[kodomo].ScrapeStop.Stop()
	o.Kodomo[kodomo] = nil
	delete(o.Kodomo, kodomo)
}
