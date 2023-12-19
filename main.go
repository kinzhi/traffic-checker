package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type AppConfig struct {
	healthCheckURL             string
	ingressDeploymentName      string
	ingressDeploymentNamespace string
	monkeyDeploymentName       string
	monkeyDeploymentNamespace  string
	timeoutSeconds             string
	restartInterval            string
	retryCount                 string
	retryInterval              string
	enableHealthCheckBool      bool
	enableHealthCheckCloseBool bool
	enableRestartBool          bool
}

var (
	AppConfigInstance  AppConfig
	log                = logrus.New()
	wg                 sync.WaitGroup
	ingressHealthGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ingress_health",
			Help: "Ingress health status (1 for healthy, 0 for unhealthy).",
		},
		[]string{"namespace", "name"})
)

func init() {
	// 设置 logrus 以输出 JSON 格式的日志
	log.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02T15:04:05.9999",
	})
	// 设置默认时区为上海
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		log.Fatalf("Error loading location: %v", err)
	}
	time.Local = location

	AppConfigInstance.healthCheckURL = getEnv("HEALTH_CHECK_URL", "http://example.com/health")
	AppConfigInstance.ingressDeploymentName = getEnv("INGRESS_DEPLOYMENT_NAME", "your-ingress-deployment")
	AppConfigInstance.ingressDeploymentNamespace = getEnv("INGRESS_DEPLOYMENT_NAMESPACE", "default")
	AppConfigInstance.monkeyDeploymentName = getEnv("MONKEY_DEPLOYMENT_NAME", "your-test-deployment")
	AppConfigInstance.monkeyDeploymentNamespace = getEnv("MONKEY_DEPLOYMENT_NAMESPACE", "default")

	AppConfigInstance.timeoutSeconds = getEnv("TIMEOUT_SECONDS", "30")
	AppConfigInstance.restartInterval = getEnv("RESTART_INTERVAL", "1h")
	AppConfigInstance.retryCount = getEnv("RETRY_COUNT", "3")
	AppConfigInstance.retryInterval = getEnv("RETRY_INTERVAL", "10s")
	enableHealthCheckBool, err := strconv.ParseBool(getEnv("ENABLE_HEALTH_CHECK", "true"))
	if err != nil {
		log.Fatal(err)
	}
	AppConfigInstance.enableHealthCheckBool = enableHealthCheckBool

	enableRestartBool, err := strconv.ParseBool(getEnv("ENABLE_RESTART", "true"))
	if err != nil {
		log.Fatal(err)
	}

	AppConfigInstance.enableRestartBool = enableRestartBool

	enableHealthCheckCloseBool, err := strconv.ParseBool(getEnv("ENABLE_HEALTH_CHECK_CLOSE", "false"))
	if err != nil {
		log.Fatal(err)
	}

	AppConfigInstance.enableHealthCheckCloseBool = enableHealthCheckCloseBool
	// 注册指标
	prometheus.MustRegister(ingressHealthGauge)
}

func main() {

	// 启动HTTP服务器
	startHTTPServer(":8080")
	// 注册Prometheus指标
	http.Handle("/metrics", promhttp.Handler())
	log.Info("Prometheus metrics server started on /metrics")

	// 使用 Service Account 创建 Kubernetes 客户端
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error creating in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}

	// 定时检查健康状态
	if AppConfigInstance.enableHealthCheckBool {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkHealth(clientset)
		}()
	}

	// 定时重启Monkey Deploymentx
	if AppConfigInstance.enableRestartBool {
		wg.Add(1)
		go func() {
			defer wg.Done()
			restartMonkeyDeployment(clientset)
		}()
	}

	// 等待所有协程完成后退出
	wg.Wait()
}

func startHTTPServer(addr string) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Daocloud Ingress Checker\n")
	})
	go func() {
		log.Infof("Server started on %s", addr)
		log.Fatal(http.ListenAndServe(addr, nil))
	}()
}

func checkHealth(clientset *kubernetes.Clientset) {

	retryIntervalDuration, err := time.ParseDuration(AppConfigInstance.retryInterval)
	if err != nil {
		log.Fatalf("Error parsing retry interval duration: %v", err)
	}

	retryCountInt, err := strconv.Atoi(AppConfigInstance.retryCount)
	if err != nil {
		log.Fatalf("Error parsing retry count: %v", err)
	}

	for {
		// time.Sleep(retryIntervalDuration) // 每次检查间隔时间
		log.Infof("[%s] Checking health with retry...", time.Now().Format(time.RFC3339))

		// 执行带重试的健康检查
		err := retryCheckHealth(clientset, retryCountInt, retryIntervalDuration)
		if err != nil {
			// 如果不健康,则关闭 Ingress Deployment
			// 如果不健康，设置指标值为 0
			ingressHealthGauge.WithLabelValues(AppConfigInstance.ingressDeploymentNamespace, AppConfigInstance.ingressDeploymentName).Set(0)
			log.Infof("[%s] Deployment is not healthy.", time.Now().Format(time.RFC3339))
			if AppConfigInstance.enableHealthCheckCloseBool {
				log.Infof("[%s]Deployment is not healthy. Closing...", time.Now().Format(time.RFC3339))
				closeIngressDeployment(clientset)
			}
		} else {
			// 如果健康，设置指标值为 1
			ingressHealthGauge.WithLabelValues(AppConfigInstance.ingressDeploymentNamespace, AppConfigInstance.ingressDeploymentName).Set(1)
			log.Infof("[%s] Destination address is healthy.", time.Now().Format(time.RFC3339))
		}
	}
}

func restartMonkeyDeployment(clientset *kubernetes.Clientset) {
	restartIntervalDuration, err := time.ParseDuration(AppConfigInstance.restartInterval)
	if err != nil {
		log.Fatalf("Error parsing restart interval duration: %v", err)
	}

	for {
		log.Infof("[%s] Restarting Monkey Deployment...", time.Now().Format(time.RFC3339))
		err := restartDeployment(clientset, AppConfigInstance.monkeyDeploymentName)
		if err != nil {
			log.Infof("[%s] Error restarting Monkey Deployment: %v", time.Now().Format(time.RFC3339), err)
		}
		time.Sleep(restartIntervalDuration) // 每隔指定时间重启一次
	}
}

func closeIngressDeployment(clientset *kubernetes.Clientset) {
	// 在这里执行关闭 Ingress Deployment 的操作
	err := scaleDeployment(clientset, AppConfigInstance.ingressDeploymentNamespace, AppConfigInstance.ingressDeploymentName, 0)
	if err != nil {
		log.Infof("[%s] Error closing Ingress Deployment: %v", time.Now().Format(time.RFC3339), err)
	}
}

func restartDeployment(clientset *kubernetes.Clientset, deploymentName string) error {
	// 在这里执行重启 Deployment 的操作
	return scaleDeployment(clientset, AppConfigInstance.monkeyDeploymentNamespace, deploymentName, -1)
}

func scaleDeployment(clientset *kubernetes.Clientset, namespace, deploymentName string, replicas int32) error {
	deploymentClient := clientset.AppsV1().Deployments(namespace)
	deployment, err := deploymentClient.Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	currentReplicas := *deployment.Spec.Replicas
	log.Infof("Current replicas for Deployment %s in namespace %s: %d", deploymentName, namespace, currentReplicas)

	if replicas == -1 {
		// // 如果 replicas 为 -1，表示需要执行 Rolling Restart

		if deployment.Spec.Template.ObjectMeta.Annotations == nil {
			deployment.Spec.Template.ObjectMeta.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.ObjectMeta.Annotations["traffic-checker/restartedAt"] = time.Now().Format(time.RFC3339)

		_, err = deploymentClient.Update(context.TODO(), deployment, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		log.Infof("Deployment %s in namespace %s restarted,Added %s ", deploymentName, namespace, deployment.Spec.Template.ObjectMeta.Annotations["traffic-checker/restartedAt"])
		return nil
	} else if currentReplicas == 0 && replicas == 0 {
		log.Info("Deployment replicas are already 0. No action needed.")
		return nil
	} else {
		deployment.Spec.Replicas = &replicas
		_, err = deploymentClient.Update(context.TODO(), deployment, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		log.Infof("Deployment %s in namespace %s scaled to %d replicas", deploymentName, namespace, replicas)
		return nil
	}

}

func isHealthy() (bool, error) {
	timeoutSeconds, err := strconv.Atoi(AppConfigInstance.timeoutSeconds)
	if err != nil {
		return false, fmt.Errorf("invalid timeoutSeconds: %v", err)
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutSeconds) * time.Second,
	}

	resp, err := client.Get(AppConfigInstance.healthCheckURL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func retryCheckHealth(clientset *kubernetes.Clientset, retryCount int, retryInterval time.Duration) error {

	for i := 0; i < retryCount; i++ {
		// 执行健康检查
		ok, err := isHealthy()
		if err == nil && ok {
			return nil
		}
		log.Infof("Health check failed (attempt %d/%d): %v", i+1, retryCount, err)
		time.Sleep(retryInterval)
	}

	return fmt.Errorf("Health check failed after %d attempts", retryCount)
}
