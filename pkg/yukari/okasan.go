package yukari

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

type OkasanScheduler struct {
	Name         string
	sleepTime    int8
	Kodomo       map[string]*KodomoScheduler
	ScheduleStop map[string]chan bool
	MaxPoN       map[string]int32
}

func NewOkasanScheduler(
	name string,
	sleepTime int8,
) *OkasanScheduler {

	atarashiiOkasanScheduler := &OkasanScheduler{
		Name:         name,
		sleepTime:    sleepTime,
		Kodomo:       map[string]*KodomoScheduler{},
		ScheduleStop: map[string]chan bool{},
		MaxPoN:       map[string]int32{},
	}

	go atarashiiOkasanScheduler.watchKsvcCreateEvent()

	return atarashiiOkasanScheduler
}

func (o *OkasanScheduler) schedule(kodomo *KodomoScheduler) {
	currentDesiredPods := map[string]int32{}
	newDesiredPods := map[string]int32{}
	deltaDesiredPods := map[string]int32{}
	decideInNode := map[string]int32{}
	noChanges := map[string]int32{}
	for _, nodename := range NODENAMES {
		currentDesiredPods[nodename] = 0
		newDesiredPods[nodename] = 0
		deltaDesiredPods[nodename] = 0
		noChanges[nodename] = 0
	}
	firstTime := true
	var minResponseTime, minIdx int32

	for {
		select {
		case <-kodomo.ScheduleStop:
			return
		default:
			decideInNode = kodomo.Decision

			if firstTime {
				currentDesiredPods = decideInNode
				firstTime = false
			} else {
				newDesiredPods = decideInNode
			}
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

			nodeidx := map[string]int{}
			for i, nodename := range NODENAMES {
				nodeidx[nodename] = i
			}

			for k_ddp := range deltaDesiredPods {
				for i := deltaDesiredPods[k_ddp]; i != 0; {
					if i < 0 {
						currentDesiredPods[k_ddp]--
						i++
					}
					if i > 0 {
						minResponseTime = int32(1000000)
						minIdx = -1
						responseTime := OKASAN_SCRAPERS[o.Name].Kodomo[kodomo.Name].Metrics.Respt
						for i_rpt := range responseTime[nodeidx[k_ddp]] { // loop each row of RESPONSETIME
							if currentDesiredPods[k_ddp] >= int32(MAXPON[nodeidx[k_ddp]]) {
								continue
							} else {
								if responseTime[nodeidx[k_ddp]][i_rpt] < minResponseTime {
									minResponseTime = responseTime[nodeidx[k_ddp]][i_rpt]
									minIdx = int32(i_rpt)
								}
							}
						}
						if minIdx != -1 {
							currentDesiredPods[NODENAMES[minIdx]]++
						}
						i--
					}
				}
			}

			bonalib.Log("currentDesiredPods", currentDesiredPods)

			o.patchSchedule(currentDesiredPods)

			time.Sleep(time.Duration(o.sleepTime) * time.Second)
		}
	}
}

func (o *OkasanScheduler) patchSchedule(desiredPods map[string]int32) {
	gvr := schema.GroupVersionResource{
		Group:    "batch.bonavadeur.io",
		Version:  "v1",
		Resource: "seikas",
	}

	// Define the patch data
	repurika := map[string]interface{}{}
	for _, nodename := range NODENAMES {
		repurika[nodename] = desiredPods[nodename]
	}
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

			// create apropriate KodomoScheduler
			child := NewKodomoScheduler(ksvcName, o.sleepTime)
			o.addKodomo(child)

			// create apropriate Seika
			createSeika(ksvcName)
			createSeika(ksvcName)
			go createSeika(ksvcName)
		}
		if event.Type == watch.Deleted {
			bonalib.Warn("Ksvc has been deleted:", ksvcName)

			// delete apropriate KodomoScheduler
			o.deleteKodomo(ksvcName)

			// create apropriate Seika
			deleteSeika(ksvcName)
			go deleteSeika(ksvcName)
		}
	}
}

func (o *OkasanScheduler) addKodomo(kodomo *KodomoScheduler) {
	kodomo.Okasan = o
	o.Kodomo[kodomo.Name] = kodomo
	o.ScheduleStop[kodomo.Name] = make(chan bool)
	go o.schedule(kodomo)
}

func (o *OkasanScheduler) deleteKodomo(kodomo string) {
	o.Kodomo[kodomo].ScheduleStop <- true
	o.ScheduleStop[kodomo] <- true
	delete(o.ScheduleStop, kodomo)
	o.Kodomo[kodomo] = nil
	delete(o.Kodomo, kodomo)
}