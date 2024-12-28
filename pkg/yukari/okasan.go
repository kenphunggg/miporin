package yukari

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"reflect"
	"strings"
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
	KPACus            map[string]map[string]int32
	coldStartTime     map[string]map[string][]float64 // (ms), ksvc: node1: {15ms, 45ms} for example
	communicationCost float64
	KsvcList          []string
	// switchingCost     float64
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

	go atarashiiOkasanScheduler.scrapeKPACus() // Scrape "Knative Pod Autoscaler"

	go atarashiiOkasanScheduler.watchKsvcCreateEvent()

	go atarashiiOkasanScheduler.getColdStartTime()

	go atarashiiOkasanScheduler.getKsvcList()

	// go atarashiiOkasanScheduler.getSwitchingCost()

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

	// Initialize data for o.coldStartTime
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

		time.Sleep(time.Duration(o.sleepTime) * time.Second)
	}
}

func (o *OkasanScheduler) scrapeKPACus() {
	// {"hello":{"cloud-node":1,"edge-node":3,"master-node":0}}
	// This mean that
	// 1 pod on cloud-node
	// 3 pods on edge-node
	// 0 pod on master-node
	tempCusInNode := map[string]map[string]float64{}
	cusInNode := map[string]map[string]int32{}
	for {
		response, err := http.Get("http://autoscaler.knative-serving.svc.cluster.local:9999/metrics/kservices-cus")
		if err != nil {
			bonalib.Warn("Error in calling to Kn-Au")
			time.Sleep(5 * time.Second)
			continue
		}
		// If there is no err: retrieve value from [autoscaler] and hold in [decideInNode]
		if err := json.NewDecoder(response.Body).Decode(&tempCusInNode); err != nil {
			// map[hello:map[cloud-node:0 edge-node:0 master-node:0]]
			bonalib.Warn("Failed to decode JSON: ", err)
			continue
		}
		response.Body.Close()

		for ksvc, nodes := range tempCusInNode {
			cusInNode[ksvc] = map[string]int32{}
			for node, cusUser := range nodes {
				cusInNode[ksvc][node] = int32(math.Ceil(cusUser))
			}
		}

		// [Okasan.KPADecision] will hold the value of [decideInNode]
		o.KPACus = cusInNode

		bonalib.Log("o.KPACus", o.KPACus)

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
			// o.algorithm(currentDesiredPods, kodomo)

			// bonalib.Log("updatedCurrentDesiredPods", o.Name, currentDesiredPods)
			o.patchSchedule(currentDesiredPods)
			// o.testSeika(currentDesiredPods)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)
		}
	}
}

func (o *OkasanScheduler) newSchedule(kodomo *KodomoScheduler) {
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
	// var minResponseTime, minIdx int32

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

			// o.algorithm(deltaDesiredPods, kodomo)

			for nodeName, pods := range deltaDesiredPods {
				// Update currentDesired pods
				for podNumber := pods; podNumber != 0; {
					if podNumber < 0 {
						currentDesiredPods[nodeName]--
						podNumber++
					}
					if podNumber > 0 {
						currentDesiredPods[nodeName]++
						podNumber--
					}
				}
			}

			bonalib.Log("currentDesiredPods", o.Name, currentDesiredPods)

			o.patchSchedule(currentDesiredPods)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)

		}
	}
}

// ------<>------START EXTENSION------<>------

// Get latency when creating a pod (cold start time)
func (o *OkasanScheduler) getColdStartTime() {
	var (
		podWatching []string
		// podModify   []string
	)

	// o.switchingCost = 0
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

		// Get Cold Start Time
		for _, ownerRef := range pod.OwnerReferences {
			if ownerRef.Kind == "Seika" {
				// Calculate Switching time when new pod is created
				if pod.Status.Phase == corev1.PodPending {
					if !contains(podWatching, pod.Name) {
						// bonalib.Log("A new pod is going to create:", pod.Name)
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
								// bonalib.Log("A new pod is created:", pod.Name)
								endTime[pod.Name] = time.Now()
								coldStartTime := float64(endTime[pod.Name].Sub(startTime[pod.Name]).Seconds()) * 1000 // in miliseconds
								podWatching = removeValue(podWatching, pod.Name)
								nodeName := pod.Spec.NodeName
								// ksvc := pod.Labels["app"]
								ksvc := pod.Labels["bonavadeur.io/seika"]

								if o.coldStartTime == nil {
									// Initialize the outer map if it's nil
									o.coldStartTime = make(map[string]map[string][]float64)
								}
								if o.coldStartTime[ksvc] == nil {
									// Initialize the inner map if it's nil
									o.coldStartTime[ksvc] = make(map[string][]float64)
								}

								o.coldStartTime[ksvc][nodeName] = append(o.coldStartTime[ksvc][nodeName], coldStartTime)

								if len(o.coldStartTime[ksvc][nodeName]) >= 100 {
									o.coldStartTime[ksvc][nodeName] = o.coldStartTime[ksvc][nodeName][1:]
								}

								// o.switchingCost += calculateAverage(o.coldStartTime[ksvc][nodeName])

								// bonalib.Log("Coldstart", o.coldStartTime)
								// bonalib.Log("SwitchingCost", o.switchingCost)
							}
						}
					}
				}

				startTime, podWatching = autoExpire(startTime, podWatching, 100) // After 100s, delete the pod that are watched to look for calculate switching cost

				// if event.Type == "DELETED" {
				// 	minusSwitchingCost := calculateAverage(o.coldStartTime[pod.Labels["bonavadeur.io/seika"]][pod.Spec.NodeName])
				// 	o.switchingCost -= minusSwitchingCost
				// 	// bonalib.Log("SwitchingCost", o.switchingCost)
				// }
			}
		}
	}
}

// Get total cost caused by latency between nodes
func (o *OkasanScheduler) getCommunicationCost(kodomo *KodomoScheduler) {
	status := make(chan bool)
	currentPodInNode := make(map[string]int8)
	serCusMatrix := make([][]float64, len(NODENAMES))
	for i := range serCusMatrix {
		serCusMatrix[i] = make([]float64, len(NODENAMES))
	}

	for {
		select {
		case <-status:
			return
		default:
			// Get latency between nodes
			latencyBetweenNodes := OKASAN_SCRAPERS[o.Name].Latency
			weightMatrix := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Weight

			// Get total number of pods on each node
			// currentPodInNode := make(map[string]int8)
			for _, nodename := range NODENAMES {
				currentPodInNode[nodename] = 0
			}

			// List all pods controlled by Seika and insert into ksvcMap
			pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), v1.ListOptions{})
			if err != nil {
				log.Fatalf("Error listing pods: %s", err)
			} // Filter and print pods controlled by Seika

			for _, pod := range pods.Items {
				if strings.Contains(pod.Name, kodomo.Name) {
					// bonalib.Log("Pod Name:", pod.Name)
					// bonalib.Log("Node Name:", pod.Spec.NodeName)
					nodename := pod.Spec.NodeName
					currentPodInNode[nodename]++
				}
			}

			// Get communication cost
			cost := float64(0)
			// for i := range latencyBetweenNodes {
			// 	for j := range latencyBetweenNodes[i] {
			// 		if j == i {
			// 			continue
			// 		}
			// 		// Get total pods distributed to node i and j
			// 		for ksvc := range ksvcMap {
			// 			totalPods := ksvcMap[ksvc][NODENAMES[i]] + ksvcMap[ksvc][NODENAMES[j]]
			// 			cost += (float64(latencyBetweenNodes[i][j]) * float64(totalPods))
			// 		}
			// 	}
			// }

			// Create serving cus matrix
			// serCusMatrix[i][j] means x cus from node i will be served at node j
			// serCusMatrix := [][]float64{}
			for i := range weightMatrix {
				for j := range weightMatrix[i] {
					k := int(0)
					if k != i {
						continue
					}
					if kodomo.Cus == nil {
						time.Sleep(time.Duration(o.sleepTime) * time.Second)
						continue
					}
					for _, cus := range kodomo.Cus {
						percentage := float64(weightMatrix[i][j]) / float64(100)
						serCusMatrix[i][j] = float64(cus) * percentage
						k++
					}
				}
			}
			bonalib.Log("serCusMatrix", serCusMatrix)
			// cost = cost / 2

			// Calculate communication cost
			for i := range weightMatrix {
				for j := range weightMatrix[i] {
					if j == i {
						continue
					}
					cost += float64(latencyBetweenNodes[i][j]) * (float64(currentPodInNode[NODENAMES[i]]) + float64(currentPodInNode[NODENAMES[j]]))
				}
			}
			bonalib.Log("cost", cost)

			// o.communicationCost = float64(cost)
			bonalib.Log("Weight  matrix", OKASAN_SCRAPERS["okaasan"].Kodomo[kodomo.Name].Weight)

			// bonalib.Log("latency", latencyBetweenNodes)
			// bonalib.Log("currentPodInNode", currentPodInNode)
			// bonalib.Log("CommunicationCost", o.communicationCost)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)

			// bonalib.Log("KPA cus", o.KPACus)

		}
	}
}

func (o *OkasanScheduler) algorithm(deltaDesiredPods map[string]int32, kodomo *KodomoScheduler) map[string]int32 {
	nodeidx := make(map[string]int)
	for idx, name := range NODENAMES {
		nodeidx[name] = idx
	}

	for nodename, pods := range deltaDesiredPods {
		if pods > 0 {
			var (
				bonusSwCost  float64 // Cold start
				newSwCost    float64 // Cold start
				bonusComCost float64 // Latency between nodes
				newComCost   float64 // Latency between nodes
			)
			// Calculate bonus switching cost (cold start)
			if o.coldStartTime == nil {
				bonusSwCost = 0
			}
			bonusSwCost = calculateAverage(o.coldStartTime[kodomo.Name][nodename])
			// newSwCost = o.switchingCost + bonusSwCost
			newSwCost = bonusSwCost

			// Calculate new communication cost (latency between nodes)
			latencyBetweenNodes := OKASAN_SCRAPERS[o.Name].Latency
			bonusComCost = 0
			for i := range latencyBetweenNodes {
				bonusComCost += float64(latencyBetweenNodes[nodeidx[nodename]][i])
			}
			newComCost = bonusComCost * float64(deltaDesiredPods[nodename])
			// newComCost = o.communicationCost + bonusComCost*float64(deltaDesiredPods[nodename])

			// Decide when to create new pod
			for podNumber := pods; podNumber != 0; {
				if newSwCost > newComCost { // Do not create new pod
					newComCost -= bonusComCost
					deltaDesiredPods[nodename]--
					podNumber--
				}
				if newSwCost < newComCost { // Create new pod
					break
				}
			}
		}
	}
	return deltaDesiredPods
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
	go o.newSchedule(kodomo)
	go o.getCommunicationCost(kodomo)

	// go o.schedule(kodomo)
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
