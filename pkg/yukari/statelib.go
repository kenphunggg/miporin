package yukari

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // For Windows compatibility
}

func grepImageID(ksvcName string) (imageid string) {
	for _, node := range NODENAMES {
		sshUser := "root"
		sshKeyPath := filepath.Join(homeDir(), ".ssh", "id_rsa")

		// Read the private key file
		key, err := os.ReadFile(sshKeyPath)
		if err != nil {
			log.Fatalf("unable to read private key: %v", err)
		}

		// Parse the private key
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			log.Fatalf("unable to parse private key: %v", err)
		}

		// Create the SSH client configuration
		config := &ssh.ClientConfig{
			User: sshUser,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), // This is insecure, replace with a proper callback in production
		}

		// Connect to the SSH server
		client, err := ssh.Dial("tcp", node+":22", config)
		if err != nil {
			log.Fatalf("unable to connect to SSH server: %v", err)
		}
		defer client.Close()

		// Create a new SSH session
		session, err := client.NewSession()
		if err != nil {
			log.Fatalf("unable to create SSH session: %v", err)
		}
		defer session.Close()

		// Run the command
		command := "crictl image ls | grep " + ksvcName + " | awk '{print $3}'"
		output, err := session.CombinedOutput(command)
		if err != nil {
			log.Fatalf("failed to execute command: %v", err)
		}

		// bonalib.Log("outputid", string(output), node)

		if string(output) != "" {
			imageid = strings.TrimSpace(string(output))
		}
	}
	return imageid
}

func grepImage(ksvcName string) string {
	gvr := schema.GroupVersionResource{
		Group:    "serving.knative.dev",
		Version:  "v1",
		Resource: "services",
	}

	// Get the Knative Service
	unstructuredObj, err := DYNCLIENT.Resource(gvr).Namespace("default").Get(context.TODO(), ksvcName, metav1.GetOptions{})
	if err != nil {
		panic(err.Error())
		return ""
	}

	// Extract the image field from the unstructured object
	spec, found, err := unstructured.NestedMap(unstructuredObj.Object, "spec", "template", "spec")
	if err != nil || !found {
		panic(fmt.Errorf("spec.template.spec not found in the Knative Service"))
		return ""
	}

	containers, found, err := unstructured.NestedSlice(spec, "containers")
	if err != nil || !found || len(containers) == 0 {
		panic(fmt.Errorf("containers not found in spec.template.spec or is empty"))
		return ""
	}

	container := containers[0].(map[string]interface{})
	image, found, err := unstructured.NestedString(container, "image")
	if err != nil || !found {
		panic(fmt.Errorf("image not found in the container spec"))
		return ""
	}

	return image
}

// func crictlRmi(kodmo *KodomoScheduler) {
// 	for _, node := range NODENAMES {
// 		// Define the remote command
// 		remoteCommand := "crictl rmi " + kodmo.imageID

// 		// Use the `ssh` command with the hostname from your SSH config file
// 		sshCommand := exec.Command("ssh", node, remoteCommand)

// 		// Capture stdout and stderr
// 		var stdout, stderr bytes.Buffer
// 		sshCommand.Stdout = &stdout
// 		sshCommand.Stderr = &stderr

// 		// Run the command
// 		if err := sshCommand.Run(); err != nil {
// 			fmt.Printf("Error executing SSH command: %v\n", err)
// 			fmt.Printf("Stderr: %s\n", stderr.String())
// 		}

// 		// Print the output
// 		fmt.Println("Command output:", stdout.String())
// 	}

// }

// func dockerPull(kodomo *KodomoScheduler) {
// 	for _, node := range NODENAMES {
// 		// Define the SSH command and the remote Docker pull command
// 		remoteCommand := "docker pull " + kodomo.image
// 		sshCommand := exec.Command("ssh", node, remoteCommand)

// 		// Capture stdout and stderr for debugging and logging
// 		var stdout, stderr bytes.Buffer
// 		sshCommand.Stdout = &stdout
// 		sshCommand.Stderr = &stderr

// 		bonalib.Log("Pulling image for node:", node)

// 		// Execute the SSH command
// 		err := sshCommand.Run()
// 		if err != nil {
// 			bonalib.Warn("Cannot pull image on node:", node)
// 			fmt.Printf("Error executing SSH command: %v\n", err)
// 			fmt.Printf("Stderr: %s\n", stderr.String())
// 		}

// 		// Print the output of the command
// 		fmt.Println("Command output:", stdout.String())
// 	}
// }
