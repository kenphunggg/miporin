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
	case <-kodomo.KodomoStateChan.Null:
		return
	case <-kodomo.KodomoStateChan.Cold:
		if kodomo.KodomoState.WarmDisk {
			return
		}
		return
	case <-kodomo.KodomoStateChan.WarmDisk: // Service and image available
		return
	case <-kodomo.KodomoStateChan.WarmCPU: // Container exist, ready to receive request
		return
	case <-kodomo.KodomoStateChan.WarmMemory: // Pause container
		return
	case <-kodomo.KodomoStateChan.Active: // Receiving request
		return
	default: // If kodomo first init (Convert from Null to Cold)
		if kodomo.KodomoState.Null { // This "if" will loop over [schedule] until ksvc finish initialize
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
					bonalib.Log("FINISH")
					kodomo.KodomoState.Null = false
					break
				}
				time.Sleep(time.Duration(o.sleepTime) * time.Second)
			}
		}
		bonalib.Log("Finish initializing ksvc", kodomo.Name)

		deleteSeika(kodomo.Name)

		bonalib.Log("seika", kodomo.Name, "deleted")

	}
}
