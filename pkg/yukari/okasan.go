package yukari

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

type OkasanScheduler struct {
	Name              string
	sleepTime         int8
	Kodomo            map[string]*KodomoScheduler
	MaxPoN            map[string]int32
	KPADecision       map[string]map[string]int32 // This value is retrieved by executing [OkasanScheduler.scrapeKPA]
	coldStartTime     map[string]float64          // (ms), ksvc:15ms for example
	communicationCost float64
	KsvcList          []string
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

	go atarashiiOkasanScheduler.getColdStartTime()

	go atarashiiOkasanScheduler.getKsvcList()

	go atarashiiOkasanScheduler.getCommunicationCost()

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

			// bonalib.Log("currentDesiredPods", o.Name, currentDesiredPods)
			o.algorithm(currentDesiredPods, kodomo)
			// bonalib.Log("updatedCurrentDesiredPods", o.Name, currentDesiredPods)
			o.patchSchedule(currentDesiredPods)
			// o.testSeika(currentDesiredPods)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)
		}
	}
}

// ------<>------START EXTENSION------<>------
func (o *OkasanScheduler) algorithm(desiredPods map[string]int32, kodomo *KodomoScheduler) {
	// [o.Name] is OkasanScheduler.Name which is okaasan
	//
	kodomoLatency := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Okasan.Latency
	// kodomoLatency := OKASAN_SCRAPERS[o.Name].Latency

	// Create region map
	regionMap := map[string]int32{}
	for _, nodename := range NODENAMES {
		regionMap[nodename] = -1
	}
	for i := 0; i < len(NODENAMES); i++ {
		if regionMap[NODENAMES[i]] == -1 {
			regionMap[NODENAMES[i]] = int32(i)
			for j := 0; j < len(NODENAMES); j++ {
				if j != i && kodomoLatency[i][j]-kodomoLatency[i][i] <= 10 {
					regionMap[NODENAMES[j]] = int32(i)
				}
			}
		} else {
			continue
		}
	}

	// Create a new map to store nodes by region
	nodesByRegion := make(map[int32][]string)

	// Iterate over the region map and populate nodesByRegion
	for node, region := range regionMap {
		nodesByRegion[region] = append(nodesByRegion[region], node)
	}

	// * Get total pod needed on each region
	// Create a map that store pod needed on each region
	regionDesiredPods := map[int32]int32{}
	for regionId, _ := range nodesByRegion {
		regionDesiredPods[regionId] = 0
	}

	// Store total number of pod needed on each region
	for regionId, nodes := range nodesByRegion {
		for _, node := range nodes {
			for nodename, pods := range desiredPods {
				if nodename == node {
					regionDesiredPods[regionId] += pods
				}
			}
		}
	}

	// * Change regionDesiredPods needed on each region (algorithm)
	// Create newRegionDesiredPods
	newRegionDesiredPods := map[int32]int32{}
	for regionId, _ := range nodesByRegion {
		newRegionDesiredPods[regionId] = 0
	}
	// Adjust value on this region (algoorithm)
	for regionId, _ := range nodesByRegion {
		if regionId+1 < int32(len(nodesByRegion)) {
			newRegionDesiredPods[regionId+1] = regionDesiredPods[regionId]
		} else {
			newRegionDesiredPods[0] = regionDesiredPods[int32(len(nodesByRegion))-1]
		}

	}

	// Create newDesiredPods
	newDesiredPods := map[string]int32{}
	for _, nodename := range NODENAMES {
		newDesiredPods[nodename] = 0
	}

	// Generate data for newDesiredPods
	for regionId, pods := range newRegionDesiredPods {
		if pods != 0 { // Find the region that need pod to open
			for id, nodes := range nodesByRegion { // Find all pods on the region to open pod
				if id == regionId {
					nodeCount := len(nodes)
					podsPerNode := pods / int32(nodeCount)
					extraPods := pods % int32(nodeCount)

					for _, node := range nodes {
						newDesiredPods[node] = podsPerNode
					}

					for i := 0; i < int(extraPods); i++ {
						newDesiredPods[nodes[i]]++
					}
				}
			}
		}
	}

	// bonalib.Log("newDesiredPods", newDesiredPods)

	// Update data on input(desiredPods) according to newDesiredPods
	for nodename, pods := range newDesiredPods {
		desiredPods[nodename] = pods
	}

}

// Get latency when creating a pod (cold start time)
func (o *OkasanScheduler) getColdStartTime() {
	var (
		podWatching []string
	)

	startTime := make(map[string]time.Time)
	endTime := make(map[string]time.Time)

	watcher, err := CLIENTSET.CoreV1().Pods("default").Watch(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}
	for event := range watcher.ResultChan() {
		pod, ok := event.Object.(*corev1.Pod)
		if !ok {
			fmt.Println("Unexpected object type")
			continue
		}

		// Filter pods owned by Seika
		for _, ownerRef := range pod.OwnerReferences {
			if ownerRef.Kind == "Seika" {
				if pod.Status.Phase == corev1.PodPending {
					if !contains(podWatching, pod.Name) {
						bonalib.Log("A new pod is going to create:", pod.Name)
						podWatching = append(podWatching, pod.Name)
						startTime[pod.Name] = time.Now()
					}
				}

				if pod.Status.Phase == corev1.PodRunning {
					if contains(podWatching, pod.Name) {
						allContainersReady := true
						for _, containerStatus := range pod.Status.ContainerStatuses {
							if !contains(podWatching, pod.Name) {
								break
							}
							if containerStatus.State.Running == nil || !containerStatus.Ready {
								allContainersReady = false
								break
							}
							if allContainersReady {
								bonalib.Log("A new pod is created:", pod.Name)
								endTime[pod.Name] = time.Now()
								coldStartTime := float64(endTime[pod.Name].Sub(startTime[pod.Name]).Seconds())
								bonalib.Log("COLDSTARTTIME", coldStartTime)
								podWatching = removeValue(podWatching, pod.Name)
								bonalib.Log("nodeName", pod.Spec.NodeName)
								bonalib.Log("ksvc", pod.Labels["app"])
							}
						}
					}
				}

				startTime, podWatching = autoExpire(startTime, podWatching, 10)
			}
		}
	}
}

// Get total latency caused by cold start
func (o *OkasanScheduler) getSwitchingCost() {
	// Get total cost caused by cold start
}

// Get total cost caused by latency between nodes
func (o *OkasanScheduler) getCommunicationCost() {
	status := make(chan bool)
	for {
		select {
		case <-status:
			return
		default:
			// Get latency between nodes
			latencyBetweenNodes := OKASAN_SCRAPERS[o.Name].Latency

			// Create a map that store seika information
			ksvcMap := make(map[string]map[string]int8)
			for _, ksvc := range o.KsvcList {
				ksvcMap[ksvc] = make(map[string]int8)
				for _, node := range NODENAMES {
					ksvcMap[ksvc][node] = int8(0)
				}
			}

			// List all pods controlled by Seika and insert into ksvcMap
			pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), v1.ListOptions{})
			if err != nil {
				log.Fatalf("Error listing pods: %s", err)
			} // Filter and print pods controlled by Seika

			for _, pod := range pods.Items {
				for _, owner := range pod.OwnerReferences {
					// Only get pods that controlled by seika
					if owner.Kind == "Seika" {
						for _, ksvc := range o.KsvcList {
							for _, node := range NODENAMES {
								labelSelector := "serving.knative.dev/service=" + ksvc

								desiredPods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), v1.ListOptions{
									LabelSelector: labelSelector,
								})
								if err != nil {
									log.Fatalf("Error listing pods: %s", err)
								}

								podCount := int8(0)
								for _, desiredPod := range desiredPods.Items {
									if desiredPod.Spec.NodeName == node {
										podCount++
									}
								}
								if ksvcMap[ksvc] == nil {
									continue
								} else {
									ksvcMap[ksvc][node] = podCount
								}
							}
						}
					}
				}
			}

			// Get communication cost
			cost := float64(0)
			for i := range latencyBetweenNodes {
				for j := range latencyBetweenNodes[i] {
					if j == i {
						continue
					}
					// Get total pods distributed to node i and j
					for ksvc := range ksvcMap {
						totalPods := ksvcMap[ksvc][NODENAMES[i]] + ksvcMap[ksvc][NODENAMES[j]]
						cost += (float64(latencyBetweenNodes[i][j]) * float64(totalPods))
					}
				}
			}
			cost = cost / 2

			o.communicationCost = float64(cost)

			bonalib.Log("CommunicationCost", o.communicationCost)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)

		}
	}

}

// Get container running cost
func (o *OkasanScheduler) getContainerRunningCost() {
	// Get hardware resource paid for running a container

	// Get total container on that ksvc
}

// ------<>------END EXTENSION------<>------

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

func (o *OkasanScheduler) getKsvcList() {
	for {
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
			if contains(o.KsvcList, ksvc.GetName()) {
				continue
			} else {
				o.KsvcList = append(o.KsvcList, ksvc.GetName())
			}
		}
		time.Sleep(time.Duration(o.sleepTime) * time.Second)
	}
}

func (o *OkasanScheduler) testSeika(desiredPods map[string]int32) {
	gvr := schema.GroupVersionResource{
		Group:    "batch.bonavadeur.io",
		Version:  "v1",
		Resource: "seikas",
	}

	// Define the patch data using the input
	repurika := map[string]interface{}{}
	for _, nodename := range NODENAMES {
		repurika[nodename] = desiredPods[nodename] + 1
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
