package services

import (
	"context"
	"fmt"
	"strings"
	"sync"

	apiErrors "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/errors"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/services/sso"
	"gorm.io/gorm"

	constants2 "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/internal/kafka/constants"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/internal/kafka/internal/api/dbapi"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/internal/kafka/internal/config"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/internal/kafka/internal/kafkas/types"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/logger"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/services"

	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/services/authorization"
	coreServices "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/services/queryparser"

	"strconv"
	"time"

	"github.com/golang/glog"

	managedkafka "github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/api/managedkafkas.managedkafka.bf2.org/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/api"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/auth"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/client/aws"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/db"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/errors"
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/metrics"
)

var kafkaDeletionStatuses = []string{constants2.KafkaRequestStatusDeleting.String(), constants2.KafkaRequestStatusDeprovision.String()}
var kafkaManagedCRStatuses = []string{constants2.KafkaRequestStatusProvisioning.String(), constants2.KafkaRequestStatusDeprovision.String(), constants2.KafkaRequestStatusReady.String(), constants2.KafkaRequestStatusFailed.String()}

type KafkaRoutesAction string

const KafkaRoutesActionCreate KafkaRoutesAction = "CREATE"
const KafkaRoutesActionDelete KafkaRoutesAction = "DELETE"
const CanaryServiceAccountPrefix = "canary"

type CNameRecordStatus struct {
	Id     *string
	Status *string
}

//go:generate moq -out kafkaservice_moq.go . KafkaService
type KafkaService interface {
	// PrepareKafkaRequest sets any required information (i.e. bootstrap server host, sso client id and secret)
	// to the Kafka Request record in the database. The kafka request will also be updated with an updated_at
	// timestamp and the corresponding cluster identifier.
	PrepareKafkaRequest(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError
	// Get method will retrieve the kafkaRequest instance that the give ctx has access to from the database.
	// This should be used when you want to make sure the result is filtered based on the request context.
	Get(ctx context.Context, id string) (*dbapi.KafkaRequest, *errors.ServiceError)
	// GetById method will retrieve the KafkaRequest instance from the database without checking any permissions.
	// You should only use this if you are sure permission check is not required.
	GetById(id string) (*dbapi.KafkaRequest, *errors.ServiceError)
	// Delete cleans up all dependencies for a Kafka request and soft deletes the Kafka Request record from the database.
	// The Kafka Request in the database will be updated with a deleted_at timestamp.
	Delete(*dbapi.KafkaRequest) *errors.ServiceError
	List(ctx context.Context, listArgs *services.ListArguments) (dbapi.KafkaList, *api.PagingMeta, *errors.ServiceError)
	GetManagedKafkaByClusterID(clusterID string) ([]managedkafka.ManagedKafka, *errors.ServiceError)
	// GenerateReservedManagedKafkasByClusterID returns a list of reserved managed
	// kafkas for a given clusterID. The number of generated reserved managed kafkas
	// is the sum of the reserved streaming units among all instance types supported
	// by the cluster.
	// If dynamic scaling is disabled the result is an empty list.
	// If the cluster is not in ready status the result is an empty list.
	// Generated kafka names have the following naming schema:
	// reserved-kafka-<instance_type>-<kafka_number> where kafka_number goes from
	// 1..<number_of_reserved_streaming_units_for_the_given_instance_type>
	// Each generated reserved kafka has a namespace equal to its name
	GenerateReservedManagedKafkasByClusterID(clusterID string) ([]managedkafka.ManagedKafka, *errors.ServiceError)
	RegisterKafkaJob(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError
	ListByStatus(status ...constants2.KafkaStatus) ([]*dbapi.KafkaRequest, *errors.ServiceError)
	// UpdateStatus change the status of the Kafka cluster
	// The returned boolean is to be used to know if the update has been tried or not. An update is not tried if the
	// original status is 'deprovision' (cluster in deprovision state can't be change state) or if the final status is the
	// same as the original status. The error will contain any error encountered when attempting to update or the reason
	// why no attempt has been done
	UpdateStatus(id string, status constants2.KafkaStatus) (bool, *errors.ServiceError)
	Update(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError
	// Updates() updates the given fields of a kafka. This takes in a map so that even zero-fields can be updated.
	// Use this only when you want to update the multiple columns that may contain zero-fields, otherwise use the `KafkaService.Update()` method.
	// See https://gorm.io/docs/update.html#Updates-multiple-columns for more info
	Updates(kafkaRequest *dbapi.KafkaRequest, values map[string]interface{}) *errors.ServiceError
	ChangeKafkaCNAMErecords(kafkaRequest *dbapi.KafkaRequest, action KafkaRoutesAction) (*route53.ChangeResourceRecordSetsOutput, *errors.ServiceError)
	GetCNAMERecordStatus(kafkaRequest *dbapi.KafkaRequest) (*CNameRecordStatus, error)
	AssignInstanceType(owner string, organisationID string) (types.KafkaInstanceType, *errors.ServiceError)
	RegisterKafkaDeprovisionJob(ctx context.Context, id string) *errors.ServiceError
	// DeprovisionKafkaForUsers registers all kafkas for deprovisioning given the list of owners
	DeprovisionKafkaForUsers(users []string) *errors.ServiceError
	DeprovisionExpiredKafkas() *errors.ServiceError
	CountByStatus(status []constants2.KafkaStatus) ([]KafkaStatusCount, error)
	// CountStreamingUnitByRegionAndInstanceType returns streaming unit counts per regions and instance type
	CountStreamingUnitByRegionAndInstanceType() ([]KafkaStreamingUnitCountPerRegion, error)
	ListKafkasWithRoutesNotCreated() ([]*dbapi.KafkaRequest, *errors.ServiceError)
	VerifyAndUpdateKafkaAdmin(ctx context.Context, kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError
	ListComponentVersions() ([]KafkaComponentVersions, error)
	HasAvailableCapacityInRegion(kafkaRequest *dbapi.KafkaRequest) (bool, *errors.ServiceError)
	// GetAvailableSizesInRegion returns a list of ids of the Kafka instance sizes that can still be created according to the specified criteria
	GetAvailableSizesInRegion(criteria *FindClusterCriteria) ([]string, *errors.ServiceError)
	ValidateBillingAccount(externalId string, instanceType types.KafkaInstanceType, billingCloudAccountId string, marketplace *string) *errors.ServiceError
}

var _ KafkaService = &kafkaService{}

type kafkaService struct {
	connectionFactory        *db.ConnectionFactory
	clusterService           ClusterService
	keycloakService          sso.KeycloakService
	kafkaConfig              *config.KafkaConfig
	awsConfig                *config.AWSConfig
	quotaServiceFactory      QuotaServiceFactory
	mu                       sync.Mutex
	awsClientFactory         aws.ClientFactory
	authService              authorization.Authorization
	dataplaneClusterConfig   *config.DataplaneClusterConfig
	providerConfig           *config.ProviderConfig
	clusterPlacementStrategy ClusterPlacementStrategy
}

func NewKafkaService(connectionFactory *db.ConnectionFactory, clusterService ClusterService, keycloakService sso.KafkaKeycloakService, kafkaConfig *config.KafkaConfig, dataplaneClusterConfig *config.DataplaneClusterConfig, awsConfig *config.AWSConfig, quotaServiceFactory QuotaServiceFactory, awsClientFactory aws.ClientFactory, authorizationService authorization.Authorization, providerConfig *config.ProviderConfig, clusterPlacementStrategy ClusterPlacementStrategy) *kafkaService {
	return &kafkaService{
		connectionFactory:        connectionFactory,
		clusterService:           clusterService,
		keycloakService:          keycloakService,
		kafkaConfig:              kafkaConfig,
		awsConfig:                awsConfig,
		quotaServiceFactory:      quotaServiceFactory,
		awsClientFactory:         awsClientFactory,
		authService:              authorizationService,
		dataplaneClusterConfig:   dataplaneClusterConfig,
		providerConfig:           providerConfig,
		clusterPlacementStrategy: clusterPlacementStrategy,
	}
}

func (k *kafkaService) ValidateBillingAccount(externalId string, instanceType types.KafkaInstanceType, billingCloudAccountId string, marketplace *string) *errors.ServiceError {
	quotaService, factoryErr := k.quotaServiceFactory.GetQuotaService(api.QuotaType(k.kafkaConfig.Quota.Type))
	if factoryErr != nil {
		return errors.NewWithCause(errors.ErrorGeneral, factoryErr, "unable to check quota during billing account validation")
	}

	return quotaService.ValidateBillingAccount(externalId, instanceType, billingCloudAccountId, marketplace)
}

func (k *kafkaService) HasAvailableCapacityInRegion(kafkaRequest *dbapi.KafkaRequest) (bool, *errors.ServiceError) {
	// get region limit for instance type
	regInstTypeLimit, e := k.providerConfig.GetInstanceLimit(kafkaRequest.Region, kafkaRequest.CloudProvider, kafkaRequest.InstanceType)
	if e != nil {
		return false, e
	}

	if regInstTypeLimit != nil && int64(*regInstTypeLimit) == 0 {
		return false, nil
	}

	// if auto scaling is enabled and no limit set - capacity is available in the region
	if k.dataplaneClusterConfig.IsDataPlaneAutoScalingEnabled() && regInstTypeLimit == nil {
		return true, nil
	}
	// check capacity
	return k.capacityAvailableForRegionAndInstanceType(regInstTypeLimit, kafkaRequest)
}

func (k *kafkaService) capacityAvailableForRegionAndInstanceType(instTypeRegCapacity *int, kafkaRequest *dbapi.KafkaRequest) (bool, *errors.ServiceError) {
	errMessage := fmt.Sprintf("Failed to check kafka capacity for region '%s' and instance type '%s'", kafkaRequest.Region, kafkaRequest.InstanceType)

	dbConn := k.connectionFactory.New()

	var count int64

	var kafkas []*dbapi.KafkaRequest

	if err := dbConn.Model(&dbapi.KafkaRequest{}).
		Where("region = ?", kafkaRequest.Region).
		Where("cloud_provider = ?", kafkaRequest.CloudProvider).
		Where("instance_type = ?", kafkaRequest.InstanceType).
		Scan(&kafkas).Error; err != nil {
		return false, errors.NewWithCause(errors.ErrorGeneral, err, errMessage)
	}

	for _, kafka := range kafkas {
		kafkaInstanceSize, e := k.kafkaConfig.GetKafkaInstanceSize(kafka.InstanceType, kafka.SizeId)
		if e != nil {
			return false, errors.NewWithCause(errors.ErrorInstancePlanNotSupported, e, errMessage)
		}
		count += int64(kafkaInstanceSize.CapacityConsumed)
	}

	kafkaInstanceSize, e := k.kafkaConfig.GetKafkaInstanceSize(kafkaRequest.InstanceType, kafkaRequest.SizeId)
	if e != nil {
		return false, errors.NewWithCause(errors.ErrorInstancePlanNotSupported, e, errMessage)
	}

	count += int64(kafkaInstanceSize.CapacityConsumed)

	return instTypeRegCapacity == nil || count <= int64(*instTypeRegCapacity), nil
}

func (k *kafkaService) GetAvailableSizesInRegion(criteria *FindClusterCriteria) ([]string, *errors.ServiceError) {
	if criteria == nil {
		err := errors.GeneralError("unable to get available sizes in region: criteria was not specified")
		logger.Logger.Error(err)
		return nil, err
	}

	supportedInstanceTypes := k.kafkaConfig.SupportedInstanceTypes.Configuration
	instanceType, err := supportedInstanceTypes.GetKafkaInstanceTypeByID(criteria.SupportedInstanceType)
	if err != nil {
		err := errors.InstanceTypeNotSupported("unable to get available sizes in region: %s", err.Error())
		logger.Logger.Error(err)
		return nil, err
	}

	// The kafka size list configuration must always be ordered starting with the smallest unit.
	// The following finds the largest Kafka size that is still available in this region. Anything smaller than this
	// size will also be considered as available to create with the remaining capacity.
	for i := len(instanceType.Sizes) - 1; i >= 0; i-- {
		kafka := &dbapi.KafkaRequest{
			CloudProvider: criteria.Provider,
			Region:        criteria.Region,
			InstanceType:  criteria.SupportedInstanceType,
			MultiAZ:       criteria.MultiAZ,
			SizeId:        instanceType.Sizes[i].Id,
		}

		// Check against region limits
		hasCapacity, err := k.HasAvailableCapacityInRegion(kafka)
		if err != nil {
			logger.Logger.Error(err)
			return nil, err
		}

		if hasCapacity {
			// Check if there is an available cluster in the region that can fit this Kafka instance type and size
			cluster, err := k.clusterPlacementStrategy.FindCluster(kafka)
			if err != nil {
				logger.Logger.Error(err)
				return nil, err
			}

			if cluster != nil {
				var availableSizes []string
				for _, size := range instanceType.Sizes[0 : i+1] {
					availableSizes = append(availableSizes, size.Id)
				}
				return availableSizes, nil
			}
		}
	}
	return nil, nil
}

func (k *kafkaService) AssignInstanceType(owner string, organisationId string) (types.KafkaInstanceType, *errors.ServiceError) {
	quotaService, factoryErr := k.quotaServiceFactory.GetQuotaService(api.QuotaType(k.kafkaConfig.Quota.Type))
	if factoryErr != nil {
		return "", errors.NewWithCause(errors.ErrorGeneral, factoryErr, "unable to check quota")
	}

	hasRhosakQuota, err := quotaService.CheckIfQuotaIsDefinedForInstanceType(owner, organisationId, types.STANDARD)
	if err != nil {
		return "", err
	}
	if hasRhosakQuota {
		return types.STANDARD, nil
	}

	return types.DEVELOPER, nil
}

// reserveQuota - reserves quota for the given kafka request. If a RHOSAK quota has been assigned, it will try to reserve RHOSAK quota, otherwise it will try with RHOSAKTrial
func (k *kafkaService) reserveQuota(kafkaRequest *dbapi.KafkaRequest) (subscriptionId string, err *errors.ServiceError) {
	if kafkaRequest.InstanceType == types.DEVELOPER.String() {
		instType, err := k.kafkaConfig.SupportedInstanceTypes.Configuration.GetKafkaInstanceTypeByID(kafkaRequest.InstanceType)

		if err != nil {
			return "", errors.NewWithCause(errors.ErrorGeneral, err, "unable to reserve quota")
		}

		if !k.kafkaConfig.Quota.AllowDeveloperInstance {
			return "", errors.NewWithCause(errors.ErrorForbidden, err, "kafka %s instances are not allowed", instType.DisplayName)
		}

		// Only one DEVELOPER instance is admitted. Let's check if the user already owns one
		dbConn := k.connectionFactory.New()
		var count int64
		if err := dbConn.Model(&dbapi.KafkaRequest{}).
			Where("instance_type = ?", types.DEVELOPER).
			Where("owner = ?", kafkaRequest.Owner).
			Where("organisation_id = ?", kafkaRequest.OrganisationId).
			Count(&count).
			Error; err != nil {
			return "", errors.NewWithCause(errors.ErrorGeneral, err, "failed to count kafka %s instances", instType.DisplayName)
		}

		if count > 0 {
			return "", errors.TooManyKafkaInstancesReached("only one %s instance is allowed", instType.DisplayName)
		}
	}

	quotaService, factoryErr := k.quotaServiceFactory.GetQuotaService(api.QuotaType(k.kafkaConfig.Quota.Type))
	if factoryErr != nil {
		return "", errors.NewWithCause(errors.ErrorGeneral, factoryErr, "unable to check quota")
	}
	subscriptionId, err = quotaService.ReserveQuota(kafkaRequest, types.KafkaInstanceType(kafkaRequest.InstanceType))
	return subscriptionId, err
}

// RegisterKafkaJob registers a new job in the kafka table
func (k *kafkaService) RegisterKafkaJob(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError {
	k.mu.Lock()
	defer k.mu.Unlock()
	// we need to pre-populate the ID to be able to reserve the quota
	kafkaRequest.ID = api.NewID()

	// The Instance Type determines the MultiAZ attribute. The previously value
	// set for the MultiAZ attribute in the request (if any) is ignored.
	// TODO improve this
	switch kafkaRequest.InstanceType {
	case types.STANDARD.String():
		kafkaRequest.MultiAZ = true
	case types.DEVELOPER.String():
		kafkaRequest.MultiAZ = false
	}

	hasCapacity, err := k.HasAvailableCapacityInRegion(kafkaRequest)
	if err != nil {
		if err.Code == errors.ErrorGeneral {
			err = errors.NewWithCause(errors.ErrorGeneral, err, "unable to validate your request, please try again")
			logger.Logger.Errorf(err.Reason)
		}
		return err
	}
	if !hasCapacity {
		errorMsg := fmt.Sprintf("Capacity exhausted in '%s' region for '%s' instance type", kafkaRequest.Region, kafkaRequest.InstanceType)
		logger.Logger.Warningf(errorMsg)
		return errors.TooManyKafkaInstancesReached(fmt.Sprintf("Region %s cannot accept instance type: %s at this moment", kafkaRequest.Region, kafkaRequest.InstanceType))
	}

	cluster, e := k.clusterPlacementStrategy.FindCluster(kafkaRequest)
	if e != nil || cluster == nil {
		msg := fmt.Sprintf("No available cluster found for '%s' Kafka instance in region: '%s'", kafkaRequest.InstanceType, kafkaRequest.Region)
		logger.Logger.Infof(msg)
		return errors.TooManyKafkaInstancesReached(fmt.Sprintf("Region %s cannot accept instance type: %s at this moment", kafkaRequest.Region, kafkaRequest.InstanceType))
	}

	kafkaRequest.ClusterID = cluster.ClusterID
	subscriptionId, err := k.reserveQuota(kafkaRequest)

	if err != nil {
		return err
	}

	dbConn := k.connectionFactory.New()
	kafkaRequest.SubscriptionId = subscriptionId
	kafkaRequest.Status = constants2.KafkaRequestStatusAccepted.String()

	// when creating new kafka - default storage size is assigned
	instanceType, instanceTypeErr := k.kafkaConfig.SupportedInstanceTypes.Configuration.GetKafkaInstanceTypeByID(kafkaRequest.InstanceType)
	if instanceTypeErr != nil {
		return errors.InstanceTypeNotSupported(instanceTypeErr.Error())
	}

	size, sizeErr := instanceType.GetKafkaInstanceSizeByID(kafkaRequest.SizeId)
	if sizeErr != nil {
		return errors.InstancePlanNotSupported(sizeErr.Error())
	}

	kafkaRequest.KafkaStorageSize = size.MaxDataRetentionSize.String()

	// Persist the QuotaTyoe to be able to dynamically pick the right Quota service implementation even on restarts.
	// A typical usecase is when a kafka A is created, at the time of creation the quota-type was ams. At some point in the future
	// the API is restarted this time changing the --quota-type flag to quota-management-list, when kafka A is deleted at this point,
	// we want to use the correct quota to perform the deletion.
	kafkaRequest.QuotaType = k.kafkaConfig.Quota.Type
	if err := dbConn.Create(kafkaRequest).Error; err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "failed to create kafka request") //hide the db error to http caller
	}

	metrics.UpdateKafkaRequestsStatusSinceCreatedMetric(constants2.KafkaRequestStatusAccepted, kafkaRequest.ID, kafkaRequest.ClusterID, time.Since(kafkaRequest.CreatedAt))
	return nil
}

func (k *kafkaService) PrepareKafkaRequest(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError {
	truncatedKafkaIdentifier := buildTruncateKafkaIdentifier(kafkaRequest)
	truncatedKafkaIdentifier, replaceErr := replaceHostSpecialChar(truncatedKafkaIdentifier)
	if replaceErr != nil {
		return errors.NewWithCause(errors.ErrorGeneral, replaceErr, "generated host is not valid")
	}

	clusterDNS, err := k.clusterService.GetClusterDNS(kafkaRequest.ClusterID)
	if err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "error retrieving cluster DNS")
	}

	kafkaRequest.Namespace = fmt.Sprintf("kafka-%s", strings.ToLower(kafkaRequest.ID))
	clusterDNS = strings.Replace(clusterDNS, constants2.DefaultIngressDnsNamePrefix, constants2.ManagedKafkaIngressDnsNamePrefix, 1)
	kafkaRequest.BootstrapServerHost = fmt.Sprintf("%s.%s", truncatedKafkaIdentifier, clusterDNS)

	if k.kafkaConfig.EnableKafkaExternalCertificate {
		// If we enable KafkaTLS, the bootstrapServerHost should use the external domain name rather than the cluster domain
		kafkaRequest.BootstrapServerHost = fmt.Sprintf("%s.%s", truncatedKafkaIdentifier, k.kafkaConfig.KafkaDomainName)
	}

	if k.keycloakService.GetConfig().EnableAuthenticationOnKafka {
		clientId := strings.ToLower(fmt.Sprintf("%s-%s", CanaryServiceAccountPrefix, kafkaRequest.ID))
		serviceAccountRequest := sso.CompleteServiceAccountRequest{
			Owner:          kafkaRequest.Owner,
			OwnerAccountId: kafkaRequest.OwnerAccountId,
			ClientId:       clientId,
			OrgId:          kafkaRequest.OrganisationId,
			Name:           fmt.Sprintf("canary-service-account-for-kafka %s", kafkaRequest.ID),
			Description:    fmt.Sprintf("canary service account for kafka %s", kafkaRequest.ID),
		}

		canaryServiceAccount, err := k.keycloakService.CreateServiceAccountInternal(serviceAccountRequest)

		if err != nil {
			return errors.FailedToCreateSSOClient("failed to  create canary service account %s:%v", kafkaRequest.ID, err)
		}

		kafkaRequest.CanaryServiceAccountClientID = canaryServiceAccount.ClientID
		kafkaRequest.CanaryServiceAccountClientSecret = canaryServiceAccount.ClientSecret
	}

	// Update the Kafka Request record in the database
	// Only updates the fields below
	updatedKafkaRequest := &dbapi.KafkaRequest{
		Meta: api.Meta{
			ID: kafkaRequest.ID,
		},
		BootstrapServerHost:              kafkaRequest.BootstrapServerHost,
		CanaryServiceAccountClientID:     kafkaRequest.CanaryServiceAccountClientID,
		CanaryServiceAccountClientSecret: kafkaRequest.CanaryServiceAccountClientSecret,
		PlacementId:                      api.NewID(),
		Status:                           constants2.KafkaRequestStatusProvisioning.String(),
		Namespace:                        kafkaRequest.Namespace,
	}
	if err := k.Update(updatedKafkaRequest); err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "failed to update kafka request")
	}

	return nil
}

func (k *kafkaService) ListByStatus(status ...constants2.KafkaStatus) ([]*dbapi.KafkaRequest, *errors.ServiceError) {
	if len(status) == 0 {
		return nil, errors.GeneralError("no status provided")
	}
	dbConn := k.connectionFactory.New()

	var kafkas []*dbapi.KafkaRequest

	if err := dbConn.Model(&dbapi.KafkaRequest{}).Where("status IN (?)", status).Scan(&kafkas).Error; err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "failed to list by status")
	}

	return kafkas, nil
}

func (k *kafkaService) Get(ctx context.Context, id string) (*dbapi.KafkaRequest, *errors.ServiceError) {
	if id == "" {
		return nil, errors.Validation("id is undefined")
	}

	claims, err := auth.GetClaimsFromContext(ctx)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorUnauthenticated, err, "user not authenticated")
	}

	dbConn := k.connectionFactory.New().Where("id = ?", id)

	var user string
	if !auth.GetIsAdminFromContext(ctx) {
		user, _ = claims.GetUsername()
		if user == "" {
			return nil, errors.Unauthenticated("user not authenticated")
		}

		orgId, _ := claims.GetOrgId()
		filterByOrganisationId := auth.GetFilterByOrganisationFromContext(ctx)

		// filter by organisationId if a user is part of an organisation and is not allowed as a service account
		if filterByOrganisationId {
			dbConn = dbConn.Where("organisation_id = ?", orgId)
		} else {
			dbConn = dbConn.Where("owner = ?", user)
		}
	}

	var kafkaRequest dbapi.KafkaRequest
	if err := dbConn.First(&kafkaRequest).Error; err != nil {
		resourceTypeStr := "KafkaResource"
		if user != "" {
			resourceTypeStr = fmt.Sprintf("%s for user %s", resourceTypeStr, user)
		}
		return nil, services.HandleGetError(resourceTypeStr, "id", id, err)
	}
	return &kafkaRequest, nil
}

func (k *kafkaService) GetById(id string) (*dbapi.KafkaRequest, *errors.ServiceError) {
	if id == "" {
		return nil, errors.Validation("id is undefined")
	}

	dbConn := k.connectionFactory.New()
	var kafkaRequest dbapi.KafkaRequest
	if err := dbConn.Where("id = ?", id).First(&kafkaRequest).Error; err != nil {
		return nil, services.HandleGetError("KafkaResource", "id", id, err)
	}
	return &kafkaRequest, nil
}

// RegisterKafkaDeprovisionJob registers a kafka deprovision job in the kafka table
func (k *kafkaService) RegisterKafkaDeprovisionJob(ctx context.Context, id string) *errors.ServiceError {
	if id == "" {
		return errors.Validation("id is undefined")
	}

	// filter kafka request by owner to only retrieve request of the current authenticated user
	claims, err := auth.GetClaimsFromContext(ctx)
	if err != nil {
		return errors.NewWithCause(errors.ErrorUnauthenticated, err, "user not authenticated")
	}

	dbConn := k.connectionFactory.New()

	if auth.GetIsAdminFromContext(ctx) {
		dbConn = dbConn.Where("id = ?", id)
	} else if claims.IsOrgAdmin() {
		orgId, _ := claims.GetOrgId()
		dbConn = dbConn.Where("id = ?", id).Where("organisation_id = ?", orgId)
	} else {
		user, _ := claims.GetUsername()
		dbConn = dbConn.Where("id = ?", id).Where("owner = ? ", user)
	}

	var kafkaRequest dbapi.KafkaRequest
	if err := dbConn.First(&kafkaRequest).Error; err != nil {
		return services.HandleGetError("KafkaResource", "id", id, err)
	}
	metrics.IncreaseKafkaTotalOperationsCountMetric(constants2.KafkaOperationDeprovision)

	deprovisionStatus := constants2.KafkaRequestStatusDeprovision

	if executed, err := k.UpdateStatus(id, deprovisionStatus); executed {
		if err != nil {
			return services.HandleGetError("KafkaResource", "id", id, err)
		}
		metrics.IncreaseKafkaSuccessOperationsCountMetric(constants2.KafkaOperationDeprovision)
		metrics.UpdateKafkaRequestsStatusSinceCreatedMetric(deprovisionStatus, kafkaRequest.ID, kafkaRequest.ClusterID, time.Since(kafkaRequest.CreatedAt))
	}

	return nil
}

func (k *kafkaService) DeprovisionKafkaForUsers(users []string) *errors.ServiceError {
	dbConn := k.connectionFactory.New().
		Model(&dbapi.KafkaRequest{}).
		Where("owner IN (?)", users).
		Where("status NOT IN (?)", kafkaDeletionStatuses).
		Update("status", constants2.KafkaRequestStatusDeprovision)

	err := dbConn.Error
	if err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "Unable to deprovision kafka requests for users")
	}

	if dbConn.RowsAffected >= 1 {
		glog.Infof("%v kafkas are now deprovisioning for users %v", dbConn.RowsAffected, users)
		var counter int64 = 0
		for ; counter < dbConn.RowsAffected; counter++ {
			metrics.IncreaseKafkaTotalOperationsCountMetric(constants2.KafkaOperationDeprovision)
			metrics.IncreaseKafkaSuccessOperationsCountMetric(constants2.KafkaOperationDeprovision)
		}
	}

	return nil
}

func (k *kafkaService) DeprovisionExpiredKafkas() *errors.ServiceError {
	dbConn := k.connectionFactory.New().Model(&dbapi.KafkaRequest{}).Session(&gorm.Session{})

	var typesWithLifespan []string
	for _, kafkaInstanceType := range k.kafkaConfig.SupportedInstanceTypes.Configuration.SupportedKafkaInstanceTypes {
		if kafkaInstanceType.HasAnInstanceSizeWithLifespan() {
			typesWithLifespan = append(typesWithLifespan, kafkaInstanceType.Id)
		}
	}

	if len(typesWithLifespan) == 0 {
		return nil
	}
	glog.V(10).Infof("Kafka instance types with lifespan set: %+v", typesWithLifespan)

	var existingKafkaRequests []dbapi.KafkaRequest
	db := dbConn.Where("instance_type IN (?)", typesWithLifespan).
		Where("status NOT IN (?)", kafkaDeletionStatuses).
		Scan(&existingKafkaRequests)
	err := db.Error
	if err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "unable to deprovision expired kafkas")
	}

	var kafkasToDeprovisionIDs []string
	timeNow := time.Now()
	for _, existingKafkaRequest := range existingKafkaRequests {
		glog.V(10).Infof("Evaluating expiration time of kafka request '%s' with instance type '%s', ID '%s' and status '%s'", existingKafkaRequest.ID, existingKafkaRequest.InstanceType, existingKafkaRequest.SizeId, existingKafkaRequest.Status)
		kafkaInstanceSize, err := k.kafkaConfig.GetKafkaInstanceSize(existingKafkaRequest.InstanceType, existingKafkaRequest.SizeId)
		if err != nil {
			return errors.NewWithCause(errors.ErrorGeneral, err, "unable to deprovision expired kafkas")
		}
		if kafkaInstanceSize.LifespanSeconds != nil {
			glog.V(10).Infof("Kafka size associated to kafka ID '%s' has '%d' lifespanSeconds", existingKafkaRequest.ID, *kafkaInstanceSize.LifespanSeconds)
			expTime := existingKafkaRequest.GetExpirationTime(*kafkaInstanceSize.LifespanSeconds)
			glog.V(10).Infof("Expiration time of kafka ID '%s' is '%s'", existingKafkaRequest.ID, expTime)
			if timeNow.After(*expTime) {
				glog.V(10).Infof("Kafka ID '%s' has expired", existingKafkaRequest.ID)
				kafkasToDeprovisionIDs = append(kafkasToDeprovisionIDs, existingKafkaRequest.ID)
			} else {
				glog.V(10).Infof("Kafka ID '%s' still has not expired", existingKafkaRequest.ID)
			}
		}
	}

	if len(kafkasToDeprovisionIDs) > 0 {
		glog.V(10).Infof("Kafka IDs to mark with status %s: %+v", constants2.KafkaRequestStatusDeprovision, kafkasToDeprovisionIDs)
		db = dbConn.Where("id IN (?)", kafkasToDeprovisionIDs).
			Updates(map[string]interface{}{"status": constants2.KafkaRequestStatusDeprovision})
		err = db.Error
		if err != nil {
			return errors.NewWithCause(errors.ErrorGeneral, err, "unable to deprovision expired kafkas")
		}
		if db.RowsAffected >= 1 {
			glog.Infof("%v kafka_request's lifespans are over their lifespan and have had their status updated to deprovisioning", db.RowsAffected)
			var counter int64 = 0
			for ; counter < db.RowsAffected; counter++ {
				metrics.IncreaseKafkaTotalOperationsCountMetric(constants2.KafkaOperationDeprovision)
				metrics.IncreaseKafkaSuccessOperationsCountMetric(constants2.KafkaOperationDeprovision)
			}
		}
	}

	return nil
}

func (k *kafkaService) Delete(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError {
	dbConn := k.connectionFactory.New()

	// if the we don't have the clusterID we can only delete the row from the database
	if kafkaRequest.ClusterID != "" {
		// delete the kafka client in mas sso
		if k.keycloakService.GetConfig().EnableAuthenticationOnKafka {
			if kafkaRequest.CanaryServiceAccountClientID != "" {
				keycloakErr := k.keycloakService.DeleteServiceAccountInternal(kafkaRequest.CanaryServiceAccountClientID)
				if keycloakErr != nil {
					return errors.NewWithCause(errors.ErrorGeneral, keycloakErr, "error deleting canary service account")
				}
			}
		}

		routes, err := kafkaRequest.GetRoutes()
		if err != nil {
			return errors.NewWithCause(errors.ErrorGeneral, err, "failed to get routes")
		}
		// Only delete the routes when they are set
		if routes != nil && k.kafkaConfig.EnableKafkaExternalCertificate {
			_, err := k.ChangeKafkaCNAMErecords(kafkaRequest, KafkaRoutesActionDelete)
			if err != nil {
				return err
			}
		}
	}

	// soft delete the kafka request
	if err := dbConn.Delete(kafkaRequest).Error; err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "unable to delete kafka request with id %s", kafkaRequest.ID)
	}

	metrics.IncreaseKafkaTotalOperationsCountMetric(constants2.KafkaOperationDelete)
	metrics.IncreaseKafkaSuccessOperationsCountMetric(constants2.KafkaOperationDelete)

	return nil
}

// List returns all Kafka requests belonging to a user.
func (k *kafkaService) List(ctx context.Context, listArgs *services.ListArguments) (dbapi.KafkaList, *api.PagingMeta, *errors.ServiceError) {
	var kafkaRequestList dbapi.KafkaList
	dbConn := k.connectionFactory.New()
	pagingMeta := &api.PagingMeta{
		Page: listArgs.Page,
		Size: listArgs.Size,
	}

	claims, err := auth.GetClaimsFromContext(ctx)
	if err != nil {
		return nil, nil, errors.NewWithCause(errors.ErrorUnauthenticated, err, "user not authenticated")
	}

	if !auth.GetIsAdminFromContext(ctx) {
		user, _ := claims.GetUsername()
		if user == "" {
			return nil, nil, errors.Unauthenticated("user not authenticated")
		}

		orgId, _ := claims.GetOrgId()
		filterByOrganisationId := auth.GetFilterByOrganisationFromContext(ctx)

		// filter by organisationId if a user is part of an organisation and is not allowed as a service account
		if filterByOrganisationId {
			// filter kafka requests by organisation_id since the user is allowed to see all kafka requests of my id
			dbConn = dbConn.Where("organisation_id = ?", orgId)
		} else {
			// filter kafka requests by owner as we are dealing with service accounts which may not have an org id
			dbConn = dbConn.Where("owner = ?", user)
		}
	}

	// Apply search query
	if len(listArgs.Search) > 0 {
		searchDbQuery, err := coreServices.NewQueryParser().Parse(listArgs.Search)
		if err != nil {
			return kafkaRequestList, pagingMeta, errors.NewWithCause(errors.ErrorFailedToParseSearch, err, "Unable to list kafka requests: %s", err.Error())
		}
		dbConn = dbConn.Where(searchDbQuery.Query, searchDbQuery.Values...)
	}

	if len(listArgs.OrderBy) == 0 {
		// default orderBy name
		dbConn = dbConn.Order("name")
	}

	// Set the order by arguments if any
	for _, orderByArg := range listArgs.OrderBy {
		dbConn = dbConn.Order(orderByArg)
	}

	// set total, limit and paging (based on https://gitlab.cee.redhat.com/service/api-guidelines#user-content-paging)
	total := int64(pagingMeta.Total)
	dbConn.Model(&kafkaRequestList).Count(&total)
	pagingMeta.Total = int(total)
	if pagingMeta.Size > pagingMeta.Total {
		pagingMeta.Size = pagingMeta.Total
	}
	dbConn = dbConn.Offset((pagingMeta.Page - 1) * pagingMeta.Size).Limit(pagingMeta.Size)

	// execute query
	if err := dbConn.Find(&kafkaRequestList).Error; err != nil {
		return kafkaRequestList, pagingMeta, errors.NewWithCause(errors.ErrorGeneral, err, "Unable to list kafka requests")
	}

	return kafkaRequestList, pagingMeta, nil
}

func (k *kafkaService) GetManagedKafkaByClusterID(clusterID string) ([]managedkafka.ManagedKafka, *errors.ServiceError) {
	dbConn := k.connectionFactory.New().
		Where("cluster_id = ?", clusterID).
		Where("status IN (?)", kafkaManagedCRStatuses).
		Where("bootstrap_server_host != ''")

	var kafkaRequestList dbapi.KafkaList
	if err := dbConn.Find(&kafkaRequestList).Error; err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "unable to list kafka requests")
	}

	var res []managedkafka.ManagedKafka
	// convert kafka requests to managed kafka
	for _, kafkaRequest := range kafkaRequestList {
		mk, err := buildManagedKafkaCR(kafkaRequest, k.kafkaConfig, k.keycloakService)
		if err != nil {
			return nil, err
		}
		res = append(res, *mk)
	}

	return res, nil
}

func (k *kafkaService) GenerateReservedManagedKafkasByClusterID(clusterID string) ([]managedkafka.ManagedKafka, *errors.ServiceError) {
	reservedKafkas := []managedkafka.ManagedKafka{}

	if !k.dataplaneClusterConfig.IsDataPlaneAutoScalingEnabled() {
		return reservedKafkas, nil
	}

	cluster, svcErr := k.clusterService.FindClusterByID(clusterID)
	if svcErr != nil {
		return nil, svcErr
	}
	if cluster == nil {
		return nil, apiErrors.GeneralError("failed to generate reserved managed kafkas for clusterID %s: clusterID not found", clusterID)
	}
	if cluster.Status != api.ClusterReady {
		logger.Logger.Warningf("ClusterID '%s' is not ready. Its status is '%s'. Returning an empty list of reserved managed kafkas", clusterID, cluster.Status)
		return reservedKafkas, nil
	}

	latestStrimziVersion, err := cluster.GetLatestAvailableAndReadyStrimziVersion()
	if err != nil {
		return nil, errors.NewWithCause(apiErrors.ErrorGeneral, err, "failed to generate reserved managed kafkas for clusterID %s: error finding ready strimzi versions", clusterID)
	}
	if latestStrimziVersion == nil {
		return nil, apiErrors.GeneralError("failed to generate reserved managed kafkas for clusterID %s: no ready strimzi versions found", clusterID)
	}

	supportedInstanceTypes := cluster.GetSupportedInstanceTypes()

	for _, supportedInstanceType := range supportedInstanceTypes {
		instanceTypeDynamicScalingConfig, ok := k.dataplaneClusterConfig.DynamicScalingConfig.ForInstanceType(supportedInstanceType)
		if !ok {
			return nil, apiErrors.GeneralError("failed to generate reserved managed kafkas for clusterID %s: dynamic scaling config for instance type '%s' not found", clusterID, supportedInstanceType)
		}
		numReservedInstances := instanceTypeDynamicScalingConfig.ReservedStreamingUnits
		for i := 1; i <= numReservedInstances; i++ {
			generatedKafkaID := fmt.Sprintf("reserved-kafka-%s-%d", supportedInstanceType, i)
			res, err := k.buildReservedManagedKafkaCR(generatedKafkaID, supportedInstanceType, *latestStrimziVersion)
			if err != nil {
				return nil, err
			}

			reservedKafkas = append(reservedKafkas, *res)
		}

	}

	return reservedKafkas, nil
}

func (k *kafkaService) Update(kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError {
	dbConn := k.connectionFactory.New().
		Model(kafkaRequest).
		Where("status not IN (?)", kafkaDeletionStatuses) // ignore updates of kafka under deletion

	if err := dbConn.Updates(kafkaRequest).Error; err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "Failed to update kafka")
	}

	return nil
}

func (k *kafkaService) Updates(kafkaRequest *dbapi.KafkaRequest, fields map[string]interface{}) *errors.ServiceError {
	dbConn := k.connectionFactory.New().
		Model(kafkaRequest).
		Where("status not IN (?)", kafkaDeletionStatuses) // ignore updates of kafka under deletion

	if err := dbConn.Updates(fields).Error; err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "Failed to update kafka")
	}

	return nil
}

func (k *kafkaService) VerifyAndUpdateKafkaAdmin(ctx context.Context, kafkaRequest *dbapi.KafkaRequest) *errors.ServiceError {
	if !auth.GetIsAdminFromContext(ctx) {
		return errors.New(errors.ErrorUnauthenticated, "User not authenticated")
	}

	// only updated specified columns to avoid changing other columns e.g Status
	updatableFields := map[string]interface{}{
		"kafka_storage_size":        kafkaRequest.KafkaStorageSize,
		"desired_strimzi_version":   kafkaRequest.DesiredStrimziVersion,
		"desired_kafka_version":     kafkaRequest.DesiredKafkaVersion,
		"desired_kafka_ibp_version": kafkaRequest.DesiredKafkaIBPVersion,
	}

	dbConn := k.connectionFactory.New().
		Model(kafkaRequest)

	if err := dbConn.Updates(updatableFields).Error; err != nil {
		return errors.NewWithCause(errors.ErrorGeneral, err, "Failed to update kafka")
	}

	return nil
}

func (k *kafkaService) UpdateStatus(id string, status constants2.KafkaStatus) (bool, *errors.ServiceError) {
	dbConn := k.connectionFactory.New()

	if kafka, err := k.GetById(id); err != nil {
		return true, errors.NewWithCause(errors.ErrorGeneral, err, "failed to update status")
	} else {
		// only allow to change the status to "deleting" if the cluster is already in "deprovision" status
		if kafka.Status == constants2.KafkaRequestStatusDeprovision.String() && status != constants2.KafkaRequestStatusDeleting {
			return false, errors.GeneralError("failed to update status: cluster is deprovisioning")
		}

		if kafka.Status == status.String() {
			// no update needed
			return false, errors.GeneralError("failed to update status: the cluster %s is already in %s state", id, status.String())
		}
	}

	if err := dbConn.Model(&dbapi.KafkaRequest{Meta: api.Meta{ID: id}}).Update("status", status).Error; err != nil {
		return true, errors.NewWithCause(errors.ErrorGeneral, err, "Failed to update kafka status")
	}

	return true, nil
}

func (k *kafkaService) ChangeKafkaCNAMErecords(kafkaRequest *dbapi.KafkaRequest, action KafkaRoutesAction) (*route53.ChangeResourceRecordSetsOutput, *errors.ServiceError) {
	routes, err := kafkaRequest.GetRoutes()
	if routes == nil || err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "failed to get routes")
	}

	domainRecordBatch := buildKafkaClusterCNAMESRecordBatch(routes, string(action))

	// Create AWS client with the region of this Kafka Cluster
	awsConfig := aws.Config{
		AccessKeyID:     k.awsConfig.Route53AccessKey,
		SecretAccessKey: k.awsConfig.Route53SecretAccessKey,
	}
	awsClient, err := k.awsClientFactory.NewClient(awsConfig, kafkaRequest.Region)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Unable to create aws client")
	}

	changeRecordsOutput, err := awsClient.ChangeResourceRecordSets(k.kafkaConfig.KafkaDomainName, domainRecordBatch)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Unable to create domain record sets")
	}

	return changeRecordsOutput, nil
}

func (k *kafkaService) GetCNAMERecordStatus(kafkaRequest *dbapi.KafkaRequest) (*CNameRecordStatus, error) {
	awsConfig := aws.Config{
		AccessKeyID:     k.awsConfig.Route53AccessKey,
		SecretAccessKey: k.awsConfig.Route53SecretAccessKey,
	}
	awsClient, err := k.awsClientFactory.NewClient(awsConfig, kafkaRequest.Region)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Unable to create aws client")
	}

	changeOutput, err := awsClient.GetChange(kafkaRequest.RoutesCreationId)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Unable to CNAME record status")
	}

	return &CNameRecordStatus{
		Id:     changeOutput.ChangeInfo.Id,
		Status: changeOutput.ChangeInfo.Status,
	}, nil
}

type KafkaStatusCount struct {
	Status constants2.KafkaStatus
	Count  int
}

type KafkaStreamingUnitCountPerRegion struct {
	Region        string
	InstanceType  string
	ClusterId     string
	Count         float64
	CloudProvider string
}

func (kafkaStreamingUnitCountPerRegion KafkaStreamingUnitCountPerRegion) isSame(kafkaPerRegionFromDB *KafkaPerRegionCount) bool {
	return kafkaStreamingUnitCountPerRegion.CloudProvider == kafkaPerRegionFromDB.CloudProvider &&
		kafkaStreamingUnitCountPerRegion.ClusterId == kafkaPerRegionFromDB.ClusterId &&
		kafkaStreamingUnitCountPerRegion.InstanceType == kafkaPerRegionFromDB.InstanceType &&
		kafkaStreamingUnitCountPerRegion.Region == kafkaPerRegionFromDB.Region
}

// KafkaPerRegionCount is a struct used to query the database using a "group by" clause
type KafkaPerRegionCount struct {
	Region        string
	InstanceType  string
	ClusterId     string
	Count         float64
	CloudProvider string
	SizeId        string
}

type ClusterSelection struct {
	CloudProvider         string
	ClusterID             string
	Region                string
	SupportedInstanceType string
}

func (k *kafkaService) CountStreamingUnitByRegionAndInstanceType() ([]KafkaStreamingUnitCountPerRegion, error) {

	var clusters []*ClusterSelection
	dbConn := k.connectionFactory.New()
	// TODO ignore clusters that do not have a cluster_id set e.g the one in accepted state
	if err := dbConn.Model(&api.Cluster{}).Scan(&clusters).Error; err != nil {
		return nil, apiErrors.NewWithCause(apiErrors.ErrorGeneral, err, "failed to list clusters")
	}

	streamingUnitsCountPerRegion := []KafkaStreamingUnitCountPerRegion{}

	// pre-populate regions count with zero count values.
	// This is useful and it ensures that the count drops to 0 for each instance type and region when all the kafkas are removed
	for _, cluster := range clusters {
		supportedInstanceTypes := strings.Split(cluster.SupportedInstanceType, ",")

		for _, supportedInstanceType := range supportedInstanceTypes {
			instanceType := strings.TrimSpace(supportedInstanceType)
			if instanceType == "" {
				continue
			}

			streamingUnitsCountPerRegion = append(streamingUnitsCountPerRegion, KafkaStreamingUnitCountPerRegion{
				CloudProvider: cluster.CloudProvider,
				ClusterId:     cluster.ClusterID,
				InstanceType:  instanceType,
				Region:        cluster.Region,
				Count:         0,
			})
		}
	}

	dbConn = k.connectionFactory.New()
	var kafkasPerRegion []*KafkaPerRegionCount
	if err := dbConn.Model(&dbapi.KafkaRequest{}).
		Select("cloud_provider, region, count(1) as Count, size_id, cluster_id, instance_type").
		Group("size_id, cluster_id, cloud_provider, region, instance_type").
		Scan(&kafkasPerRegion).Error; err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Failed to count kafkas when setting capacity metrics")
	}

	for _, kafkaCountPerRegion := range kafkasPerRegion {
		resultPresent := false
		instSize, err := k.kafkaConfig.GetKafkaInstanceSize(kafkaCountPerRegion.InstanceType, kafkaCountPerRegion.SizeId)
		if err != nil {
			return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Failed to count kafkas of '%s' instance type and '%s' size_id when setting capacity metrics", kafkaCountPerRegion.InstanceType, kafkaCountPerRegion.SizeId)
		}

		streamingUnitCount := float64(instSize.CapacityConsumed) * kafkaCountPerRegion.Count
		for i, streamingUnitCountPerRegion := range streamingUnitsCountPerRegion {
			if streamingUnitCountPerRegion.isSame(kafkaCountPerRegion) {
				streamingUnitsCountPerRegion[i].Count += streamingUnitCount
				resultPresent = true
				break
			}
		}

		// We should never reach here in manual scaling
		// TODO review if this is needed when dynamic scaling is implemented
		if !resultPresent {
			newStreamingUnitCountPerRegion := KafkaStreamingUnitCountPerRegion{
				CloudProvider: kafkaCountPerRegion.CloudProvider,
				ClusterId:     kafkaCountPerRegion.ClusterId,
				InstanceType:  kafkaCountPerRegion.InstanceType,
				Region:        kafkaCountPerRegion.Region,
				Count:         streamingUnitCount,
			}
			streamingUnitsCountPerRegion = append(streamingUnitsCountPerRegion, newStreamingUnitCountPerRegion)
		}
	}

	return streamingUnitsCountPerRegion, nil
}

func (k *kafkaService) CountByStatus(status []constants2.KafkaStatus) ([]KafkaStatusCount, error) {
	dbConn := k.connectionFactory.New()
	var results []KafkaStatusCount
	if err := dbConn.Model(&dbapi.KafkaRequest{}).Select("status as Status, count(1) as Count").Where("status in (?)", status).Group("status").Scan(&results).Error; err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "Failed to count kafkas")
	}

	// if there is no count returned for a status from the above query because there is no kafkas in such a status,
	// we should return the count for these as well to avoid any confusion
	if len(status) > 0 {
		countersMap := map[constants2.KafkaStatus]int{}
		for _, r := range results {
			countersMap[r.Status] = r.Count
		}
		for _, s := range status {
			if _, ok := countersMap[s]; !ok {
				results = append(results, KafkaStatusCount{Status: s, Count: 0})
			}
		}
	}

	return results, nil
}

type KafkaComponentVersions struct {
	ID                     string
	ClusterID              string
	DesiredStrimziVersion  string
	ActualStrimziVersion   string
	StrimziUpgrading       bool
	DesiredKafkaVersion    string
	ActualKafkaVersion     string
	KafkaUpgrading         bool
	DesiredKafkaIBPVersion string
	ActualKafkaIBPVersion  string
	KafkaIBPUpgrading      bool
}

func (k *kafkaService) ListComponentVersions() ([]KafkaComponentVersions, error) {
	dbConn := k.connectionFactory.New()
	var results []KafkaComponentVersions
	if err := dbConn.Model(&dbapi.KafkaRequest{}).Select("id", "cluster_id", "desired_strimzi_version", "actual_strimzi_version", "strimzi_upgrading", "desired_kafka_version", "actual_kafka_version", "kafka_upgrading", "desired_kafka_ibp_version", "actual_kafka_ibp_version", "kafka_ibp_upgrading").Scan(&results).Error; err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "failed to list component versions")
	}
	return results, nil
}

func (k *kafkaService) ListKafkasWithRoutesNotCreated() ([]*dbapi.KafkaRequest, *errors.ServiceError) {
	dbConn := k.connectionFactory.New()
	var results []*dbapi.KafkaRequest
	if err := dbConn.Where("routes IS NOT NULL").Where("routes_created = ?", "no").Find(&results).Error; err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "failed to list kafka requests")
	}
	return results, nil
}

func buildManagedKafkaCR(kafkaRequest *dbapi.KafkaRequest, kafkaConfig *config.KafkaConfig, keycloakService sso.KeycloakService) (*managedkafka.ManagedKafka, *errors.ServiceError) {
	k, err := kafkaConfig.GetKafkaInstanceSize(kafkaRequest.InstanceType, kafkaRequest.SizeId)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "unable to list kafka request")
	}
	labels := map[string]string{
		"bf2.org/kafkaInstanceProfileQuotaConsumed": strconv.Itoa(k.QuotaConsumed),
		"bf2.org/kafkaInstanceProfileType":          kafkaRequest.InstanceType,
	}
	managedKafkaCR := &managedkafka.ManagedKafka{
		Id: kafkaRequest.ID,
		TypeMeta: metav1.TypeMeta{
			Kind:       "ManagedKafka",
			APIVersion: "managedkafka.bf2.org/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      kafkaRequest.Name,
			Namespace: kafkaRequest.Namespace,
			Annotations: map[string]string{
				"bf2.org/id":          kafkaRequest.ID,
				"bf2.org/placementId": kafkaRequest.PlacementId,
			},
			Labels: labels,
		},
		Spec: managedkafka.ManagedKafkaSpec{
			Capacity: managedkafka.Capacity{
				IngressPerSec:               k.IngressThroughputPerSec.String(),
				EgressPerSec:                k.EgressThroughputPerSec.String(),
				TotalMaxConnections:         k.TotalMaxConnections,
				MaxDataRetentionSize:        kafkaRequest.KafkaStorageSize,
				MaxPartitions:               k.MaxPartitions,
				MaxDataRetentionPeriod:      k.MaxDataRetentionPeriod,
				MaxConnectionAttemptsPerSec: k.MaxConnectionAttemptsPerSec,
			},
			Endpoint: managedkafka.EndpointSpec{
				BootstrapServerHost: kafkaRequest.BootstrapServerHost,
			},
			Versions: managedkafka.VersionsSpec{
				Kafka:    kafkaRequest.DesiredKafkaVersion,
				Strimzi:  kafkaRequest.DesiredStrimziVersion,
				KafkaIBP: kafkaRequest.DesiredKafkaIBPVersion,
			},
			Deleted: kafkaRequest.Status == constants2.KafkaRequestStatusDeprovision.String(),
			Owners:  buildKafkaOwner(kafkaRequest, kafkaConfig),
		},
		Status: managedkafka.ManagedKafkaStatus{},
	}

	keycloakConfig := keycloakService.GetConfig()
	keycloakRealmConfig := keycloakService.GetRealmConfig()

	if keycloakConfig.EnableAuthenticationOnKafka {
		managedKafkaCR.Spec.OAuth = managedkafka.OAuthSpec{
			TokenEndpointURI:       keycloakRealmConfig.TokenEndpointURI,
			JwksEndpointURI:        keycloakRealmConfig.JwksEndpointURI,
			ValidIssuerEndpointURI: keycloakRealmConfig.ValidIssuerURI,
			UserNameClaim:          keycloakConfig.UserNameClaim,
			FallBackUserNameClaim:  keycloakConfig.FallBackUserNameClaim,
			CustomClaimCheck:       BuildCustomClaimCheck(kafkaRequest, keycloakConfig.SelectSSOProvider),
			MaximumSessionLifetime: 0,
		}

		if keycloakConfig.TLSTrustedCertificatesValue != "" {
			managedKafkaCR.Spec.OAuth.TlsTrustedCertificate = &keycloakConfig.TLSTrustedCertificatesValue
		}

		if kafkaRequest.ReauthenticationEnabled {
			managedKafkaCR.Spec.OAuth.MaximumSessionLifetime = 299000 // 4m59s
		}

		serviceAccounts := []managedkafka.ServiceAccount{}
		serviceAccounts = append(serviceAccounts, managedkafka.ServiceAccount{
			Name:      "canary",
			Principal: kafkaRequest.CanaryServiceAccountClientID,
			Password:  kafkaRequest.CanaryServiceAccountClientSecret,
		})
		managedKafkaCR.Spec.ServiceAccounts = serviceAccounts
	}

	if kafkaConfig.EnableKafkaExternalCertificate {
		managedKafkaCR.Spec.Endpoint.Tls = &managedkafka.TlsSpec{
			Cert: kafkaConfig.KafkaTLSCert,
			Key:  kafkaConfig.KafkaTLSKey,
		}
	}

	return managedKafkaCR, nil
}

// buildReservedManagedKafkaCR builds a Reserved Managed Kafka CR.
// The ID, K8s object ID, K8s namespace and PlacementID are all set to
// the provided kafkaID.
func (k *kafkaService) buildReservedManagedKafkaCR(kafkaID string, instanceType string, desiredStrimziVersion api.StrimziVersion) (*managedkafka.ManagedKafka, *errors.ServiceError) {
	// Reserved instances always make use of the streaming base unit which is x1.
	// For now we hardcode it but there might be a better alternative to it.
	streamingBaseUnit := "x1"
	kafkaInstanceSize, err := k.kafkaConfig.GetKafkaInstanceSize(instanceType, streamingBaseUnit)
	if err != nil {
		return nil, errors.NewWithCause(errors.ErrorGeneral, err, "unable to list kafka request")
	}
	labels := map[string]string{
		"bf2.org/kafkaInstanceProfileQuotaConsumed":    strconv.Itoa(kafkaInstanceSize.QuotaConsumed),
		"bf2.org/kafkaInstanceProfileType":             instanceType,
		managedkafka.ManagedKafkaBf2DeploymentLabelKey: managedkafka.ManagedKafkaBf2DeploymentLabelValueReserved,
	}

	desiredKafkaVersion := desiredStrimziVersion.GetLatestKafkaVersion()
	if desiredKafkaVersion == nil {
		return nil, errors.GeneralError("no available Kafka versions in Strimzi version %s", desiredStrimziVersion.Version)
	}
	desiredKafkaIBPVersion := desiredStrimziVersion.GetLatestKafkaIBPVersion()
	if desiredKafkaIBPVersion == nil {
		return nil, errors.GeneralError("no available Kafka IBP versions in Strimzi version %s", desiredStrimziVersion.Version)
	}

	managedKafkaCR := &managedkafka.ManagedKafka{
		Id: kafkaID,
		TypeMeta: metav1.TypeMeta{
			Kind:       "ManagedKafka",
			APIVersion: "managedkafka.bf2.org/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      kafkaID,
			Namespace: kafkaID,
			Annotations: map[string]string{
				"bf2.org/id":          kafkaID,
				"bf2.org/placementId": kafkaID,
			},
			Labels: labels,
		},
		Spec: managedkafka.ManagedKafkaSpec{
			Capacity: managedkafka.Capacity{
				IngressPerSec:               kafkaInstanceSize.IngressThroughputPerSec.String(),
				EgressPerSec:                kafkaInstanceSize.EgressThroughputPerSec.String(),
				TotalMaxConnections:         kafkaInstanceSize.TotalMaxConnections,
				MaxDataRetentionSize:        kafkaInstanceSize.MaxDataRetentionSize.String(),
				MaxPartitions:               kafkaInstanceSize.MaxPartitions,
				MaxDataRetentionPeriod:      kafkaInstanceSize.MaxDataRetentionPeriod,
				MaxConnectionAttemptsPerSec: kafkaInstanceSize.MaxConnectionAttemptsPerSec,
			},
			Endpoint: managedkafka.EndpointSpec{
				BootstrapServerHost: fmt.Sprintf("%s-dummyhost", kafkaID),
			},
			Versions: managedkafka.VersionsSpec{
				Kafka:    desiredKafkaVersion.Version,
				Strimzi:  desiredStrimziVersion.Version,
				KafkaIBP: desiredKafkaIBPVersion.Version,
			},
			Deleted: false,
			Owners:  []string{}, // TODO is this enough?
		},
		Status: managedkafka.ManagedKafkaStatus{},
	}
	return managedKafkaCR, nil
}

func buildKafkaOwner(kafkaRequest *dbapi.KafkaRequest, kafkaConfig *config.KafkaConfig) []string {
	if kafkaConfig.EnableKafkaOwnerConfig {
		return append([]string{kafkaRequest.Owner}, kafkaConfig.KafkaOwnerList...)
	}
	return []string{
		kafkaRequest.Owner,
	}
}

func buildKafkaClusterCNAMESRecordBatch(routes []dbapi.DataPlaneKafkaRoute, action string) *route53.ChangeBatch {
	var changes []*route53.Change
	for _, r := range routes {
		c := buildResourceRecordChange(r.Domain, r.Router, action)
		changes = append(changes, c)
	}
	recordChangeBatch := &route53.ChangeBatch{
		Changes: changes,
	}

	return recordChangeBatch
}

func buildResourceRecordChange(recordName string, clusterIngress string, action string) *route53.Change {
	recordType := "CNAME"
	recordTTL := int64(300)

	resourceRecordChange := &route53.Change{
		Action: &action,
		ResourceRecordSet: &route53.ResourceRecordSet{
			Name: &recordName,
			Type: &recordType,
			TTL:  &recordTTL,
			ResourceRecords: []*route53.ResourceRecord{
				{
					Value: &clusterIngress,
				},
			},
		},
	}

	return resourceRecordChange
}
