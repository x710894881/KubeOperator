package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/KubeOperator/KubeOperator/pkg/constant"
	"github.com/KubeOperator/KubeOperator/pkg/db"
	"github.com/KubeOperator/KubeOperator/pkg/dto"
	"github.com/KubeOperator/KubeOperator/pkg/logger"
	"github.com/KubeOperator/KubeOperator/pkg/model"
	"github.com/KubeOperator/KubeOperator/pkg/repository"
	"github.com/KubeOperator/KubeOperator/pkg/service/cluster/tools"
	helm2 "github.com/KubeOperator/KubeOperator/pkg/util/helm"
	kubernetesUtil "github.com/KubeOperator/KubeOperator/pkg/util/kubernetes"
	appv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterToolService interface {
	List(clusterName string) ([]dto.ClusterTool, error)
	GetNodePort(clusterName, toolName, toolVersion, namespace string) (dto.ClusterTool, error)
	SyncStatus(clusterName string) ([]dto.ClusterTool, error)
	Enable(clusterName string, tool dto.ClusterTool) (dto.ClusterTool, error)
	Upgrade(clusterName string, tool dto.ClusterTool) (dto.ClusterTool, error)
	Disable(clusterName string, tool dto.ClusterTool) (dto.ClusterTool, error)
}

func NewClusterToolService() ClusterToolService {
	return &clusterToolService{
		toolRepo:       repository.NewClusterToolRepository(),
		clusterService: NewClusterService(),
	}
}

type clusterToolService struct {
	toolRepo       repository.ClusterToolRepository
	clusterService ClusterService
}

func (c clusterToolService) List(clusterName string) ([]dto.ClusterTool, error) {
	var items []dto.ClusterTool
	ms, err := c.toolRepo.List(clusterName)
	if err != nil {
		return items, err
	}
	for _, m := range ms {
		d := dto.ClusterTool{ClusterTool: m}
		d.Vars = map[string]interface{}{}
		_ = json.Unmarshal([]byte(m.Vars), &d.Vars)
		items = append(items, d)
	}
	return items, nil
}

func (c clusterToolService) GetNodePort(clusterName, toolName, toolVersion, namespace string) (dto.ClusterTool, error) {
	var (
		cluster model.Cluster
		tool    dto.ClusterTool
		svcName string
	)
	if err := db.DB.Where("name = ?", clusterName).Preload("Spec").Preload("Secret").Find(&cluster).Error; err != nil {
		return tool, err
	}
	kubeClient, err := kubernetesUtil.NewKubernetesClient(&kubernetesUtil.Config{
		Hosts: []kubernetesUtil.Host{kubernetesUtil.Host(fmt.Sprintf("%s:%d", cluster.Spec.KubeRouter, cluster.Spec.KubeApiServerPort))},
		Token: cluster.Secret.KubernetesToken,
	})
	if err != nil {
		return tool, err
	}
	switch toolName {
	case "kubepi":
		svcName = "kubepi"
	case "prometheus":
		svcName = "prometheus-server"
	}
	d, err := kubeClient.CoreV1().Services(namespace).Get(context.TODO(), svcName, metav1.GetOptions{})
	if err != nil {
		return tool, err
	}
	if len(d.Spec.Ports) != 0 {
		tool.NodePort = fmt.Sprint(d.Spec.Ports[0].NodePort)
		return tool, nil
	}
	return tool, fmt.Errorf("can't get nodeport %s(%s) from cluster %s", svcName, namespace, clusterName)
}

func (c clusterToolService) SyncStatus(clusterName string) ([]dto.ClusterTool, error) {
	var (
		cluster   model.Cluster
		tools     []model.ClusterTool
		backTools []dto.ClusterTool
	)
	if err := db.DB.Where("name = ?", clusterName).Preload("Spec").Preload("Secret").Find(&cluster).Error; err != nil {
		return backTools, err
	}
	if err := db.DB.Where("cluster_id = ?", cluster.ID).Find(&tools).Error; err != nil {
		return backTools, err
	}
	kubeClient, err := kubernetesUtil.NewKubernetesClient(&kubernetesUtil.Config{
		Hosts: []kubernetesUtil.Host{kubernetesUtil.Host(fmt.Sprintf("%s:%d", cluster.Spec.KubeRouter, cluster.Spec.KubeApiServerPort))},
		Token: cluster.Secret.KubernetesToken,
	})
	if err != nil {
		return backTools, err
	}
	var (
		allDeployments  []appv1.Deployment
		allStatefulsets []appv1.StatefulSet
	)
	namespaceList, err := kubeClient.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return backTools, err
	}
	for _, ns := range namespaceList.Items {
		deployments, err := kubeClient.AppsV1().Deployments(ns.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return backTools, err
		}
		allDeployments = append(allDeployments, deployments.Items...)
		statefulsets, err := kubeClient.AppsV1().StatefulSets(ns.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return backTools, err
		}
		allStatefulsets = append(allStatefulsets, statefulsets.Items...)
	}
	for _, tool := range tools {
		dtoItem := dto.ClusterTool{
			ClusterTool: tool,
			Vars:        map[string]interface{}{},
		}
		isEnable := false
		sourceName := ""
		sourceType := "deployment"
		switch tool.Name {
		case "registry":
			sourceName = constant.DefaultRegistryDeploymentName
		case "chartmuseum":
			sourceName = constant.DefaultChartmuseumDeploymentName
		case "kubepi":
			sourceName = constant.DefaultKubePiDeploymentName
		case "kubeapps":
			sourceName = constant.DefaultKubeappsDeploymentName
		case "grafana":
			sourceName = constant.DefaultGrafanaDeploymentName
		case "prometheus":
			sourceName = constant.DefaultPrometheusDeploymentName
		case "logging":
			sourceName = constant.DefaultLoggingStateSetsfulName
			sourceType = "statefulset"
		case "loki":
			sourceName = constant.DefaultLokiStateSetsfulName
			sourceType = "statefulset"
		}
		if sourceType == "deployment" {
			for _, deploy := range allDeployments {
				if deploy.ObjectMeta.Name == sourceName {
					if deploy.Status.ReadyReplicas > 0 {
						isEnable = true
						tool.Status = constant.StatusRunning
					} else {
						tool.Status = constant.StatusWaiting
					}
					dtoItem.Vars["namespace"] = deploy.ObjectMeta.Namespace
					buf, _ := json.Marshal(&dtoItem.Vars)
					tool.Vars = string(buf)
					_ = db.DB.Model(&model.ClusterTool{}).Updates(&tool)
					break
				}
			}
		}
		if sourceType == "statefulset" {
			for _, statefulset := range allStatefulsets {
				if statefulset.ObjectMeta.Name == sourceName {
					if statefulset.Status.ReadyReplicas > 0 {
						isEnable = true
						tool.Status = constant.StatusRunning
					} else {
						tool.Status = constant.StatusWaiting
					}
					dtoItem.Vars["namespace"] = statefulset.ObjectMeta.Namespace
					buf, _ := json.Marshal(&dtoItem.Vars)
					tool.Vars = string(buf)
					_ = db.DB.Model(&model.ClusterTool{}).Updates(&tool)
					break
				}
			}
		}
		if !isEnable {
			if tool.Status != constant.StatusWaiting {
				tool.Status = constant.StatusWaiting
				_ = db.DB.Model(&model.ClusterTool{}).Updates(&tool)
			}
		}
		dtoItem.ClusterTool = tool
		backTools = append(backTools, dtoItem)
	}

	var h helm2.Client
	err = h.SyncRepoCharts(cluster.Spec.Architectures)
	return backTools, err
}

func (c clusterToolService) Disable(clusterName string, tool dto.ClusterTool) (dto.ClusterTool, error) {
	cluster, hosts, err := c.getBaseParams(clusterName)
	if err != nil {
		return tool, err
	}

	tool.ClusterID = cluster.ID
	mo := tool.ClusterTool
	buf, _ := json.Marshal(&tool.Vars)
	mo.Vars = string(buf)
	tool.ClusterTool = mo

	itemValue, ok := tool.Vars["namespace"]
	namespace := ""
	if !ok {
		namespace = constant.DefaultNamespace
	} else {
		namespace = itemValue.(string)
	}

	ct, err := tools.NewClusterTool(&tool.ClusterTool, cluster, hosts, namespace, namespace, false)
	if err != nil {
		return tool, err
	}
	mo.Status = constant.ClusterTerminating
	_ = c.toolRepo.Save(&mo)
	go c.doUninstall(ct, &tool.ClusterTool)
	return tool, nil
}

func (c clusterToolService) Enable(clusterName string, tool dto.ClusterTool) (dto.ClusterTool, error) {
	cluster, hosts, err := c.getBaseParams(clusterName)
	if err != nil {
		return tool, err
	}

	var toolDetail model.ClusterToolDetail
	if err := db.DB.Where("name = ? AND version = ?", tool.Name, tool.Version).Find(&toolDetail).Error; err != nil {
		return tool, err
	}

	tool.ClusterID = cluster.ID
	mo := tool.ClusterTool
	buf, _ := json.Marshal(&tool.Vars)
	mo.Vars = string(buf)
	tool.ClusterTool = mo

	kubeClient, err := kubernetesUtil.NewKubernetesClient(&kubernetesUtil.Config{
		Hosts: hosts,
		Token: cluster.Secret.KubernetesToken,
	})
	if err != nil {
		return tool, err
	}
	oldNamespace, namespace := c.getNamespace(cluster.ID, tool)
	ns, _ := kubeClient.CoreV1().Namespaces().Get(context.TODO(), namespace, metav1.GetOptions{})
	if ns.ObjectMeta.Name == "" {
		n := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		_, err = kubeClient.CoreV1().Namespaces().Create(context.TODO(), n, metav1.CreateOptions{})
		if err != nil {
			return tool, err
		}
	}
	ct, err := tools.NewClusterTool(&tool.ClusterTool, cluster, hosts, oldNamespace, namespace, true)
	if err != nil {
		return tool, err
	}
	mo.Status = constant.ClusterInitializing
	_ = c.toolRepo.Save(&mo)
	go c.doInstall(ct, &tool.ClusterTool, toolDetail)
	return tool, nil
}

func (c clusterToolService) Upgrade(clusterName string, tool dto.ClusterTool) (dto.ClusterTool, error) {
	cluster, hosts, err := c.getBaseParams(clusterName)
	if err != nil {
		return tool, err
	}

	var toolDetail model.ClusterToolDetail
	if err := db.DB.Where("name = ? AND version = ?", tool.Name, tool.HigherVersion).Find(&toolDetail).Error; err != nil {
		return tool, err
	}

	tool.ClusterID = cluster.ID
	mo := tool.ClusterTool
	buf, _ := json.Marshal(&tool.Vars)
	mo.Vars = string(buf)
	mo.Status = constant.ClusterUpgrading
	mo.Version = mo.HigherVersion
	mo.HigherVersion = ""
	tool.ClusterTool = mo

	itemValue, ok := tool.Vars["namespace"]
	namespace := ""
	if !ok {
		namespace = constant.DefaultNamespace
	} else {
		namespace = itemValue.(string)
	}
	ct, err := tools.NewClusterTool(&tool.ClusterTool, cluster, hosts, namespace, namespace, true)
	if err != nil {
		return tool, err
	}

	_ = c.toolRepo.Save(&mo)
	go c.doUpgrade(ct, &tool.ClusterTool, toolDetail)
	return tool, nil
}

func (c clusterToolService) doInstall(p tools.Interface, tool *model.ClusterTool, toolDetail model.ClusterToolDetail) {
	err := p.Install(toolDetail)
	if err != nil {
		logger.Log.Errorf("install tool %s failed: %+v", tool.Name, err)
		tool.Status = constant.ClusterFailed
		tool.Message = err.Error()
	} else {
		logger.Log.Infof("install tool %s successful: %+v", tool.Name, err)
		tool.Status = constant.ClusterRunning
	}
	_ = c.toolRepo.Save(tool)
}

func (c clusterToolService) doUpgrade(p tools.Interface, tool *model.ClusterTool, toolDetail model.ClusterToolDetail) {
	err := p.Upgrade(toolDetail)
	if err != nil {
		logger.Log.Errorf("upgrade tool %s failed: %+v", tool.Name, err)
		tool.Status = constant.ClusterFailed
		tool.Message = err.Error()
	} else {
		logger.Log.Infof("upgrade tool %s successful: %+v", tool.Name, err)
		tool.Status = constant.ClusterRunning
	}
	_ = c.toolRepo.Save(tool)
}

func (c clusterToolService) doUninstall(p tools.Interface, tool *model.ClusterTool) {
	if err := p.Uninstall(); err != nil {
		logger.Log.Errorf("uninstall %s failed: %+v", tool.Name, err)
	} else {
		logger.Log.Infof("uninstall tool %s successful: %+v", tool.Name, err)
	}
	tool.Status = constant.ClusterWaiting
	_ = c.toolRepo.Save(tool)
}

func (c clusterToolService) getNamespace(clusterID string, tool dto.ClusterTool) (string, string) {
	namespace := ""
	Sp, ok := tool.Vars["namespace"]
	if !ok {
		namespace = constant.DefaultNamespace
	} else {
		namespace = Sp.(string)
	}
	var oldTools model.ClusterTool
	if err := db.DB.Where("cluster_id = ? AND name = ?", clusterID, tool.Name).First(&oldTools).Error; err != nil {
		return namespace, namespace
	}
	oldVars := map[string]interface{}{}
	_ = json.Unmarshal([]byte(oldTools.Vars), &oldVars)
	oldSp, ok := oldVars["namespace"]
	if !ok {
		return namespace, namespace
	} else {
		return oldSp.(string), namespace
	}
}

func (c clusterToolService) getBaseParams(clusterName string) (model.Cluster, []kubernetesUtil.Host, error) {
	var (
		cluster model.Cluster
		host    []kubernetesUtil.Host
		err     error
	)
	if err := db.DB.Where("name = ?", clusterName).Preload("Spec").Preload("Secret").Find(&cluster).Error; err != nil {
		return cluster, host, err
	}

	host, err = c.clusterService.GetApiServerEndpoints(clusterName)
	if err != nil {
		return cluster, host, err
	}

	return cluster, host, nil
}
