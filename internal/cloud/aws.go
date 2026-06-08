package cloud

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// awsProvider lists EC2 instances for one AWS account using static
// credentials decrypted from the secrets vault.
type awsProvider struct {
	cloudAccountID string
	region         string
	client         *ec2.Client
}

func newAWSProvider(ctx context.Context, cloudAccountID string, creds awsCredentials) (Provider, error) {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" || creds.Region == "" {
		return nil, fmt.Errorf("cloud: aws credentials require access_key_id, secret_access_key and region")
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(creds.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("cloud: load aws config: %w", err)
	}

	return &awsProvider{
		cloudAccountID: cloudAccountID,
		region:         creds.Region,
		client:         ec2.NewFromConfig(cfg),
	}, nil
}

func (p *awsProvider) ListResources(ctx context.Context) ([]Resource, error) {
	out, err := p.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
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
