package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/apex/log"
	"github.com/getsentry/sentry-go"
	v12 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	v13 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"strings"
	"time"
)

func getConfig(cfg string) (*rest.Config, error) {
	if cfg == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", cfg)
}

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

type patchString struct {
	Op   string `json:"op"`
	Path string `json:"path"`
}

func newClientset(filename string) (*kubernetes.Clientset, error) {
	config, err := getConfig(filename)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func errExternalAddressNotFound() error { return errors.New("external address not found") }

func getExternalIp(node v12.Node) (string, error) {
	for _, address := range node.Status.Addresses {
		if address.Type == v12.NodeExternalIP {
			return address.Address, nil
		}
	}
	return "", errExternalAddressNotFound()
}

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}

func main() {

	err := sentry.Init(sentry.ClientOptions{
		// Either set your DSN here or set the SENTRY_DSN environment variable.
		Dsn: "https://3704ef4f0d3a4842aac67ace79509b67@sentry.moodwork.com/11",
		// Either set environment and release here or set the SENTRY_ENVIRONMENT
		// and SENTRY_RELEASE environment variables.
		Environment: "",
		Release:     "node-sentinele@1.0.0",
		// Enable printing of SDK debug messages.
		// Useful when getting started or trying to figure something out.
		Debug: true,
	})
	if err != nil {
		log.Fatalf("sentry.Init: %s", err)
	}
	var kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	var coffee_namespace = flag.String("coffee-namespace", "", "scaleway-k8s-node-coffee namespace name")
	flag.Parse()
	const configmapName = "scaleway-k8s-node-coffee"

	const excludeExternalLoadBalancerlabelKey = "node.kubernetes.io/exclude-from-external-load-balancers"
	const excludeExternalLoadBalancerlabelKeyEscaped = "node.kubernetes.io~1exclude-from-external-load-balancers"
	const excludeExternalLoadBalancerlabelValue = "true"

	if coffee_namespace == nil || *coffee_namespace == "" {
		log.Fatal("scaleway-k8s-node-coffee namespace name is required to get reserved ip pools")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientSet, err := newClientset(*kubeconfig)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to cluster")
	}

	configmap, err := clientSet.CoreV1().ConfigMaps(*coffee_namespace).Get(ctx, configmapName, v1.GetOptions{})
	if err != nil {
		log.Error(err.Error())
	}
	reservedIps := strings.Split(configmap.Data["RESERVED_IPS_POOL"], ",")

	defer sentry.Flush(2 * time.Second)

	indexer := cache.Indexers{}

	nodeInformer := v13.NewNodeInformer(clientSet, time.Second*10, indexer)
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node := obj.(*v12.Node)
			log.Info("node added:" + node.Name)
		},
		DeleteFunc: func(obj interface{}) {
			node := obj.(*v12.Node)
			log.Info("node deleted: " + node.Name)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNode := oldObj.(*v12.Node)
			oldExternalIp, oldErr := getExternalIp(*oldNode)
			newNode := newObj.(*v12.Node)

			if _, ok := newNode.Labels["reserved-ip"]; ok {
				if _, oldok := oldNode.Labels["reserved-ip"]; oldok {
				} else {
					log.Info("node: " + newNode.Name + " added  label reserved-ip")
				}
			}

			newExternalIp, newErr := getExternalIp(*newNode)
			if oldErr == nil && newErr == nil {
				if oldExternalIp != newExternalIp {
					log.Info("node: " + newNode.Name + " ip changed: " + oldExternalIp + " > " + newExternalIp)
				} else {

					if contains(reservedIps, oldExternalIp) {
						log.Debug("node: " + newNode.Name + " ip is still the same: " + newExternalIp + " and part of reserved ip pool")

						if _, ok := newNode.Labels[excludeExternalLoadBalancerlabelKey]; ok {
							log.Info("allowing node: " + newNode.Name + " to join Load Balancer backend pool")

							payload := []patchString{{
								Op:   "remove",
								Path: fmt.Sprintf("/metadata/labels/%s", excludeExternalLoadBalancerlabelKeyEscaped),
							}}
							payloadBytes, _ := json.Marshal(payload)
							_, err = clientSet.CoreV1().Nodes().Patch(ctx, newNode.Name, types.JSONPatchType, []byte(payloadBytes), v1.PatchOptions{})

							if err != nil {
								fmt.Println(err)
							}
						}
					} else {
						log.Info("node: " + newNode.Name + " ip is still the same: " + newExternalIp + " and not part of reserved ip pool")

						if _, ok := newNode.Labels[excludeExternalLoadBalancerlabelKey]; ok {

						} else {
							log.Info("Excluding this node: " + newNode.Name + " from External Load Balancer")
							payload := []patchStringValue{{
								Op:    "add",
								Path:  fmt.Sprintf("/metadata/labels/%s", excludeExternalLoadBalancerlabelKeyEscaped),
								Value: excludeExternalLoadBalancerlabelValue,
							}}
							payloadBytes, _ := json.Marshal(payload)
							_, err = clientSet.CoreV1().Nodes().Patch(ctx, newNode.Name, types.JSONPatchType, []byte(payloadBytes), v1.PatchOptions{})
							if err != nil {
								fmt.Println(err)
							}
						}

					}
				}
			} else {
				log.Info("node: " + oldNode.Name + " external ip is not yet available")
			}
		},
	})
	stop := make(chan struct{})
	defer close(stop)
	nodeInformer.Run(stop)
	for {
		time.Sleep(time.Second)
	}

}
