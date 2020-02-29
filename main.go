package main

import (
	"flag"
	"fmt"
	"jms-pod-sync/jumpserver"
	"jms-pod-sync/kubernetes"
	"net/http"
	"os"
	"strings"
	"time"

	set "github.com/deckarep/golang-set"
	"github.com/gin-gonic/gin"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/op/go-logging"
	v1 "k8s.io/api/core/v1"
)

var log = logging.MustGetLogger("jpsync")
var format = logging.MustStringFormatter(
	`%{color}%{time:15:04:05.000} %{shortfunc} ▶ %{level:.4s} %{id:03x}%{color:reset} %{message}`,
)

var (
	help              bool
	username          string
	password          string
	host              string
	kubeConfigPath    string
	port              string
	namespace         string
	label             string
	sshPortNamePrefix string
	interval          time.Duration
	jmsCli            *jumpserver.JS
	kubeCli           *kubernetes.Kubernetes
	podCaches         map[string]*PodCache
)

const (
	waitToAdd int32 = iota
	alreadyAdded
	waitToDel
)

//SSHPort expose ssh port
type SSHPort struct {
	name          string
	containerPort int32
	containerName string
}

//PodCache support cache pods
type PodCache struct {
	name     string
	ip       string
	status   int32
	assetID  string
	sshPorts []SSHPort
}

func init() {
	backend := logging.NewLogBackend(os.Stderr, "", 0)
	backendFormatter := logging.NewBackendFormatter(backend, format)
	backend1Leveled := logging.AddModuleLevel(backend)
	backend1Leveled.SetLevel(logging.ERROR, "")
	logging.SetBackend(backend1Leveled, backendFormatter)
	var err error
	flag.BoolVar(&help, "help", false, "print usage doc")
	flag.StringVar(&host, "host", "", "jumpserver host")
	flag.StringVar(&username, "username", "", "jumpserver user")
	flag.StringVar(&password, "password", "", "jumpserver password")
	flag.StringVar(&namespace, "namespace", "", "k8s namespace")
	flag.StringVar(&label, "label", "", "watch label")
	flag.StringVar(&sshPortNamePrefix, "ssh-port-name-prefix", "ssh", "ssh port name prefix")
	flag.StringVar(&port, "port", ":8080", "listening port")
	flag.DurationVar(&interval, "interval", time.Minute, "sync interval")
	flag.StringVar(&kubeConfigPath, "kubeconfig", "$HOME/.kube/config", "kube config")
	flag.Usage = usage
	flag.Parse()
	if err = checkFlag(); err != nil {
		fmt.Println("Error: " + err.Error())
		flag.Usage()
	}
	jmsCli, err = jumpserver.NewClient(host, username, password)
	if err != nil {
		fmt.Println("Error: " + err.Error())
		flag.Usage()
	}
	kubeCli, err = kubernetes.NewClientInCluster()
	if err != nil {
		kubeCli, err = kubernetes.NewClientOutCluster(kubeConfigPath)
		if err != nil {
			fmt.Println("Error: " + err.Error())
			flag.Usage()
		}
	}
	podCaches = map[string]*PodCache{}
}

func usage() {
	fmt.Println(`
Usage: jpsync [options]

For example:
	
	jpsync --host example.jumpserver.com --username admin --password admin --kubeconfig $HOME/.kube/config		

Options:
		`)
	flag.PrintDefaults()
	os.Exit(1)
}

func checkFlag() error {
	if help {
		flag.Usage()
	}
	if host == "" {
		return fmt.Errorf("host can not be empty")
	} else {
		if !strings.HasPrefix(host, "http://") {
			host = "http://" + host
		}
		if !strings.HasSuffix(host, "/api/v1") {
			host = host + "/api/v1"
		}
	}
	if username == "" {
		return fmt.Errorf("username can not be empty")
	}
	if password == "" {
		return fmt.Errorf("password can not be empty")
	}
	if kubeConfigPath == "$HOME/.kube/config" {
		home, err := homedir.Dir()
		if err != nil {
			return err
		}
		kubeConfigPath = home + "/.kube/config"
	}
	return nil
}

func main() {
	r := setupRouter()
	go syncPodToJms()
	r.Run(port)
}

func setupRouter() *gin.Engine {
	r := gin.Default()
	r.GET("/health", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return r
}

func syncPodToJms() {
	for {
		syncPodToJms0()
		time.Sleep(interval)
	}
}

func syncPodToJms0() {
	log.Info("Starting pod collection...")
	pods, err := kubeCli.GetPods(namespace, label, 65535)
	if err != nil {
		log.Error(err.Error())
	}
	log.Infof("Collected %d pods", len(pods))
	foundSet := set.NewSet()
	alreadyAddedSet := set.NewSet()
	for _, pod := range pods {
		foundSet.Add(pod.Name)
	}
	for podName, podCache := range podCaches {
		if podCache.status == alreadyAdded {
			alreadyAddedSet.Add(podName)
		}
	}
	stableSet := foundSet.Intersect(alreadyAddedSet)
	waitToAddSet := foundSet.Difference(stableSet)
	log.Info("There are %d pod wait to add to jumpserver", waitToAddSet.Cardinality())
	waitToDelSet := alreadyAddedSet.Difference(stableSet)
	log.Info("There are %d pod wait to delete from jumpserver", waitToDelSet.Cardinality())
	cachePods(pods, podCaches, alreadyAddedSet)
	updatePodStatus(podCaches, waitToAddSet, waitToDelSet)
	executePodSync(podCaches, waitToAddSet, waitToDelSet)
}

//cache pods
func cachePods(pods []v1.Pod, podCaches map[string]*PodCache, alreadyAddedSet set.Set) {
	for _, pod := range pods {
		if alreadyAddedSet.Contains(podCaches[pod.Name]) {
			continue
		}
		podCaches[pod.Name] = &PodCache{}
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if strings.HasPrefix(port.Name, sshPortNamePrefix) {
					podCaches[pod.Name].sshPorts = append(podCaches[pod.Name].sshPorts, SSHPort{
						containerName: container.Name,
						name:          port.Name,
						containerPort: port.ContainerPort,
					})
				}
			}
		}
	}
}

//update pod status
func updatePodStatus(podCaches map[string]*PodCache, waitToAddSet, waitToDelSet set.Set) {
	for podName, podCache := range podCaches {
		//mark pod to waitToAdd
		if waitToAddSet.Contains(podName) {
			podCache.status = waitToAdd
		}
		//mark pod to waitToDel
		if waitToAddSet.Contains(podName) {
			podCache.status = waitToDel
		}
	}
}

func executePodSync(podCaches map[string]*PodCache, waitToAddSet, waitToDelSet set.Set) {
	for podName := range waitToAddSet.Iter() {
		err := addPodToJms(podCaches[podName.(string)])
		if err != nil {
			log.Error(err.Error())
			continue
		}
		podCaches[podName.(string)].status = alreadyAdded
	}
	for podName := range waitToDelSet.Iter() {
		err := delPodFromJms(podCaches[podName.(string)])
		if err != nil {
			log.Error(err.Error())
			continue
		}
		delete(podCaches, podName.(string))
	}
}

func addPodToJms(podCache *PodCache) error {
	podIP := podCache.ip
	podName := podCache.name
	var assetID string
	var err error
	for _, sshPort := range podCache.sshPorts {
		hostname := podName + "_" + sshPort.containerName
		if !jmsCli.HaveAsset(hostname) {
			assetID, err = jmsCli.AddAsset(podIP, hostname, "Linux", sshPort.containerPort)
			if err != nil {
				return err
			}
			podCache.assetID = assetID
		}
		if !jmsCli.HaveAssetUser(username, assetID) {
			err = jmsCli.AddAssetToUser(username, assetID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func delPodFromJms(podCache *PodCache) error {
	podName := podCache.name
	for _, sshPort := range podCache.sshPorts {
		hostname := podName + "_" + sshPort.containerName
		if jmsCli.HaveAsset(hostname) {
			err := jmsCli.DelAsset(hostname)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
