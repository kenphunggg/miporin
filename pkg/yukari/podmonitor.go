package yukari

import (
	"context"
	"strings"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type State struct {
	Null       bool
	Cold       bool // Service availble
	WarmDisk   bool // Service and image available
	WarmCPU    bool // Container exist, ready to receive request
	WarmMemory bool // Pause container
	Active     bool // Receiving request
}

type StateChan struct {
	Null       chan bool
	Cold       chan bool // Service availble
	WarmDisk   chan bool // Service and image available
	WarmCPU    chan bool // Container exist, ready to receive request
	WarmMemory chan bool // Pause container
	Active     chan bool // Receiving request
}

type PodMonitor struct {
	Name      string
	NodeName  string
	sleepTime int8
	State     *State
	StateChan *StateChan
	Kodomo    *KodomoScheduler
}

func NewState() *State {
	newState := &State{
		Null:       false,
		Cold:       true,
		WarmDisk:   false,
		WarmCPU:    false,
		WarmMemory: false,
		Active:     false,
	}
	return newState
}

func NewStateChan() *StateChan {
	newStateChan := &StateChan{
		Null:       make(chan bool),
		Cold:       make(chan bool),
		WarmDisk:   make(chan bool),
		WarmCPU:    make(chan bool),
		WarmMemory: make(chan bool),
		Active:     make(chan bool),
	}
	return newStateChan
}

func NewPodMonitor(name string, sleepTime int8) *PodMonitor {
	newPodState := &PodMonitor{
		Name:      name,
		sleepTime: sleepTime,
		State:     NewState(),
		StateChan: NewStateChan(),
	}

	// go newPodState.PodStateSchedule()

	return newPodState
}

func SchedulePodState(podmonitor *PodMonitor) {
	for {
		select {
		// DONE
		case <-podmonitor.Kodomo.ScheduleStop.Kodomo:
			bonalib.Log("Pod", podmonitor.Name, "in Null state (stopped)")
			// time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
			return
		// DONE
		case <-podmonitor.StateChan.Null:
			if podmonitor.State.Cold {
				podmonitor.State.Cold = false
				podmonitor.State.Null = true
				bonalib.Log("Stop scheduling for pod", podmonitor.Name, "in ksvc", podmonitor.Kodomo.Name)
				return
			} else {
				bonalib.Log("You are in wrong state")
				time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
			}
		// DONE
		case <-podmonitor.StateChan.Cold:
			if podmonitor.State.WarmDisk {
				bonalib.Log("Changing from Warm Disk to Cold")
				// Delete image
				dockerRmi(podmonitor)
				podmonitor.State.WarmDisk = false
				podmonitor.State.Cold = true
				bonalib.Log("Finsish changing from Warm Disk to Cold")
				time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
			} else {
				bonalib.Log("You are in wrong state")
				time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
			}
		// DONE
		case <-podmonitor.StateChan.WarmDisk: // Service and image available (Download image)
			if podmonitor.State.Cold {
				bonalib.Log("Changing from Cold to Warm Disk")
				// PULL IMAGE TO DOCKER
				dockerPull(podmonitor)
				podmonitor.State.Cold = false
				podmonitor.State.WarmDisk = true
				bonalib.Log("Finsish changing from Cold to Warm Disk")
			} else if podmonitor.State.WarmCPU {
				bonalib.Log("Changing from WarmDisk to WarmCPU")
				podmonitor.State.WarmCPU = false
				podmonitor.State.WarmDisk = true
				bonalib.Log("Changing from WarmDisk to WarmCPU")
			} else if !podmonitor.State.Cold && !podmonitor.State.WarmCPU {
				bonalib.Log("You are in wrong state")
				time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
			}
		// FROM ACTIVE TO WARMCPU
		case <-podmonitor.StateChan.WarmCPU: // Container exist, ready to receive request
			if podmonitor.State.WarmDisk {
				bonalib.Log("Changing to WarmCPU")
				podmonitor.State.WarmDisk = false
				podmonitor.State.WarmCPU = true
				bonalib.Log("Finish changing from WarmDisk to WarmCPU")
			} else if podmonitor.State.Active {
				bonalib.Log("Changing to WarmCPU")
				podmonitor.State.Active = false
				podmonitor.State.WarmCPU = true
				bonalib.Log("Finish changing from WarmCPU to WarmCPU")
			} else if !podmonitor.State.Active && !podmonitor.State.WarmDisk {
				bonalib.Log("You are in wrong state")
				time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
			}
		case <-podmonitor.StateChan.Active: // Receiving request
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
			// bonalib.Log("Pod", podmonitor.Name, "in cold state")
			time.Sleep(time.Duration(podmonitor.sleepTime) * time.Second)
		}
	}
}

func (p *PodMonitor) initKsvc() {
	for { // Loop until finish initilize ksvc
		pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}

		podCount := len(pods.Items)
		grep := false

		for _, pod := range pods.Items { // This is an infinite loop loop for all pods
			if podCount > 0 { // This will ensure the loop will only iterate over existing pods
				if strings.Contains(pod.Name, p.Name) {
					// bonalib.Log("pod name", pod.Name)
					grep = true
				}
				podCount--
			}
			if podCount == 0 { // This will ensure the loop will only iterate over existing pods
				if !grep { // If there is no pod in [deployment state] / Finish initialize ksvc
					// bonalib.Log("finish initialize")
				}
				break
			}
		}

		if !grep {
			p.State.Null = false
			break
		}
		time.Sleep(time.Duration(p.sleepTime) * time.Second)
	}
}

func CreatePodMonitor(kodomo *KodomoScheduler, podstate *PodMonitor) {
	podstate.Kodomo = kodomo
	kodomo.PodMonitor[podstate.Name] = podstate
	go SchedulePodState(podstate)
}

func DeletePodMonitor(kodomo *KodomoScheduler, podstate *PodMonitor) {
	podstate.StateChan.Null <- true
	podname := podstate.Name
	delete(podstate.Kodomo.PodMonitor, podstate.Name)
	bonalib.Info("Stopped monitoring for pod", podname, "in ksvc", kodomo.Name)
}
