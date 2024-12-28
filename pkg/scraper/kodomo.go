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
	// Serving time for traffic from node i to be proccessed on node j
	Respt [][]int32
}

type StopChan struct {
	Kodomo chan bool
}

func NewKodomoScraper(
	name string, //ksvc name
	window string,
	sleepTime int8,
) *KodomoScraper {
	atarashiiKodomoScraper := &KodomoScraper{
		Name:       name,
		Metrics:    NewMetrics(), // Hold 2 2D slices(Serving time and Response time)
		window:     window,
		sleepTime:  sleepTime,
		Okasan:     nil,
		PodOnNode:  map[string]int32{}, // Hold number of pod on each node
		Weight:     make([][]int32, len(NODENAMES)),
		ScrapeStop: NewStopChan(), // Channel that have ability to stop the Kodomo
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
			// Return serving time matrix
			// Extract value to [KodomoScraper.Matrix.Servt]
			k.scrapeServingTime()
			// Get total number of pods with default namespace on all nodes
			// Extract value to [KodomoScraper.PodOnNode]
			k.scrapePodOnNode()
			// Response time equal Serving time plus Latency
			// Serving time scraped from Service monitor by Kodomo
			// Latency between nodes scraped from Prometheus by Okasan
			if k.Metrics.Servt == nil || k.Okasan.Latency == nil {
				time.Sleep(time.Duration(k.sleepTime) * time.Second)
				continue
			}

			k.Metrics.Respt = libs.AddMatrix(k.Metrics.Servt, k.Okasan.Latency)

			// Make a empty 2D "weight" slice
			w := make([][]int32, len(NODENAMES))
			// Make a negative 2D slice based on Response matrix
			for i, row := range k.Metrics.Respt {
				w[i] = weightedNegative(row) //Golang's build in function to turn value to negative(-)
			}

			// Get total pods with "default" namespace
			_sumPods := int32(0)
			for nodename := range k.PodOnNode {
				_sumPods += k.PodOnNode[nodename]
			}

			if _sumPods == 0 { // PoN == [0, 0, 0] / Make a square matrix with main diaginal set to 100
				// Make an empty slice hold "weight" value
				w = make([][]int32, len(NODENAMES))
				// Make an empty [len(NODENAMES)]x[len(NODENAMES)] slice
				// This will ensure the matrix created is a square matrix
				for i := range w {
					w[i] = make([]int32, len(NODENAMES))
				}
				// Set the value in "main diagonal" to 100
				for i := 0; i < len(NODENAMES); i++ {
					w[i][i] = 100
				}
			} else { // Set value for lower triangular matrix
				for i := range w {
					// Set value for lower triangular matrix
					for j := range w[i] {
						if k.PodOnNode[NODENAMES[j]] == 0 { // If numner of pod on destination pod equal 0
							w[i][j] = 0
						}
						if k.PodOnNode[NODENAMES[j]] != 0 && w[i][j] == 0 { // If weight is not set or set to zero
							w[i][j] = 1 //Set to 1
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

// Current problem: Only get serving time that served on node i but not mention the source of the traffic
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

// Scrape number of pod on each node
func (k *KodomoScraper) scrapePodOnNode() {
	// kubectl get pods -n default
	pods, err := CLIENTSET.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		bonalib.Warn("KodomoScraper.scrapePodOnNode: err when list all pods", err)
		panic(err)
	}

	// Initialize a map hold the number of pod on each node
	podOnNode := map[string]int32{}
	for _, node := range NODENAMES {
		podOnNode[node] = 0
	}

	// Count all pods with status "Running" on each node and extract the data to [Kodomo.podOnNode]
	for _, pod := range pods.Items {
		if pod.Status.Phase != "Terminating" && pod.Status.Phase != "Pending" && strings.Contains(pod.Name, "hello") {
			podOnNode[pod.Spec.NodeName]++
		}
	}

	k.PodOnNode = podOnNode
}
