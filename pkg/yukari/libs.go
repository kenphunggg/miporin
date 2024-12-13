package yukari

import (
	"context"
	"fmt"
	"time"

	"github.com/bonavadeur/miporin/pkg/bonalib"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func createSeika(ksvcName string) {
	namespace := "default"

	seikaGVR := schema.GroupVersionResource{
		Group:    "batch.bonavadeur.io",
		Version:  "v1",
		Resource: "seikas",
	}

	var deployment *v1.Deployment
	var err error
	for {
		deployment, err = CLIENTSET.AppsV1().
			Deployments(namespace).
			Get(context.TODO(), ksvcName+"-00001-deployment", metav1.GetOptions{})
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		} else {
			// delete some fields
			deployment.Spec.Template.ObjectMeta.CreationTimestamp = metav1.Time{}
			deployment.ObjectMeta.ResourceVersion = ""
			deployment.ObjectMeta.UID = ""
			// time.Sleep(5 * time.Second)
			break
		}
	}

	seikaInstance := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "batch.bonavadeur.io/v1",
			"kind":       "Seika",
			"metadata": map[string]interface{}{
				"name": ksvcName,
			},
			"spec": map[string]interface{}{
				"repurika": map[string]interface{}{},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"bonavadeur.io/seika": ksvcName,
					},
				},
				"template": deployment.Spec.Template,
			},
		},
	}
	repurika := seikaInstance.Object["spec"].(map[string]interface{})["repurika"].(map[string]interface{})
	for _, nodename := range NODENAMES {
		repurika[nodename] = 0
	}

	// // create seika instance
	result, err := DYNCLIENT.Resource(seikaGVR).Namespace("default").Create(context.TODO(), seikaInstance, metav1.CreateOptions{})
	if err != nil {
		fmt.Println(err)
	} else {
		bonalib.Info("Created Seika instance", result.GetName())
	}
}

func deleteSeika(ksvcName string) {
	namespace := "default"

	seikaGVR := schema.GroupVersionResource{
		Group:    "batch.bonavadeur.io",
		Version:  "v1",
		Resource: "seikas",
	}
	deletePolicy := metav1.DeletePropagationBackground
	err := DYNCLIENT.
		Resource(seikaGVR).
		Namespace(namespace).
		Delete(context.TODO(), ksvcName, metav1.DeleteOptions{
			PropagationPolicy: &deletePolicy,
		})
	if err != nil {
		bonalib.Warn("Failed to delete Seika instance")
	} else {
		bonalib.Info("Deleted Seika instance", ksvcName)
	}
}

// Function to check if a slice contains a specific value
func contains(slice []string, value string) bool {
	for _, v := range slice {
		if v == value {
			return true
		}
	}
	return false
}

func removeValue(slice []string, value string) []string {
	var result []string
	for _, v := range slice {
		if v != value {
			result = append(result, v)
		}
	}
	return result
}

func autoExpire(startTime map[string]time.Time, slice []string, durationTime int) (map[string]time.Time, []string) {
	duration := time.Duration(durationTime) * time.Second
	now := time.Now()

	// Auto expire value inside a slice
	for pod, start := range startTime {
		if now.Sub(start) >= duration {
			slice = removeValue(slice, pod)
			delete(startTime, pod)
		}
	}

	return startTime, slice
}

func calculateAverage(slice []float64) float64 {
	if len(slice) == 0 {
		return 0.0 // Return 0 if the slice is empty to avoid division by zero
	}
	var sum float64
	for _, value := range slice {
		sum += value
	}
	average := sum / float64(len(slice))
	return average
}

func ceilDivide(a, b int) int {
	result := a / b
	if a%b != 0 {
		result++
	}
	return result
}

func getTargetAnnotation(config interface{}) (string, error) {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid configuration format")
	}

	spec, ok := configMap["spec"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("'spec' not found in configuration")
	}

	template, ok := spec["template"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("'template' not found in spec")
	}

	metadata, ok := template["metadata"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("'metadata' not found in template")
	}

	annotations, ok := metadata["annotations"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("'annotations' not found in metadata")
	}
	target, ok := annotations["autoscaling.knative.dev/target"].(string)
	if !ok {
		return "", fmt.Errorf("'autoscaling.knative.dev/target' not found in annotations")
	}
	return target, nil
}
