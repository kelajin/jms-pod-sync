package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	set "github.com/deckarep/golang-set"

	"github.com/kelajin/jms-pod-sync/jumpserver"
	"github.com/kelajin/jms-pod-sync/kubernetes"
	jms "github.com/kelajin/jumpserver-client-go"
	v1 "k8s.io/api/core/v1"

	"github.com/gin-gonic/gin"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/op/go-logging"
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
)

const (
	waitToAdd int32 = iota
	alreadyAdded
	waitToDel
)
const nameSep = "__"

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
	}
	if !strings.HasPrefix(host, "http://") {
		host = "http://" + host
	}
	if !strings.HasSuffix(host, "/api/v1") {
		host = host + "/api/v1"
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
	pods, err := kubeCli.GetPods(namespace, label, 65535)
	if err != nil {
		return err
	}
	assetsFromKube, err := convertPodsToAssets(pods)
	if err != nil {
		return err
	}
	log.Infof("Generated %d assets from pods of k8s which have ssh ports", len(assetsFromKube))
	assetsFromJms, err := jmsCli.ListAssets(0, 65535)
	log.Infof("There are %d assets in jumpserver", len(assetsFromJms))
	assetsFromKubeNameSet := getAssetsNameSet(assetsFromKube)
	assetsFromJmsNameSet := getAssetsNameSet(assetsFromJms)
	stillAssetsNameSet := assetsFromKubeNameSet.Intersect(assetsFromJmsNameSet)
	foundAssetNameSet := assetsFromKubeNameSet.Difference(stillAssetsNameSet)
	lostAssetNameSet := assetsFromJmsNameSet.Difference(stillAssetsNameSet)
	log.Infof("There are %d found asset and %d lost asset and %d still asset", foundAssetNameSet.Cardinality(), lostAssetNameSet.Cardinality(), stillAssetsNameSet.Cardinality())
	addFoundAssetToJms(assetsFromKube, foundAssetNameSet)
	delLostAssetFromJms(assetsFromJms, lostAssetNameSet)
	return nil
}

func addFoundAssetToJms(assetsFromKube []jms.Asset, foundAssetNameSet set.Set) {
	for _, asset := range assetsFromKube {
		if foundAssetNameSet.Contains(asset.Hostname) {
			_, err := jmsCli.AddAsset(asset)
			if err != nil {
				log.Error(err.Error())
			} else {
				log.Infof("Add pod %s to jumpserver successed", asset.Hostname)
			}
		}
	}
}
func delLostAssetFromJms(assetsFromJms []jms.Asset, lostAssetNameSet set.Set) {
	for _, asset := range assetsFromJms {
		if lostAssetNameSet.Contains(asset.Hostname) {
			err := jmsCli.DelAsset(asset)
			if err != nil {
				log.Error(err.Error())
			} else {
				log.Infof("Del pod %s from jumpserver successed", asset.Hostname)
			}
		}
	}
}

func getAssetsNameSet(assets []jms.Asset) set.Set {
	nameSet := set.NewSet()
	for _, asset := range assets {
		nameSet.Add(asset.Hostname)
	}
	return nameSet
}

func convertPodsToAssets(pods []v1.Pod) ([]jms.Asset, error) {
	assets := []jms.Asset{}
	for _, pod := range pods {
		namePrefix := pod.Name
		ip := pod.Status.PodIP
		for _, container := range pod.Spec.Containers {
			nameMiddle := container.Name
			for _, port := range container.Ports {
				nameSuffix := port.Name
				if !strings.HasPrefix(nameSuffix, sshPortNamePrefix) {
					continue
				}
				sshPort := port.ContainerPort
				assetName := namePrefix + nameSep + nameMiddle + nameSep + nameSuffix
				asset := jms.Asset{
					Ip:       ip,
					Hostname: assetName,
					Port:     sshPort,
					Platform: "Linux",
					Comment:  fmt.Sprintf("%s:%s:%s", assetName, ip, sshPort),
				}
				assets = append(assets, asset)
			}
		}
	}
	return assets, nil
}
