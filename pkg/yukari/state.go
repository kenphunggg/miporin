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

type StateSignal struct {
	enableNull       bool
	enableCold       bool
	enableWarmDisk   bool
	enableWarmCPU    bool
	enableWarmMemory bool
	enableActive     bool
}

func NewState() *State {
	newState := &State{
		Null:       true,
		Cold:       false,
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

func (o *OkasanScheduler) StateSchedule(kodomo *KodomoScheduler) {
	select {
	case <-kodomo.StateChan.Null:
		return
	case <-kodomo.StateChan.Cold:
		if kodomo.State.WarmDisk {
			return
		}
		return
	case <-kodomo.StateChan.WarmDisk: // Service and image available
		if !kodomo.State.WarmDisk {
			if kodomo.State.Cold {
				bonalib.Log("Changing from cold to warm disk")
				// PULL IMAGE TO DOCKER
				dockerPull(kodomo)
				// COPY AND RETAG IMAGE

				// SAVE TO TAR FILE

				// EXPORT TO CRICTL

				// Test
				kodomo.State.Cold = false
				kodomo.State.WarmDisk = true
				bonalib.Log("Finsish changing from cold to warm disk")
			}
		}
		// return
	case <-kodomo.StateChan.WarmCPU: // Container exist, ready to receive request
		if !kodomo.State.WarmCPU {
			if kodomo.State.WarmDisk {
				<-kodomo.StateChan.WarmDisk
				bonalib.Log("Changing to WarmCPU")
				kodomo.State.WarmDisk = false
				kodomo.State.WarmCPU = true
				bonalib.Log("Finish changing from WarmDisk to WarmCPU")
			}
		}
		// return
	case <-kodomo.StateChan.WarmMemory: // Pause container
		return
	case <-kodomo.StateChan.Active: // Receiving request
		return
	default: // If kodomo first init (Convert from Null to Cold)
		if kodomo.State.Null { // This "if" will loop over [schedule] until ksvc finish initialize
			for { // Loop until finish initilize ksvc
				pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					panic(err.Error())
				}

				podCount := len(pods.Items)
				grep := false

				for _, pod := range pods.Items { // This is an infinite loop loop for all pods
					if podCount > 0 { // This will ensure the loop will only iterate over existing pods
						if strings.Contains(pod.Name, kodomo.Name) {
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
					kodomo.State.Null = false
					break
				}
				time.Sleep(time.Duration(o.sleepTime) * time.Second)
			}
			if !kodomo.State.Cold {
				kodomo.image = grepImage(kodomo.Name)
				kodomo.imageID = grepImageID(kodomo.Name)
				// deleteSeika(kodomo.Name)
				// bonalib.Log("image", kodomo.image)
				// bonalib.Log("imageid", kodomo.imageID)
				// crictlRmi(kodomo)
				kodomo.State.Cold = true

			}
		}
	}
}
