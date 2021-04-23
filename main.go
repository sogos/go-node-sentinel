package main

import (
	"errors"
	"flag"
	"github.com/apex/log"
	"github.com/getsentry/sentry-go"
	v12 "k8s.io/api/core/v1"
	v13 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"time"
)

func getConfig(cfg string) (*rest.Config, error) {
	if cfg == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", cfg)
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
	flag.Parse()
	//ctx, cancel := context.WithCancel(context.Background())
	//defer cancel()
	clientSet, err := newClientset(*kubeconfig)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to cluster")
	}

	//nodes, err := clientSet.CoreV1().Nodes().List(ctx, v1.ListOptions{})
	//if err != nil {
	//	log.WithError(err).Fatal("failed to connect to cluster")
	//}


	defer sentry.Flush(2 * time.Second)

	indexer := cache.Indexers{}

	nodeInformer := v13.NewNodeInformer(clientSet, time.Second*30, indexer)
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
					log.Info("node: " + newNode.Name + " label reserved-ip")
				}
			}
			newExternalIp, newErr := getExternalIp(*newNode)
			if oldErr == nil &&  newErr == nil {
				if oldExternalIp != newExternalIp {
					log.Info("node: " + newNode.Name + " ip changed: " + oldExternalIp + " > " + newExternalIp)
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
