package kubernetes

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//Kubernetes kubernetes utils
type Kubernetes struct {
	clientset *kubernetes.Clientset
}

//NewClientInCluster initialize a new client of k8s
func NewClientInCluster() (*Kubernetes, error) {
	// creates the in-cluster config
	k := &Kubernetes{}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	k.clientset = clientset
	return k, nil
}

//NewClientOutCluster initialize a new client of k8s
func NewClientOutCluster(kubeConfigPath string) (*Kubernetes, error) {
	// creates the in-cluster config
	k := &Kubernetes{}
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, err
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	k.clientset = clientset
	return k, nil
}

//GetPods get pods from kubernetes
func (k *Kubernetes) GetPods(namespace string, labelSelector string, limit int64) ([]v1.Pod, error) {
	pods, err := k.clientset.CoreV1().Pods(namespace).List(metav1.ListOptions{
		Limit:         limit,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}
	items := pods.Items
	return items, nil
}
