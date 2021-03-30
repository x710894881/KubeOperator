package service

import (
	"errors"

	"github.com/KubeOperator/KubeOperator/pkg/constant"
	"github.com/KubeOperator/KubeOperator/pkg/controller/page"
	"github.com/KubeOperator/KubeOperator/pkg/db"
	"github.com/KubeOperator/KubeOperator/pkg/dto"
	"github.com/KubeOperator/KubeOperator/pkg/model"
	"github.com/KubeOperator/KubeOperator/pkg/model/common"
	"github.com/KubeOperator/KubeOperator/pkg/repository"
	"github.com/jinzhu/gorm"
)

type ProjectResourceService interface {
	Batch(op dto.ProjectResourceOp) error
	Page(num, size int, projectName string, resourceType string) (*page.Page, error)
	GetResources(resourceType, projectName string) (interface{}, error)
	GetResourceTree() ([]dto.ProjectResourceTree, error)
}

type projectResourceService struct {
	projectResourceRepo repository.ProjectResourceRepository
	projectRepo         repository.ProjectRepository
}

func NewProjectResourceService() ProjectResourceService {
	return &projectResourceService{
		projectResourceRepo: repository.NewProjectResourceRepository(),
		projectRepo:         repository.NewProjectRepository(),
	}
}

func (p projectResourceService) Page(num, size int, projectName string, resourceType string) (*page.Page, error) {
	var page page.Page
	pj, err := p.projectRepo.Get(projectName)
	if err != nil {
		return nil, err
	}
	total, mos, err := p.projectResourceRepo.PageByProjectIdAndType(num, size, pj.ID, resourceType)
	if err != nil {
		return nil, err
	}
	var resourceIds []string
	for _, mo := range mos {
		resourceIds = append(resourceIds, mo.ResourceID)
	}

	if len(resourceIds) > 0 {
		switch resourceType {
		case constant.ResourceHost:
			var hosts []model.Host
			err = db.DB.Where("id in (?)", resourceIds).Preload("Cluster").Preload("Zone").Find(&hosts).Error
			if err != nil {
				return nil, err
			}

			var result []dto.Host
			for _, mo := range hosts {
				hostDTO := dto.Host{
					Host:        mo,
					ClusterName: mo.Cluster.Name,
					ZoneName:    mo.Zone.Name,
				}
				result = append(result, hostDTO)
			}
			page.Items = result
		case constant.ResourcePlan:
			var result []model.Plan
			err = db.DB.Where("id in (?)", resourceIds).Find(&result).Error
			if err != nil {
				return nil, err
			}
			page.Items = result
		case constant.ResourceBackupAccount:
			var result []model.BackupAccount
			err = db.DB.Where("id in (?)", resourceIds).Find(&result).Error
			if err != nil {
				return nil, err
			}
			page.Items = result
		default:
			return nil, err
		}

		page.Total = total
	}

	return &page, err
}

func (p projectResourceService) Batch(op dto.ProjectResourceOp) error {
	var opItems []model.ProjectResource
	for _, item := range op.Items {

		var resourceId string
		switch item.ResourceType {
		case constant.ResourceHost:
			host, err := NewHostService().Get(item.ResourceName)
			if err != nil {
				return err
			}
			resourceId = host.ID
			if host.ClusterID != "" {
				return errors.New("DELETE_HOST_FAILED_BY_CLUSTER")
			}
		case constant.ResourcePlan:
			plan, err := NewPlanService().Get(item.ResourceName)
			if err != nil {
				return err
			}
			resourceId = plan.ID
		case constant.ResourceBackupAccount:
			plan, err := NewBackupAccountService().Get(item.ResourceName)
			if err != nil {
				return err
			}
			resourceId = plan.ID
		}

		var itemId string
		if op.Operation == constant.BatchOperationDelete {
			var p model.ProjectResource
			err := db.DB.Where("project_id = ? AND resource_type = ? AND resource_id = ?", item.ProjectID, item.ResourceType, resourceId).First(&p).Error
			if err != nil {
				return err
			}
			itemId = p.ID

			if item.ResourceType == constant.ResourceBackupAccount {
				var clusterResources []model.ProjectResource
				err = db.DB.Where("project_id = ? AND resource_type = ?", item.ProjectID, constant.ResourceCluster).Find(&clusterResources).Error
				if err != nil && !gorm.IsRecordNotFoundError(err) {
					return err
				}
				if len(clusterResources) > 0 {
					for _, clusterResource := range clusterResources {
						var backupStrategy model.ClusterBackupStrategy
						err = db.DB.Where("backup_account_id = ? AND cluster_id = ?", resourceId, clusterResource.ResourceID).First(&backupStrategy).Error
						if err != nil && !gorm.IsRecordNotFoundError(err) {
							return err
						}
						if backupStrategy.ID != "" {
							var backupFiles []model.ClusterBackupFile
							err = db.DB.Where("cluster_backup_strategy_id = ? AND cluster_id = ?", backupStrategy.ID, clusterResource.ResourceID).Find(&backupFiles).Error
							if err != nil && !gorm.IsRecordNotFoundError(err) {
								return err
							}
							if len(backupFiles) > 0 {
								return errors.New("DELETE_FAILED_BY_BACKUP_FILE")
							}
						}
					}
				}
			}
		}

		if op.Operation == constant.BatchOperationCreate {
			if item.ResourceType == constant.ResourceHost {
				var clusterResources []model.ProjectResource
				err := db.DB.Where("resource_id = ? AND resource_type = ?", resourceId, constant.ResourceHost).Find(&clusterResources).Error
				if err != nil && !gorm.IsRecordNotFoundError(err) {
					return err
				}
				if len(clusterResources) > 0 {
					continue
				}
			}
		}

		opItems = append(opItems, model.ProjectResource{
			BaseModel:    common.BaseModel{},
			ID:           itemId,
			ResourceID:   resourceId,
			ResourceType: item.ResourceType,
			ProjectID:    item.ProjectID,
		})
	}
	return p.projectResourceRepo.Batch(op.Operation, opItems)
}

func (p projectResourceService) GetResources(resourceType, projectName string) (interface{}, error) {
	var result interface{}
	var projectResources []model.ProjectResource
	var resourceIds []string
	if resourceType == constant.ResourcePlan || resourceType == constant.ResourceBackupAccount {
		project, err := p.projectRepo.Get(projectName)
		if err != nil {
			return nil, err
		}
		if err = db.DB.Select("resource_id").Where("project_id = ? AND resource_type = ?", project.ID, resourceType).Find(&projectResources).Error; err != nil {
			return nil, err
		}
	}
	if resourceType == constant.ResourceHost {
		if err := db.DB.Select("resource_id").Where("resource_type = ?", resourceType).Find(&projectResources).Error; err != nil {
			return nil, err
		}
	}
	for _, pr := range projectResources {
		resourceIds = append(resourceIds, pr.ResourceID)
	}
	if len(resourceIds) == 0 {
		resourceIds = append(resourceIds, "1")
	}

	switch resourceType {
	case constant.ResourceHost:
		var result []model.Host
		if err := db.DB.Where("id not  in (?) and cluster_id = ''", resourceIds).Find(&result).Error; err != nil {
			return nil, err
		}
		return result, nil
	case constant.ResourcePlan:
		var result []model.Plan
		resourceIds = append(resourceIds, "1")
		err := db.DB.Where("id not in (?)", resourceIds).Preload("Zones").Preload("Region").Find(&result).Error
		if err != nil {
			return nil, err
		}
		return result, nil
	case constant.ResourceBackupAccount:
		var result []model.BackupAccount
		resourceIds = append(resourceIds, "1")
		err := db.DB.Where("id not in (?)", resourceIds).Find(&result).Error
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	return result, nil
}

func (p projectResourceService) GetResourceTree() ([]dto.ProjectResourceTree, error) {
	var (
		projects []model.Project
		tree     []dto.ProjectResourceTree
	)

	if err := db.DB.Model(model.Project{}).Order("name").Find(&projects).Error; err != nil {
		return nil, err
	}
	id := 0
	for _, p := range projects {
		id++
		tree = append(tree, dto.ProjectResourceTree{
			ID:    id,
			Label: p.Name,
			Type:  constant.ResourceProject,
		})
	}
	for i, t := range tree {
		var project model.Project
		if err := db.DB.Where("name = ?", t.Label).First(&project).Error; err != nil {
			return nil, err
		}
		var projectResources []model.ProjectResource
		if err := db.DB.Where("project_id = ? AND resource_type = ?", project.ID, constant.ResourceCluster).Find(&projectResources).Error; err != nil {
			return nil, err
		}
		var resourceIds []string
		for _, pr := range projectResources {
			resourceIds = append(resourceIds, pr.ResourceID)
		}
		var clusters []model.Cluster
		if err := db.DB.Model(&model.Cluster{}).
			Where("id in (?)", resourceIds).
			Find(&clusters).Error; err != nil {
			return nil, err
		}
		for _, c := range clusters {
			id++
			tree[i].Children = append(tree[i].Children, dto.ProjectResourceTree{
				ID:    id,
				Label: c.Name,
				Type:  constant.ResourceCluster,
			})
		}
	}
	return tree, nil
}
