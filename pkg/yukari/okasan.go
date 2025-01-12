package yukari

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
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
	KPACus            map[string]map[string]int32
	coldStartTime     map[string]map[string][]float64 // (ms), ksvc: node1: {15ms, 45ms} for example
	communicationCost float64
	KsvcList          []string
	ksvcMap           map[string]map[string]int8
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

	go atarashiiOkasanScheduler.getKsvcMap()

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

		// bonalib.Log("o.KPACus", o.KPACus)

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
			o.patchSchedule(kodomo, currentDesiredPods)
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

	// o.switchingCost := 0

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

			// Cross-Edge algorithm
			o.algorithm(kodomo, currentDesiredPods, deltaDesiredPods)

			for node, pods := range deltaDesiredPods {
				for currentPods := pods; currentPods != 0; {
					if currentPods < 0 {
						currentDesiredPods[node]--
						currentPods++
					}
					if currentPods > 0 {
						if currentDesiredPods[node] >= int32(MAXPON[nodeidx[node]]) {
							continue
						}
						currentDesiredPods[node]++
						currentPods--
					}
				}
			}
			// currentDesiredPods["cloud-node"] = 1

			o.patchSchedule(kodomo, currentDesiredPods)

			// bonalib.Log("deltaDesiredPods", deltaDesiredPods)
			bonalib.Log("currentDesiredPods", currentDesiredPods)

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
	for {
		select {
		case <-kodomo.ScheduleStop.Okasan:
			bonalib.Log("STOP")
			time.Sleep(time.Duration(o.sleepTime*2) * time.Second)
			return
		default:
			latencyBetweenNodes := OKASAN_SCRAPERS[o.Name].Latency
			weightMatrix := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Weight
			cus := o.KPACus[kodomo.Name]
			if latencyBetweenNodes == nil || weightMatrix == nil || cus == nil {
				time.Sleep(time.Duration(o.sleepTime) * time.Second)
				continue
			}

			nodeIdx := make(map[string]int, len(NODENAMES))
			// Map each node name to its index
			for idx, node := range NODENAMES {
				nodeIdx[node] = idx // Mapping node name to its index
			}
			// Get communication cost
			cost := float32(0)
			for source, cusInSource := range cus {
				for des := range weightMatrix[nodeIdx[source]] {
					if float32(cusInSource)*float32(weightMatrix[nodeIdx[source]][des])/100 != 0 && latencyBetweenNodes[nodeIdx[source]][des] > 30 {
						totalPods := o.ksvcMap[kodomo.Name][NODENAMES[des]]
						cost += (float32(latencyBetweenNodes[nodeIdx[source]][des]) * float32(totalPods))
						// bonalib.Log("Traffic is not serve in right region", cost)
						// bonalib.Log("source", source)
						// bonalib.Log("des", NODENAMES[des])
						// bonalib.Log("latencyBetweenNodes", latencyBetweenNodes[nodeIdx[source]][des])
						// bonalib.Log("weightMatrix[nodeIdx[source]][des]", weightMatrix[nodeIdx[source]][des])
					}
				}
			}
			o.communicationCost = float64(cost)
			bonalib.Log("CommunicationCost", o.communicationCost)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)
		}
	}
}

func (o *OkasanScheduler) algorithm(kodomo *KodomoScheduler, currentDesiredPods map[string]int32, deltaDesiredPods map[string]int32) map[string]int32 {
	// Calculate current total pods
	currentTotalPods := int32(0)
	for _, node := range NODENAMES {
		currentTotalPods += currentDesiredPods[node]
	}

	// Modify [deltaDesiredPods] with algorith
	for _, node := range NODENAMES {
		if deltaDesiredPods[node] <= 0 {
			currentTotalPods += deltaDesiredPods[node]
			continue
		}
		if deltaDesiredPods[node] > 0 {
			// CALCULATE NEW COMMUNICATION COST
			latencyBetweenNodes := OKASAN_SCRAPERS[o.Name].Latency
			currentPodOnNode := o.ksvcMap[kodomo.Name]

			bonusCommunicationCost := float64(0)
			for source := range latencyBetweenNodes {
				if NODENAMES[source] == node {
					for des := range latencyBetweenNodes[source] {
						bonusCommunicationCost += (float64(currentPodOnNode[NODENAMES[des]]) * float64(latencyBetweenNodes[source][des]))
					}
				}
			}
			// bonalib.Log("Bonus Communication Cost", bonusCommunicationCost)

			// CALCULATE NEW SWITCHING COST
			coldStartTime := calculateAverage(o.coldStartTime[kodomo.Name][node])
			// bonalib.Log("Bonus Switching Cost", bonusSwitchingCost)

			// Calculate minimum total number of pods
			totalCus := int32(0)
			for _, node := range NODENAMES {
				totalCus += o.KPACus[kodomo.Name][node]
			}
			minPods := ceilDivide(int(totalCus), kodomo.AuTarget)
			bonalib.Log("minPods", minPods)

			// DECIDE TO CREATE NEW POD
			for currentPods := deltaDesiredPods[node]; currentPods != 0; {
				bonusSwitchingCost := coldStartTime * float64(deltaDesiredPods[node])
				bonalib.Log("bonusSwitchingCost", bonusSwitchingCost)

				// Decide whether to create new pod or not
				if bonusSwitchingCost > bonusCommunicationCost+o.communicationCost {
					if currentTotalPods <= int32(minPods) {
						bonalib.Log("Exceed min pod", currentTotalPods)
						break
					}
					currentPods--
					deltaDesiredPods[node]--
				} else {
					currentTotalPods += currentPods
					break
				}
			}

		}
	}
	return deltaDesiredPods
}

// ------<>------END EXTENSION------<>------

func (o *OkasanScheduler) patchSchedule(kodomo *KodomoScheduler, desiredPods map[string]int32) {
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
	resourceName := kodomo.Name

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
	time.Sleep(time.Duration(o.sleepTime) * time.Second)
	delete(o.Kodomo, kodomo)
}

func (o *OkasanScheduler) getKsvcList() {
	o.ksvcMap = make(map[string]map[string]int8)
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
				o.ksvcMap[ksvc.GetName()] = make(map[string]int8)
				for _, node := range NODENAMES {
					// o.ksvcMap[ksvc.GetName()] = make(map[string]int8)
					o.ksvcMap[ksvc.GetName()][node] = int8(0)
				}
			}
		}
		time.Sleep(time.Duration(o.sleepTime) * time.Second)
	}
}

func (o *OkasanScheduler) getKsvcMap() {
	// List all pods controlled by Seika and insert into ksvcMap

	for {
		for _, ksvc := range o.KsvcList {
			labelSelector := "serving.knative.dev/service=" + ksvc
			pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), v1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				log.Fatalf("Error listing pods: %s", err)
			}

			for _, node := range NODENAMES {
				o.ksvcMap[ksvc][node] = 0
			}
			for _, pod := range pods.Items {
				for _, node := range NODENAMES {
					if pod.Spec.NodeName == node {
						o.ksvcMap[ksvc][node]++
					}
				}
			}
		}

		// bonalib.Log("ksvcMap", o.ksvcMap)
		time.Sleep(time.Duration(o.sleepTime) * time.Second)
	}

}
