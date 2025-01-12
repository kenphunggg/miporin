package yukari

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Each [KodomoScheduler] keep track on each ksvc,
// If a ksvc want to create new pod, it send info to [KodomoScheduler] to hold the data on [Decision] variable
type KodomoScheduler struct {
	Name         string
	Decision     map[string]int32
	Cus          map[string]int32
	window       int32
	sleepTime    int8
	Okasan       *OkasanScheduler
	ScheduleStop *StopChan
	AuTarget     int
}

type StopChan struct {
	Kodomo chan bool
	Okasan chan bool
}

func NewKodomoScheduler(
	name string, sleepTime int8,
) *KodomoScheduler {
	atarashiiKodomoScheduler := &KodomoScheduler{
		Name:         name,
		sleepTime:    sleepTime,
		Decision:     map[string]int32{},
		ScheduleStop: NewStopChan(),
	}

	// Initialize value for decision on node to 0
	for _, nodename := range NODENAMES {
		atarashiiKodomoScheduler.Decision[nodename] = int32(0)
	}

	auTarget, _ := autoscalingTarget(atarashiiKodomoScheduler)
	atarashiiKodomoScheduler.AuTarget = bonalib.Str2Int(auTarget)

	go atarashiiKodomoScheduler.schedule()

	go atarashiiKodomoScheduler.scrapePodAutoScaling()

	return atarashiiKodomoScheduler
}

func NewStopChan() *StopChan {
	newStopChan := &StopChan{
		Kodomo: make(chan bool),
		Okasan: make(chan bool),
	}
	return newStopChan
}

func (s *StopChan) Stop() {
	s.Kodomo <- true
	s.Okasan <- true
}

func (k *KodomoScheduler) schedule() {
	for {
		select {
		case <-k.ScheduleStop.Kodomo:
			return
		default:
			// k.Decision = k.Okasan.KPADecision[k.Name]
			k.Cus = k.Okasan.KPACus[k.Name]
			time.Sleep(time.Duration(k.sleepTime) * time.Second)
		}
	}
}

func (k *KodomoScheduler) scrapePodAutoScaling() {
	for {
		select {
		case <-k.ScheduleStop.Kodomo:
			bonalib.Log("STOP SCRAPE POD AUTOSCALING", k.Name)
			time.Sleep(time.Duration(k.sleepTime) * time.Second)
			return
		default:
			// RETRIEVE VALUE FROM OKASAN STRUCT
			// [o.Name] is OkasanScheduler.Name which is okaasan
			// kodomoRespt := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Metrics.Respt
			kodomoRespt := OKASAN_SCRAPERS[k.Okasan.Name].Kodomo[k.Name].Metrics.Respt
			kodomoKPAcus := k.Cus

			if kodomoKPAcus == nil || kodomoRespt == nil {
				time.Sleep(time.Duration(k.sleepTime) * time.Second)
				continue
			}

			// INITIALIZE MATRIX TO DEVIDE NODES INTO REGIONS
			// * Initialize region map *
			// Sample value: {node1: 1, node2: 2, node3: 1}
			// It means node 1 and 2 are placed in region 1 and region2 have node2
			regionMap := map[string]int32{}
			for _, nodename := range NODENAMES {
				regionMap[nodename] = -1
			}
			// Insert value retrieved from Okasan.Latency
			for i := 0; i < len(NODENAMES); i++ {
				if regionMap[NODENAMES[i]] == -1 {
					regionMap[NODENAMES[i]] = int32(i)
					for j := 0; j < len(NODENAMES); j++ {
						if j != i && kodomoRespt[i][j]-kodomoRespt[i][i] <= 10 {
							regionMap[NODENAMES[j]] = int32(i)
						}
					}
				} else {
					continue
				}
			}

			// Create a new map to store nodes by region
			// Sample value:
			// {Region 1: {node1, node3}; Region 2: {node2}}
			nodesByRegion := make(map[int32][]string)
			// Iterate over the region map and populate nodesByRegion
			for node, region := range regionMap {
				nodesByRegion[region] = append(nodesByRegion[region], node)
			}

			// * Get total pod needed on each region
			// Initialize a map that store request in each region
			requestFromRegion := map[int32]int32{}
			for regionId, _ := range nodesByRegion {
				requestFromRegion[regionId] = 0
			}
			// Store total request from each region
			for regionId, nodes := range nodesByRegion {
				for _, node := range nodes {
					for nodename, requests := range kodomoKPAcus {
						if nodename == node {
							requestFromRegion[regionId] += requests
						}
					}
				}
			}

			// Initialize total number of pod needed on each region
			target, _ := autoscalingTarget(k)
			kn_au_target := bonalib.Str2Int(target)
			desiredPodInRegion := map[int32]int32{}
			for regionId, _ := range nodesByRegion {
				desiredPodInRegion[regionId] = 0
			}
			// Store total number of pod eeded for each region
			for regionId, requests := range requestFromRegion {
				// desiredPodInRegion[regionId] = requests / int32(kn_au_target)
				desiredPodInRegion[regionId] = int32(ceilDivide(int(requests), kn_au_target))
			}

			// Initialize desiredPods on each node
			desiredPods := map[string]int32{}
			for _, node := range NODENAMES {
				desiredPods[node] = 0
			}

			nodeCountInRegion := make(map[int32]int32)
			for _, region := range regionMap {
				nodeCountInRegion[region]++
			}

			for regionID, pods := range desiredPodInRegion {
				for regID, nodeCount := range nodeCountInRegion {
					if regID == regionID {
						podCount := pods / nodeCount
						podBonus := pods % nodeCount
						for nodeIdx := 0; nodeIdx < len(NODENAMES); nodeIdx++ {
							for name, rID := range regionMap {
								if rID == regionID && name == NODENAMES[nodeIdx] {
									desiredPods[name] += podCount
									if podBonus != 0 {
										desiredPods[name]++
										podBonus--
									}
								}
							}
						}
					}
				}
			}

			k.Decision = desiredPods

			// bonalib.Log("k.Decision", k.Decision)

			time.Sleep(time.Duration(k.sleepTime) * time.Second)
		}
	}

}

func autoscalingTarget(kodomo *KodomoScheduler) (string, error) {
	ksvcGVR := schema.GroupVersionResource{
		Group:    "serving.knative.dev",
		Version:  "v1",
		Resource: "services",
	}

	ksvcDetails, err := DYNCLIENT.Resource(ksvcGVR).Namespace("default").Get(context.TODO(), kodomo.Name, metav1.GetOptions{})
	if err != nil {
		bonalib.Warn("Error listing Knative services", err)
	}

	annotations := ksvcDetails.GetAnnotations()
	lastAppliedConfigJSON, ok := annotations["kubectl.kubernetes.io/last-applied-configuration"]
	if !ok {
		return "", fmt.Errorf("annotation 'kubectl.kubernetes.io/last-applied-configuration' not found")
	}

	var config interface{}
	err = json.Unmarshal([]byte(lastAppliedConfigJSON), &config)
	if err != nil {
		return "", fmt.Errorf("error parsing JSON: %v", err)
	}

	target, err := getTargetAnnotation(config)
	if err != nil {
		bonalib.Warn("Error finding target annotation:", err)
		return "", fmt.Errorf("error finding target annotation: %v", err)
	}

	return target, nil
}
