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

func (p *PodMonitor) PodMonitorSchedule() {
	for {
		select {
		case <-p.StateChan.Null:
		case <-p.StateChan.Cold:
		case <-p.StateChan.WarmDisk:
		case <-p.StateChan.WarmCPU:
		case <-p.StateChan.Active:
		default:
			bonalib.Log("DEFAULT")
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
