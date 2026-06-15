package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	smithy "github.com/aws/smithy-go"
)

// devIPRuleDescription/devIPFirewallRuleName tag the AWS security-group rule
// and Azure firewall rule created by authorizeAWSDevIP/authorizeAzureDevIP, so
// they're identifiable for future cleanup tooling (stale rules from previous
// dev IPs are not removed by this code — a known, documented limitation).
const (
	devIPRuleDescription  = "autodevops-dev-ip"
	devIPFirewallRuleName = "autodevops-dev-ip"
)

// getPublicIP returns the dev machine's current public IPv4 address via a
// short-timeout request to an IP-echo endpoint. Best-effort: callers must
// treat a non-nil error as "skip dev-IP injection for this deploy", not as a
// deploy failure.
func getPublicIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return "", fmt.Errorf("deploy: build public-ip request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("deploy: fetch public ip: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("deploy: read public-ip response: %w", err)
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("deploy: invalid public ip %q", ip)
	}
	return ip, nil
}

// authorizeAWSDevIP allows devIP/32 to reach port on the given security group
// (e.g. an RDS instance's SG, which by default only allows the VPC CIDR).
// Idempotent: an existing identical rule (InvalidPermission.Duplicate) is
// treated as success.
func (o *Orchestrator) authorizeAWSDevIP(ctx context.Context, cloudAccountID, region, securityGroupID string, port int, devIP string) error {
	if securityGroupID == "" || port == 0 {
		return nil
	}
	cfg, err := o.loadAWSConfig(ctx, cloudAccountID, region)
	if err != nil {
		return err
	}
	_, err = ec2.NewFromConfig(cfg).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(securityGroupID),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(int32(port)),
				ToPort:     aws.Int32(int32(port)),
				IpRanges: []ec2types.IpRange{
					{CidrIp: aws.String(devIP + "/32"), Description: aws.String(devIPRuleDescription)},
				},
			},
		},
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidPermission.Duplicate" {
			return nil
		}
		return fmt.Errorf("deploy: authorize dev ip on security group %s: %w", securityGroupID, err)
	}
	return nil
}

// authorizeAzureDevIP allows devIP to reach the Azure Postgres Flexible
// Server identified by outputs (resource_group_name, db_fqdn — the server
// name is the FQDN's first label, e.g. "myserver" from
// "myserver.postgres.database.azure.com"). Idempotent: BeginCreateOrUpdate on
// the same rule name overwrites it with the same start/end IP.
func (o *Orchestrator) authorizeAzureDevIP(ctx context.Context, cloudAccountID string, outputs map[string]any, devIP string) error {
	resourceGroupName := outputString(outputs, "resource_group_name")
	fqdn := outputString(outputs, "db_fqdn")
	if resourceGroupName == "" || fqdn == "" {
		return nil
	}
	serverName, _, _ := strings.Cut(fqdn, ".")
	if serverName == "" {
		return nil
	}

	cred, subscriptionID, err := o.loadAzureCredential(ctx, cloudAccountID)
	if err != nil {
		return err
	}
	client, err := armpostgresqlflexibleservers.NewFirewallRulesClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("deploy: build azure firewall rules client: %w", err)
	}
	poller, err := client.BeginCreateOrUpdate(ctx, resourceGroupName, serverName, devIPFirewallRuleName, armpostgresqlflexibleservers.FirewallRule{
		Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
			StartIPAddress: to.Ptr(devIP),
			EndIPAddress:   to.Ptr(devIP),
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("deploy: create azure dev-ip firewall rule: %w", err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("deploy: apply azure dev-ip firewall rule: %w", err)
	}
	return nil
}
