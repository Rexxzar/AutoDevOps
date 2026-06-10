package cloud

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// awsProvider lists every resource type AutoDevOps can provision for one AWS
// account — EC2 instances, RDS databases, and ECS (Fargate) services — using
// static credentials decrypted from the secrets vault.
type awsProvider struct {
	cloudAccountID string
	region         string
	ec2Client      *ec2.Client
	rdsClient      *rds.Client
	ecsClient      *ecs.Client
}

// newAWSProvider builds a provider scoped to a single region — region is
// passed separately from creds.Region so the poller can also query regions
// where AutoDevOps has provisioned resources for this account, not just its
// registered home region (see Poller.listAWSResources).
func newAWSProvider(ctx context.Context, cloudAccountID, region string, creds awsCredentials) (Provider, error) {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("cloud: aws credentials require access_key_id and secret_access_key")
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("cloud: load aws config: %w", err)
	}

	return &awsProvider{
		cloudAccountID: cloudAccountID,
		region:         region,
		ec2Client:      ec2.NewFromConfig(cfg),
		rdsClient:      rds.NewFromConfig(cfg),
		ecsClient:      ecs.NewFromConfig(cfg),
	}, nil
}

// ListResources lists every EC2 instance, RDS database, and ECS service in
// the account — the AWS-side resource types AutoDevOps can provision via
// templates/aws/stack.tf.tmpl (EC2, RDS, ECS), so the dashboard's Cloud
// Inventory mirrors what the wizard offers.
func (p *awsProvider) ListResources(ctx context.Context) ([]Resource, error) {
	var resources []Resource

	instances, err := p.listEC2Instances(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, instances...)

	dbs, err := p.listRDSInstances(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, dbs...)

	services, err := p.listECSServices(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, services...)

	return resources, nil
}

func (p *awsProvider) listEC2Instances(ctx context.Context) ([]Resource, error) {
	out, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
	if err != nil {
		return nil, fmt.Errorf("cloud: aws describe instances: %w", err)
	}

	var resources []Resource
	for _, reservation := range out.Reservations {
		for _, instance := range reservation.Instances {
			resources = append(resources, ec2InstanceToResource(p.cloudAccountID, p.region, instance))
		}
	}
	return resources, nil
}

func (p *awsProvider) listRDSInstances(ctx context.Context) ([]Resource, error) {
	var resources []Resource
	paginator := rds.NewDescribeDBInstancesPaginator(p.rdsClient, &rds.DescribeDBInstancesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: aws describe db instances: %w", err)
		}
		for _, db := range page.DBInstances {
			resources = append(resources, rdsInstanceToResource(p.cloudAccountID, p.region, db))
		}
	}
	return resources, nil
}

func (p *awsProvider) listECSServices(ctx context.Context) ([]Resource, error) {
	var resources []Resource

	clusterPaginator := ecs.NewListClustersPaginator(p.ecsClient, &ecs.ListClustersInput{})
	for clusterPaginator.HasMorePages() {
		clusterPage, err := clusterPaginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: aws list ecs clusters: %w", err)
		}

		for _, clusterArn := range clusterPage.ClusterArns {
			servicePaginator := ecs.NewListServicesPaginator(p.ecsClient, &ecs.ListServicesInput{Cluster: aws.String(clusterArn)})
			for servicePaginator.HasMorePages() {
				servicePage, err := servicePaginator.NextPage(ctx)
				if err != nil {
					return nil, fmt.Errorf("cloud: aws list ecs services: %w", err)
				}
				if len(servicePage.ServiceArns) == 0 {
					continue
				}

				described, err := p.ecsClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
					Cluster:  aws.String(clusterArn),
					Services: servicePage.ServiceArns,
				})
				if err != nil {
					return nil, fmt.Errorf("cloud: aws describe ecs services: %w", err)
				}
				for _, svc := range described.Services {
					resources = append(resources, ecsServiceToResource(p.cloudAccountID, p.region, svc))
				}
			}
		}
	}

	return resources, nil
}

// ec2InstanceToResource maps one AWS SDK instance into the provider-agnostic
// Resource shape. Pure mapping, no SDK calls — exercised directly in tests
// with synthetic ec2types.Instance values (no network/credentials needed).
func ec2InstanceToResource(cloudAccountID, region string, instance ec2types.Instance) Resource {
	var name string
	for _, tag := range instance.Tags {
		if aws.ToString(tag.Key) == "Name" {
			name = aws.ToString(tag.Value)
			break
		}
	}

	state := map[string]any{
		"instance_type": string(instance.InstanceType),
	}
	if instance.State != nil {
		state["state"] = string(instance.State.Name)
	}
	if instance.PublicIpAddress != nil {
		state["public_ip"] = aws.ToString(instance.PublicIpAddress)
	}
	if instance.PrivateIpAddress != nil {
		state["private_ip"] = aws.ToString(instance.PrivateIpAddress)
	}

	return Resource{
		CloudAccountID: cloudAccountID,
		Provider:       ProviderAWS,
		ResourceType:   ResourceTypeEC2Instance,
		ResourceID:     aws.ToString(instance.InstanceId),
		Region:         region,
		Name:           name,
		State:          state,
	}
}

// rdsInstanceToResource maps one AWS SDK DB instance into the
// provider-agnostic Resource shape. Pure mapping, no SDK calls — exercised
// directly in tests with synthetic rdstypes.DBInstance values (no
// network/credentials needed).
func rdsInstanceToResource(cloudAccountID, region string, db rdstypes.DBInstance) Resource {
	state := map[string]any{
		"engine":         aws.ToString(db.Engine),
		"engine_version": aws.ToString(db.EngineVersion),
		"instance_class": aws.ToString(db.DBInstanceClass),
		"multi_az":       aws.ToBool(db.MultiAZ),
	}
	if db.DBInstanceStatus != nil {
		state["status"] = aws.ToString(db.DBInstanceStatus)
	}
	if db.Endpoint != nil {
		if db.Endpoint.Address != nil {
			state["endpoint"] = aws.ToString(db.Endpoint.Address)
		}
		if db.Endpoint.Port != nil {
			state["port"] = aws.ToInt32(db.Endpoint.Port)
		}
	}

	return Resource{
		CloudAccountID: cloudAccountID,
		Provider:       ProviderAWS,
		ResourceType:   ResourceTypeRDSInstance,
		ResourceID:     aws.ToString(db.DBInstanceArn),
		Region:         region,
		Name:           aws.ToString(db.DBInstanceIdentifier),
		State:          state,
	}
}

// ecsServiceToResource maps one AWS SDK ECS service into the
// provider-agnostic Resource shape. Pure mapping, no SDK calls — exercised
// directly in tests with synthetic ecstypes.Service values (no
// network/credentials needed).
func ecsServiceToResource(cloudAccountID, region string, svc ecstypes.Service) Resource {
	state := map[string]any{
		"launch_type":     string(svc.LaunchType),
		"desired_count":   svc.DesiredCount,
		"running_count":   svc.RunningCount,
		"task_definition": aws.ToString(svc.TaskDefinition),
		"cluster_arn":     aws.ToString(svc.ClusterArn),
	}
	if svc.Status != nil {
		state["status"] = aws.ToString(svc.Status)
	}

	return Resource{
		CloudAccountID: cloudAccountID,
		Provider:       ProviderAWS,
		ResourceType:   ResourceTypeECSService,
		ResourceID:     aws.ToString(svc.ServiceArn),
		Region:         region,
		Name:           aws.ToString(svc.ServiceName),
		State:          state,
	}
}
