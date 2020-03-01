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
	`%{color}%{time:15:04:05.000} %{shortfile} %{shortfunc} ▶ %{level:.4s} %{id:03x}%{color:reset} %{message}`,
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
	flag.StringVar(&label, "label", "ssh.port/open=true", "watch label")
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
		err := syncPodToJms0()
		if err != nil {
			log.Error(err.Error())
		}
		time.Sleep(interval)
	}
}

func syncPodToJms0() error {
	log.Info("Starting pod collection...")
	pods, err := kubeCli.GetPods(namespace, label, 65535)
	if err != nil {
		return err
	}
	pods = filterPodsWhichHaveSSHPorts(pods)
	log.Infof("Collected %d pods which have ssh ports in k8s cluster", len(pods))
	foundSet := set.NewSet()
	for _, pod := range pods {
		foundSet.Add(pod.Name)
	}
	assets, err := jmsCli.ListAssets(0, 65535)
	log.Infof("Collected %d assets in jumpserver", len(assets))
	if err != nil {
		return err
	}
	lostSet := set.NewSet()
	stillSet := set.NewSet()
	for _, asset := range assets {
		hostname := asset.Hostname
		podName := strings.Split(hostname, "_")[0]
		if !foundSet.Contains(podName) {
			lostSet.Add(podName)
		} else {
			stillSet.Add(podName)
		}
	}
	foundSet = foundSet.Difference(stillSet)
	log.Infof("There are %d pods wait to add to jumpserver", foundSet.Cardinality())
	log.Infof("There are %d pods wait to del from jumpserver", lostSet.Cardinality())
	log.Infof("There are %d pods already added to jumpserver", stillSet.Cardinality())
	refreshPodCaches(pods, podCaches, foundSet, lostSet, stillSet)
	executePodSync(podCaches)
	return nil
}

func filterPodsWhichHaveSSHPorts(pods []v1.Pod) []v1.Pod {
	res := []v1.Pod{}
	for _, pod := range pods {
		isFoundSSHPort := false
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if strings.HasPrefix(port.Name, sshPortNamePrefix) {
					isFoundSSHPort = true
				}
			}
		}
		if isFoundSSHPort {
			res = append(res, pod)
		}
	}
	return res
}

func refreshPodCaches(pods []v1.Pod, podCaches map[string]*PodCache, foundSet, lostSet, stillSet set.Set) {
	for _, pod := range pods {
		if podCaches[pod.Name] != nil {
			continue
		}
		podCaches[pod.Name] = &PodCache{}
		isFoundSSHPort := false
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if strings.HasPrefix(port.Name, sshPortNamePrefix) {
					isFoundSSHPort = true
					podCaches[pod.Name].ip = pod.Status.PodIP
					podCaches[pod.Name].name = pod.Name
					if lostSet.Contains(pod.Name) {
						podCaches[pod.Name].status = waitToDel
						log.Infof("Mark pod %s's status \"waitToDel\"", pod.Name)
					} else if stillSet.Contains(pod.Name) {
						podCaches[pod.Name].status = alreadyAdded
						log.Infof("Mark pod %s's status \"alreadyAdded\"", pod.Name)
					} else if foundSet.Contains(pod.Name) {
						podCaches[pod.Name].status = waitToAdd
						log.Infof("Mark pod %s's status \"waitToAdd\"", pod.Name)
					} else {
						log.Infof("Detected strange pod %s, which is not belong to any set", pod.Name)
					}
					podCaches[pod.Name].sshPorts = append(podCaches[pod.Name].sshPorts, SSHPort{
						containerName: container.Name,
						name:          port.Name,
						containerPort: port.ContainerPort,
					})
					log.Infof("Append [%s::%s::%s::%d] to pod caches", pod.Name, container.Name, port.Name, port.ContainerPort)
				}
			}
		}
		if !isFoundSSHPort {
			delete(podCaches, pod.Name)
			log.Warningf("Can not found ssh port in pod %s, but was collected", pod.Name)
		}
	}
}

func executePodSync(podCaches map[string]*PodCache) {
	for podName, podCache := range podCaches {
		if podCache.status == waitToAdd {
			err := addPodToJms(podCache)
			if err != nil {
				log.Errorf("Add pod %s to jumpserver failed", podName)
				log.Error(err.Error())
				continue
			}
			podCache.status = alreadyAdded
			log.Infof("Add pod %s to jumpserver successed", podName)
			log.Infof("Mark pod %s's status \"alreadyAdded\"", podName)
		}
		if podCache.status == waitToDel {
			err := delPodFromJms(podCache)
			if err != nil {
				log.Errorf("Delete pod %s from jumpserver failed", podName)
				log.Error(err.Error())
				continue
			}
			delete(podCaches, podName)
			log.Infof("Delete pod %s from jumpserver successed", podName)
			log.Infof("Delete Pod %s from pod caches", podName)
		}
	}
}

func addPodToJms(podCache *PodCache) error {
	podIP := podCache.ip
	podName := podCache.name
	var assetID string
	for _, sshPort := range podCache.sshPorts {
		hostname := podName + "_" + sshPort.containerName
		hasAsset, err := jmsCli.HasAsset(hostname)
		if err != nil {
			log.Errorf(err.Error())
			continue
		}
		if !hasAsset {
			assetID, err = jmsCli.AddAsset(podIP, hostname, "Linux", sshPort.containerPort)
			if err != nil {
				return err
			}
			podCache.assetID = assetID
		} else {
			podCache.status = alreadyAdded
			log.Warningf("Asset %s already exists in jumpserver, mark pod %s to alreadyAdded", hostname, podName)
		}
	}
	return nil
}

func delPodFromJms(podCache *PodCache) error {
	podName := podCache.name
	for _, sshPort := range podCache.sshPorts {
		hostname := podName + "_" + sshPort.containerName
		hasAsset, err := jmsCli.HasAsset(hostname)
		if err != nil {
			log.Error(err.Error())
			return err
		}
		if hasAsset {
			err := jmsCli.DelAsset(hostname)
			if err != nil {
				return err
			}
		} else {
			log.Warningf("Asset %s does not exist in jumpserver", hostname)
		}
	}
	return nil
}
