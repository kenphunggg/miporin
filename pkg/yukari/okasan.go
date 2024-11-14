package yukari

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

type OkasanScheduler struct {
	Name        string
	sleepTime   int8
	Kodomo      map[string]*KodomoScheduler
	MaxPoN      map[string]int32
	KPADecision map[string]map[string]int32 // This value is retrieved by executing [OkasanScheduler.scrapeKPA]
}

func NewOkasanScheduler(
	name string,
	sleepTime int8,
) *OkasanScheduler {

	atarashiiOkasanScheduler := &OkasanScheduler{
		Name:        name,
		sleepTime:   sleepTime,
		Kodomo:      map[string]*KodomoScheduler{},
		MaxPoN:      map[string]int32{},
		KPADecision: map[string]map[string]int32{},
	}
	atarashiiOkasanScheduler.init()

	go atarashiiOkasanScheduler.scrapeKPA() // Scrape "Knative Pod Autoscaler"

	go atarashiiOkasanScheduler.watchKsvcCreateEvent()

	return atarashiiOkasanScheduler
}

// Create new [KodomoScheduler] for each ksvc
func (o *OkasanScheduler) init() {
	// Get all knative service
	ksvcGVR := schema.GroupVersionResource{
		Group:    "serving.knative.dev",
		Version:  "v1",
		Resource: "services",
	}
	// List all ksvc in default namespace and hold the value in [ksvcList]
	ksvcList, err := DYNCLIENT.Resource(ksvcGVR).Namespace("default").List(context.TODO(), v1.ListOptions{})
	if err != nil {
		bonalib.Warn("Error listing Knative services:", err)
	}
	// For each ksvc, create a new [Kodomo]
	for _, ksvc := range ksvcList.Items {
		ksvcName := ksvc.GetName()
		child := NewKodomoScheduler(ksvcName, o.sleepTime)
		o.addKodomo(child)
	}
}

// This function make a http request to [Knative Autoscaler] to retrieve its information
func (o *OkasanScheduler) scrapeKPA() {
	// Create a nested map
	// Example:
	// decideInNode["node1"] = map[string]int32{
	// "ksvc1": 2,
	// "ksvc2": 8,
	// }
	decideInNode := map[string]map[string]int32{}
	for {
		// This return number of pod needed on each node
		// {"hello":{"cloud-node":1,"edge-node":3,"master-node":0}}
		// This mean that
		// 1 pod on cloud-node
		// 3 pods on edge-node
		// 0 pod on master-node
		response, err := http.Get("http://autoscaler.knative-serving.svc.cluster.local:9999/metrics/kservices")
		if err != nil {
			bonalib.Warn("Error in calling to Kn-Au")
			time.Sleep(5 * time.Second)
			continue
		}
		// If there is no err: retrieve value from [autoscaler] and hold in [decideInNode]
		if err := json.NewDecoder(response.Body).Decode(&decideInNode); err != nil {
			// map[hello:map[cloud-node:0 edge-node:0 master-node:0]]
			bonalib.Warn("Failed to decode JSON: ", err)
			continue
		}
		response.Body.Close()

		// [Okasan.KPADecision] will hold the value of [decideInNode]
		o.KPADecision = decideInNode

		time.Sleep(time.Duration(o.sleepTime) * time.Second)
	}
}

// Get value from [KodomoScheduler] to execute [schedule]
func (o *OkasanScheduler) schedule(kodomo *KodomoScheduler) {
	decideInNode := map[string]int32{}
	currentDesiredPods := map[string]int32{}
	newDesiredPods := map[string]int32{}
	deltaDesiredPods := map[string]int32{}
	noChanges := map[string]int32{} // noChanges is a const, equal [0, 0, 0]

	// Initial value for all node in cluster
	for _, nodename := range NODENAMES {
		currentDesiredPods[nodename] = 0
		newDesiredPods[nodename] = 0
		deltaDesiredPods[nodename] = 0
		noChanges[nodename] = 0
	}
	firstTime := true
	var minResponseTime, minIdx int32

	// Convert nodename into number
	nodeidx := map[string]int{}
	for i, nodename := range NODENAMES {
		nodeidx[nodename] = i
	}

	for {
		select {
		case <-kodomo.ScheduleStop.Okasan:
			return
		default:
			// [KodomoScheduler.Decision] equal [Okasan.KPADecision] which is scrapped from [Knative Autoscaler]
			// It will return the number of pod required on each node
			decideInNode = kodomo.Decision

			// Initial number of pod on each node
			if firstTime {
				currentDesiredPods = decideInNode
				firstTime = false
			} else {
				newDesiredPods = decideInNode
			}

			// Calculate the differences on number of pods on each node
			for k_cdp := range currentDesiredPods {
				deltaDesiredPods[k_cdp] = newDesiredPods[k_cdp] - currentDesiredPods[k_cdp]
			}

			for _, v_dpp := range deltaDesiredPods {
				if v_dpp != 0 { // if have any change in delta, break and go to following steps
					break
				}
			}

			if reflect.DeepEqual(deltaDesiredPods, noChanges) { // if no change, sleep and continue
				time.Sleep(time.Duration(o.sleepTime) * time.Second)
				continue
			}

			// k_ddp: nodename
			// v_dpp: number of pod needed
			for k_ddp, v_ddp := range deltaDesiredPods { // Check for every node that have the change in number of pod
				for i := v_ddp; i != 0; {
					if i < 0 { // If number of pod on that node decrease
						currentDesiredPods[k_ddp]-- // Decrease the [currentDesiredPods] by 1
						i++                         // Continue decrease until reach 0
					}
					if i > 0 { // If number of pod on that node increase
						minResponseTime = int32(1000000) // Set min response time to extreme high
						minIdx = -1                      // Set min index to -1

						// Get response time for traffic from node i to node j
						// It is the matric like this
						// Respt [i][j]int32
						responseTime := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Metrics.Respt

						// nodeidx[k_ddp]: convert nodename into number
						// responseTime[nodeidx[k_ddp]]: response time for traffic from node [k_ddp]
						// loop each row of RESPONSETIME - loop for the response time of trafficfrom node [k_ddp] to all nodes
						for i_rpt := range responseTime[nodeidx[k_ddp]] {
							// If [currentDesiredPods] in node [k_ddp] is not fewer than max pod in node [k_ddp]
							// MAXPOD is set to []int{10, 10, 3} [in yukari/main.go]
							if currentDesiredPods[k_ddp] >= int32(MAXPON[nodeidx[k_ddp]]) {
								continue
							} else {
								// If response time from [k_ddp] to [i_rpt] lower than minimum response time
								// This will always true cause minResponseTime is set to 1000000
								if responseTime[nodeidx[k_ddp]][i_rpt] < minResponseTime {
									minResponseTime = responseTime[nodeidx[k_ddp]][i_rpt] // Update minResponseTime
									minIdx = int32(i_rpt)                                 // it is served on node [i_rpt]
								}
							}
						}
						// This ensure that the system will not scale pod reach out of maximimum pod in node that have been set
						if minIdx != -1 {
							currentDesiredPods[NODENAMES[minIdx]]++ // Update current desired pod
						}
						i--
					}
				}
			}

			bonalib.Log("currentDesiredPods", o.Name, currentDesiredPods)
			o.patchSchedule(currentDesiredPods)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)
		}
	}
}

func (o *OkasanScheduler) patchSchedule(desiredPods map[string]int32) {
	gvr := schema.GroupVersionResource{
		Group:    "batch.bonavadeur.io",
		Version:  "v1",
		Resource: "seikas",
	}

	// Define the patch data using the input
	repurika := map[string]interface{}{}
	for _, nodename := range NODENAMES {
		repurika[nodename] = desiredPods[nodename]
	}

	// Prepare before convert to JSON
	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"repurika": repurika,
		},
	}

	// Convert patch data to JSON
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		fmt.Printf("Error marshalling patch data: %v", err)
	}

	// Namespace and resource name
	namespace := "default"
	resourceName := "hello"

	// Execute the patch request
	patchedResource, err := DYNCLIENT.Resource(gvr).
		Namespace(namespace).
		Patch(context.TODO(), resourceName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		bonalib.Warn("Error patching resource: ", err)
	} else {
		resource, found, _ := unstructured.NestedString(patchedResource.Object, "metadata", "name")
		if !found {
			bonalib.Warn("Seika not found:", err)
		}
		fmt.Println("Patched resource:", resource)
	}
}

func (o *OkasanScheduler) watchKsvcCreateEvent() {
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

	for event := range watcher.ResultChan() {
		ksvc, _ := event.Object.(*unstructured.Unstructured)
		ksvcName, _, _ := unstructured.NestedString(ksvc.Object, "metadata", "name")
		if event.Type == watch.Added {
			bonalib.Warn("Ksvc has been created:", ksvcName)
			// create apropriate Seika
			createSeika(ksvcName)
			// create apropriate KodomoScheduler
			child := NewKodomoScheduler(ksvcName, o.sleepTime)
			o.addKodomo(child)
			bonalib.Warn("Ksvc has been created: end", ksvcName)
		}
		if event.Type == watch.Deleted {
			bonalib.Warn("Ksvc has been deleted:", ksvcName)
			// delete apropriate Seika
			deleteSeika(ksvcName)
			// delete apropriate KodomoScheduler
			o.deleteKodomo(ksvcName)
			bonalib.Warn("Ksvc has been deleted: end", ksvcName)
		}
	}
}

func (o *OkasanScheduler) addKodomo(kodomo *KodomoScheduler) {
	kodomo.Okasan = o
	o.Kodomo[kodomo.Name] = kodomo
	go o.schedule(kodomo)
}

func (o *OkasanScheduler) deleteKodomo(kodomo string) {
	o.Kodomo[kodomo].ScheduleStop.Stop()
	o.Kodomo[kodomo] = nil
	delete(o.Kodomo, kodomo)
}
