package scraper

import (
	"context"
	"strings"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	"github.com/bonavadeur/miporin/pkg/libs"
	"github.com/bonavadeur/miporin/pkg/miporin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type KodomoScraper struct {
	Name       string
	Metrics    *Metrics
	window     string // seconds
	sleepTime  int8   // seconds
	Okasan     *OkasanScraper
	PodOnNode  map[string]int32
	Weight     [][]int32
	ScrapeStop *StopChan
}

// Create new type [Metrics] that contain Servingtime and Responsetime
type Metrics struct {
	// Serving time for traffic from node i to node j
	Servt [][]int32
	// Serving time for traffic from node i to node j
	Respt [][]int32
}

type StopChan struct {
	Kodomo chan bool
}

func NewKodomoScraper(
	name string,
	window string,
	sleepTime int8,
) *KodomoScraper {
	atarashiiKodomoScraper := &KodomoScraper{
		Name:       name,
		Metrics:    NewMetrics(),
		window:     window,
		sleepTime:  sleepTime,
		Okasan:     nil,
		PodOnNode:  map[string]int32{},
		Weight:     make([][]int32, len(NODENAMES)),
		ScrapeStop: NewStopChan(),
	}

	for _, nodename := range NODENAMES {
		atarashiiKodomoScraper.PodOnNode[nodename] = int32(0)
	}

	// kodomo scrape own metrics: servingTime
	go atarashiiKodomoScraper.scrape()

	return atarashiiKodomoScraper
}

func NewMetrics() *Metrics {
	newMetrics := &Metrics{
		Servt: [][]int32{},
		Respt: [][]int32{},
	}
	return newMetrics
}

func NewStopChan() *StopChan {
	newStopChan := &StopChan{
		Kodomo: make(chan bool),
	}
	return newStopChan
}

func (s *StopChan) Stop() {
	s.Kodomo <- true
}

func (k *KodomoScraper) scrape() {
	for {
		select {
		// If it receive a signal from [KodomoScraper], return nothing
		case <-k.ScrapeStop.Kodomo:
			return
		// By default
		default:
			k.scrapeServingTime()
			k.scrapePodOnNode()
			k.Metrics.Respt = libs.AddMatrix(k.Metrics.Servt, k.Okasan.Latency)

			w := make([][]int32, len(NODENAMES))
			for i, row := range k.Metrics.Respt {
				w[i] = weightedNegative(row)
			}

			_sumPods := int32(0)
			for nodename := range k.PodOnNode {
				_sumPods += k.PodOnNode[nodename]
			}

			if _sumPods == 0 { // PoN == [0, 0, 0]
				w = make([][]int32, len(NODENAMES))
				for i := range w {
					w[i] = make([]int32, len(NODENAMES))
				}
				for i := 0; i < len(NODENAMES); i++ {
					w[i][i] = 100
				}
			} else {
				for i := range w {
					for j := range w[i] {
						if k.PodOnNode[NODENAMES[j]] == 0 {
							w[i][j] = 0
						}
						if k.PodOnNode[NODENAMES[j]] != 0 && w[i][j] == 0 {
							w[i][j] = 1
						}
					}
				}
				for i, row := range w {
					w[i] = weightedPositive(row)
				}
			}

			k.Weight = w

			time.Sleep(time.Duration(k.sleepTime) * time.Second)
		}
	}
}

func (k *KodomoScraper) scrapeServingTime() {
	// Query Serving time over 10 seconds
	// Query test in [Prometheus]
	// rate(revision_request_latencies_sum{service_name="hello"}[10s])/rate(revision_request_latencies_count{service_name="hello"}[10s])
	// Value output: 1.730103324765e+09 50
	// It means Sunday, October 28, 2024, 14:35:24 UTC, 50 (servingtime)
	servingTimeRaw := Query("rate(revision_request_latencies_sum{service_name=\"" + k.Name + "\"}[" + k.window + "s])/rate(revision_request_latencies_count{service_name=\"" + k.Name + "\"}[" + k.window + "s])")
	servingTimeResult := servingTimeRaw["data"].(map[string]interface{})["result"].([]interface{})
	servingTimeLine := make([][]int32, len(NODENAMES))

	for _, stResult := range servingTimeResult {
		// Extract ipaddress from [servingTimeResult]
		// Ip address is the pod ip
		ip := strings.Split(stResult.(map[string]interface{})["metric"].(map[string]interface{})["instance"].(string), ":")[0]
		// Get serving time for that pod/node
		_servingTime := libs.String2RoundedInt(stResult.(map[string]interface{})["value"].([]interface{})[1].(string))
		// Find the location(node) where the the pod located in
		_inNode := miporin.CheckIPInNode(ip)
		// It means that traffic is served at node [_inNode]
		for i, node := range NODENAMES {
			if _inNode == node {
				// Append row i in ServingTimeLine
				servingTimeLine[i] = append(servingTimeLine[i], _servingTime)
			}
		}
	}

	// Each line of [servingTimeRow] equal to average of a line in [servingTimeLine]
	servingTimeRow := make([]int32, len(NODENAMES))
	for i, stl := range servingTimeLine {
		servingTimeRow[i] = libs.Average(stl)
	}

	servingTime := make([][]int32, len(NODENAMES))
	for i := range servingTime {
		servingTime[i] = servingTimeRow
	}

	k.Metrics.Servt = servingTime
}

func (k *KodomoScraper) scrapePodOnNode() {
	pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		bonalib.Warn("KodomoScraper.scrapePodOnNode: err when list all pods", err)
		panic(err)
	}

	podOnNode := map[string]int32{}
	for _, node := range NODENAMES {
		podOnNode[node] = 0
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != "Terminating" && pod.Status.Phase != "Pending" && strings.Contains(pod.Name, "hello") {
			podOnNode[pod.Spec.NodeName]++
		}
	}

	k.PodOnNode = podOnNode
}
