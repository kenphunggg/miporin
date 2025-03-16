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
	Name          string
	Decision      map[string]int32
	Cus           map[string]int32
	window        int32
	SleepTime     int8
	Okasan        *OkasanScheduler
	ScheduleStop  *StopChan
	AuTarget      int
	PodMonitor    map[string]*PodMonitor
	PodMonitorMap map[string]int32
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
		SleepTime:    sleepTime,
		Decision:     map[string]int32{},
		ScheduleStop: NewStopChan(),
		PodMonitor:   map[string]*PodMonitor{},
	}

	// Initialize value for decision on node to 0
	for _, nodename := range NODENAMES {
		atarashiiKodomoScheduler.Decision[nodename] = int32(0)
	}

	auTarget, _ := autoscalingTarget(atarashiiKodomoScheduler)
	atarashiiKodomoScheduler.AuTarget = bonalib.Str2Int(auTarget)

	atarashiiKodomoScheduler.PodMonitorMap = make(map[string]int32)
	for _, nodename := range NODENAMES {
		atarashiiKodomoScheduler.PodMonitorMap[nodename] = 0
	}

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
			time.Sleep(time.Duration(k.SleepTime) * time.Second)
			return
		default:
			// k.Decision = k.Okasan.KPADecision[k.Name]
			k.Cus = k.Okasan.KPACus[k.Name]

			// Map holding total of pod needed on each pod
			podmap := make(map[string]int32)
			for _, nodename := range NODENAMES {
				podmap[nodename] = 0
			}
			for _, podstate := range k.PodMonitor {
				if podstate.State.WarmCPU {
					podmap[podstate.NodeName]++
				}
			}
			k.PodMonitorMap = podmap

			time.Sleep(time.Duration(k.SleepTime) * time.Second)
		}
	}
}

func (k *KodomoScheduler) scrapePodAutoScaling() {
	for {
		select {
		case <-k.ScheduleStop.Okasan:
			// bonalib.Log("STOP SCRAPE POD AUTOSCALING", k.Name)
			time.Sleep(time.Duration(k.SleepTime) * time.Second)
			return
		default:
			if k == nil {
				// bonalib.Log("No kodomo found")
				return
			} else {
				time.Sleep(time.Duration(k.SleepTime) * time.Second)

				if OKASAN_SCRAPERS["okaasan"].Kodomo[k.Name] == nil ||
					OKASAN_SCRAPERS["okaasan"].Kodomo[k.Name].Metrics == nil {
					// bonalib.Log("Metric nil")
					time.Sleep(time.Duration(k.SleepTime) * time.Second)
					return
				} else {
					kodomoRespt := OKASAN_SCRAPERS[k.Okasan.Name].Kodomo[k.Name].Metrics.Respt
					kodomoKPAcus := k.Cus

					if kodomoKPAcus == nil || kodomoRespt == nil {
						time.Sleep(time.Duration(k.SleepTime) * time.Second)
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
					if target == "" {
						bonalib.Log("NOT FOUND KODOMO", k.Name)
						time.Sleep(time.Duration(k.SleepTime) * time.Second)
						continue
					}
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
				}
			}
			// RETRIEVE VALUE FROM OKASAN STRUCT
			// [o.Name] is OkasanScheduler.Name which is okaasan
			// kodomoRespt := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Metrics.Respt
		}
	}

}

func autoscalingTarget(kodomo *KodomoScheduler) (string, error) {
	select {
	case <-kodomo.ScheduleStop.Okasan:
		// bonalib.Log("STOP GET AUTOSCALING TARGET ANNOTAION")
		time.Sleep(time.Duration(kodomo.SleepTime) * time.Second)
		return "KodomoStopped", nil
	default:
		ksvcGVR := schema.GroupVersionResource{
			Group:    "serving.knative.dev",
			Version:  "v1",
			Resource: "services",
		}

		ksvcDetails, err := DYNCLIENT.Resource(ksvcGVR).Namespace("default").Get(context.TODO(), kodomo.Name, metav1.GetOptions{})
		if err != nil {
			bonalib.Warn("Error listing Knative services to find target annotation:", err)
			return "", err
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

}

func SchedulePodState(podstate *PodMonitor) {
	for {
		select {
		case <-podstate.StateChan.WarmDisk: // Service and image available
			// if !kodomo.State.WarmDisk {
			if podstate.State.Cold {
				bonalib.Log("Changing from Cold to Warm Disk")
				// PULL IMAGE TO DOCKER
				// dockerPull(kodomo)
				podstate.State.Cold = false
				podstate.State.WarmDisk = true
				bonalib.Log("Finsish changing from Cold to Warm Disk")
			} else if podstate.State.WarmCPU {
				bonalib.Log("Changing from WarmDisk to WarmCPU")
				podstate.State.WarmCPU = false
				podstate.State.WarmDisk = true
				bonalib.Log("Changing from WarmDisk to WarmCPU")
			} else if !podstate.State.Cold && !podstate.State.WarmCPU {
				bonalib.Log("You are in wrong state")
			}
			return
		case <-podstate.StateChan.WarmCPU: // Container exist, ready to receive request
			bonalib.Log("Warmcpu")
			if podstate.State.WarmDisk {
				bonalib.Log("Changing to WarmCPU")
				podstate.State.WarmDisk = false
				podstate.State.WarmCPU = true
				bonalib.Log("Finish changing from WarmDisk to WarmCPU")
			} else if podstate.State.Active {
				bonalib.Log("Changing to WarmCPU")
				podstate.State.Active = false
				podstate.State.WarmCPU = true
				bonalib.Log("Finish changing from WarmCPU to WarmCPU")
			} else if !podstate.State.Active && !podstate.State.WarmDisk {
				bonalib.Log("You are in wrong state")
			}
			return
		case <-podstate.StateChan.Active: // Receiving request
			return
		default: // If kodomo first init (Convert from Null to Cold)
			// for {
			// 	bonalib.Log("default")
			// 	if p.State.Null { // This "if" will loop over [schedule] until ksvc finish initialize
			// 		bonalib.Log("Changing from Null to Cold")
			// 		p.initKsvc()
			// 		// p.image = grepImage(kodomo.Name)
			// 		// p.imageID = grepImageID(p.Name)
			// 		// deleteSeika(kodomo.Name)
			// 		// bonalib.Log("image", kodomo.image)
			// 		// bonalib.Log("imageid", kodomo.imageID)
			// 		// crictlRmi(kodomo)
			// 		p.State.Cold = true
			// 		bonalib.Log("Finish changing from Null to Cold")
			// 	}

			// 	time.Sleep(time.Duration(p.sleepTime) * time.Second)
			// }
			bonalib.Log("DEFAULT")
			time.Sleep(time.Duration(podstate.sleepTime) * time.Second)
		}
	}
}

func (k *KodomoScheduler) AddPodState(podmonitor *PodMonitor) {
	podmonitor.Kodomo = k
	k.PodMonitor[podmonitor.Name] = podmonitor
	// go k.SchedulePodState(k.PodState[podstate.Name])
}

func (k *KodomoScheduler) deletePodState(podstate *PodMonitor) {
	return
}
