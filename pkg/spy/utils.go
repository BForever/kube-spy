package spy

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/golang/glog"
	"github.com/huanwei/kube-chaos/pkg/exec"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"strconv"
	"strings"
	"time"
)

func GetConfig() *Config {
	// Read config file
	configFile, err := ioutil.ReadFile("/spy/spy.conf")
	if err != nil {
		glog.Errorf("Fail to open spy config : %v", err)
		glog.Flush()
		panic(err.Error())
	}
	decoder := yaml.NewDecoder(bytes.NewReader(configFile))

	// Decode config file
	spyConfig := &Config{}
	decoder.Decode(spyConfig)
	glog.Infof("decoded:\n%v", spyConfig)
	glog.Flush()

	return spyConfig
}

func GetClientset(kubeconfig string) *kubernetes.Clientset {
	// Uses the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		glog.Errorf("Fail to build config from flags : %v", err)
		glog.Flush()
		panic(err.Error())
	}

	// Creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Errorf("Fail to create the clientset : %v", err)
		glog.Flush()
		panic(err.Error())
	}

	return clientset
}

func GetServices(clientset *kubernetes.Clientset, config *Config) []*v1.Service {
	// Create service array
	services := make([]*v1.Service, len(config.VictimServices))
	var err error
	// Get services
	for i, service := range config.VictimServices {
		services[i], err = clientset.CoreV1().Services(config.Namespace).Get(service.Name, meta_v1.GetOptions{})
		if err != nil {
			glog.Errorf("Fail to get service %s : %s", service.Name, err)
			glog.Flush()
			panic(err.Error())
		}
	}
	return services
}

func GetService(clientset *kubernetes.Clientset, config *Config, serviceName string) (*v1.Service, error) {
	service, err := clientset.CoreV1().Services(config.Namespace).Get(serviceName, meta_v1.GetOptions{})
	if err != nil {
		err = errors.New(fmt.Sprintf("Fail to get service %s : %s", service.Name, err))
		return nil, err
	}
	return service, nil
}

func GetPods(clientset *kubernetes.Clientset, service *v1.Service) *v1.PodList {
	// Find pods' selector
	labelSelector := ""
	for selector, value := range service.Spec.Selector {
		if labelSelector == "" {
			labelSelector += selector + "=" + value
		} else {
			labelSelector += "," + selector + "=" + value
		}
	}
	// Get pods using selectors
	var (
		pods *v1.PodList
		err  error
	)

	for {
		pods, err = clientset.CoreV1().Pods("").List(meta_v1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			glog.Errorf(fmt.Sprintf("Failed to get pods of service %s:%s", service.Name, err))
		}
		break
	}

	return pods
}

func GetPodsInfo(pods *v1.PodList) (cidrs, podNames []string) {
	// get all pods' ip and names
	for _, pod := range pods.Items {
		cidrs = append(cidrs, pod.Status.PodIP)
		podNames = append(podNames, pod.Name)
	}
	return cidrs, podNames
}

func PingPods(serviceName, namespace string, podNames, cidrs []string, chaos *Chaos, stop, complete chan bool) {
	finished := make(chan bool, len(cidrs))

	for i := range cidrs {
		go PingPod(serviceName, namespace, podNames[i], cidrs[i], chaos, finished, stop)
	}
	for range cidrs {
		<-finished
	}
	SendPingResults()
	complete <- true
}

func PingPod(serviceName, namespace, podName, cidr string, chaos *Chaos, finished chan bool, stop chan bool) {
	var (
		loss      string
		delay     string
		output    string
		index     int
		data      []byte
		err       error
		timestamp time.Time
	)

	e := exec.New()
	for {
		// Ping ip of pod 100 times in 1 sec
		timestamp = time.Now()
		data, err = e.Command("ping", "-i", "0.001", "-c", "100", "-q", cidr).CombinedOutput()
		timestamp = time.Now().Add(timestamp.Sub(time.Now()) / 2)
		if err != nil {
			glog.Infof(fmt.Sprintf("Failed to ping %s:%s", cidr, err))
			loss = "100%"
			delay = "Timeout"
		} else {
			output = string(data)
			index = strings.Index(output, "%")
			loss = output[index-1:index] + "%%"
			index = strings.Index(output, "rtt")
			delay = output[index:]
		}
		glog.Infof(fmt.Sprintf("ping %s loss:%s %s", cidr, loss, delay))
		AddPingResult(serviceName, namespace, chaos, podName, delay, loss, timestamp)
		if len(stop) == 1 {
			break
		}
	}
	finished <- true
}

func GetPartPods(podList *v1.PodList, Range string) []v1.Pod {
	var (
		err error
		num int
	)

	// Default value: all pods
	num = len(podList.Items)
	// If set, get part of the pods to do chaos
	if Range != "" {
		// Percentage
		if Range[len(Range)-1] == '%' {
			var percent float32
			_, err = fmt.Sscanf(Range, "%f", &percent)
			if err == nil {
				// Check value
				if percent < 0 || percent > 100 {
					err = errors.New("percentage out of range")
				} else {
					num = int(percent * float32(len(podList.Items)) / 100)
				}
			}
		} else {
			// Integer
			num, err = strconv.Atoi(Range)
			if err == nil && num > len(podList.Items) {
				err = errors.New("range larger than total pods number")
			}
		}
	}
	if err != nil {
		glog.Errorf("Invalid chaos pod range [%s] : %s", Range, err)
		// Default value: all pods
		num = len(podList.Items)
	}

	glog.V(3).Infof("Selected pods num: %d", num)
	return podList.Items[:num]
}
