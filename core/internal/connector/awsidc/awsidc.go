// Package awsidc connects Grantra to AWS IAM Identity Center (formerly AWS
// SSO): who can reach which AWS account, and with which permission set.
// Groups and account assignments (a permission set on an account) become
// entitlements; Identity Center users become accounts. It only reads:
// people and groups usually come into Identity Center from the company's
// identity provider, where Grantra changes them.
package awsidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	istypes "github.com/aws/aws-sdk-go-v2/service/identitystore/types"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/ssoadmin/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

// Kind is the connector name applications use.
const Kind = "aws-identity-center"

func init() { connector.Register(Kind, New) }

// Config is the AWS IAM Identity Center connector's settings.
type Config struct {
	// Region is where the Identity Center instance lives, e.g. eu-west-1.
	Region string `json:"region"`
	// InstanceARN picks the instance when the account sees several.
	// Default the only one.
	InstanceARN string `json:"instance_arn,omitempty"`
	// AccessKeyID and SecretAccessKey sign in as an IAM user; without them
	// Grantra uses the server's own AWS identity (environment, IAM role for
	// service accounts, instance role).
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	// RoleARN is a role to assume first, such as a read-only role in the
	// organization's management account.
	RoleARN string `json:"role_arn,omitempty"`
	// EndpointURL is for tests.
	EndpointURL string `json:"endpoint_url,omitempty"`
	AllowHTTP   bool   `json:"allow_http,omitempty"`
}

// Client reads one Identity Center instance.
type Client struct {
	store    *identitystore.Client
	admin    *ssoadmin.Client
	instance string
}

// New builds an AWS IAM Identity Center connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		return nil, errors.New("aws-identity-center config: region is required, e.g. eu-west-1")
	}
	if cfg.InstanceARN != "" && !strings.HasPrefix(cfg.InstanceARN, "arn:") {
		return nil, fmt.Errorf("aws-identity-center config: instance_arn %q is not an ARN", cfg.InstanceARN)
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	if cfg.AccessKeyID != "" {
		secret, err := connector.Secret(Kind, "secret_access_key", cfg.SecretAccessKey, "env:GRANTRA_SECRET_AWS_KEY", secrets)
		if err != nil {
			return nil, err
		}
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, secret, "")))
	} else if cfg.SecretAccessKey != "" {
		return nil, errors.New("aws-identity-center config: secret_access_key needs access_key_id")
	}
	var endpoint *string
	if cfg.EndpointURL != "" {
		u, err := httpapi.BaseURL(Kind, "endpoint_url", cfg.EndpointURL, cfg.AllowHTTP)
		if err != nil {
			return nil, err
		}
		endpoint = aws.String(u.String())
	}
	// Loading only reads settings; credentials are fetched on the first call.
	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("aws-identity-center config: %w", err)
	}
	if cfg.RoleARN != "" {
		stsClient := sts.NewFromConfig(awsCfg, func(o *sts.Options) { o.BaseEndpoint = endpoint })
		awsCfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(stsClient, cfg.RoleARN,
			func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = "grantra" }))
	}
	return &Client{
		store:    identitystore.NewFromConfig(awsCfg, func(o *identitystore.Options) { o.BaseEndpoint = endpoint }),
		admin:    ssoadmin.NewFromConfig(awsCfg, func(o *ssoadmin.Options) { o.BaseEndpoint = endpoint }),
		instance: cfg.InstanceARN,
	}, nil
}

// assignmentPrefix starts the native id of an account assignment:
// "assignment:<account id>:<permission set ARN>". Groups use their id.
const assignmentPrefix = "assignment:"

// pages reads every page of a paginated AWS list.
func pages[O, Opts any](ctx context.Context, p interface {
	HasMorePages() bool
	NextPage(context.Context, ...func(Opts)) (O, error)
}, fn func(O)) error {
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		fn(out)
	}
	return nil
}

// instanceOf finds the instance to read and its identity store.
func (c *Client) instanceOf(ctx context.Context) (instance, store string, err error) {
	var all []ssotypes.InstanceMetadata
	err = pages(ctx, ssoadmin.NewListInstancesPaginator(c.admin, &ssoadmin.ListInstancesInput{}),
		func(o *ssoadmin.ListInstancesOutput) { all = append(all, o.Instances...) })
	if err != nil {
		return "", "", fmt.Errorf("list Identity Center instances: %w", err)
	}
	for _, i := range all {
		if c.instance == "" && len(all) == 1 || aws.ToString(i.InstanceArn) == c.instance {
			return aws.ToString(i.InstanceArn), aws.ToString(i.IdentityStoreId), nil
		}
	}
	if c.instance != "" {
		return "", "", fmt.Errorf("no Identity Center instance %s in this account and region", c.instance)
	}
	return "", "", fmt.Errorf("found %d Identity Center instances in this account and region: set instance_arn", len(all))
}

// Read returns every group, account assignment and user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	instance, storeID, err := c.instanceOf(ctx)
	if err != nil {
		return connector.Snapshot{}, err
	}
	store := aws.String(storeID)
	var (
		b       connector.Builder
		groups  []istypes.Group
		members = map[string][]string{} // group id -> user ids
	)
	err = pages(ctx, identitystore.NewListGroupsPaginator(c.store, &identitystore.ListGroupsInput{IdentityStoreId: store}),
		func(o *identitystore.ListGroupsOutput) { groups = append(groups, o.Groups...) })
	if err != nil {
		return connector.Snapshot{}, fmt.Errorf("list groups: %w", err)
	}
	for _, g := range groups {
		id := aws.ToString(g.GroupId)
		b.Entitlement(connector.Entitlement{NativeID: id, Name: aws.ToString(g.DisplayName), Description: aws.ToString(g.Description)})
		err := pages(ctx, identitystore.NewListGroupMembershipsPaginator(c.store, &identitystore.ListGroupMembershipsInput{IdentityStoreId: store, GroupId: g.GroupId}),
			func(o *identitystore.ListGroupMembershipsOutput) {
				for _, m := range o.GroupMemberships {
					if u, ok := m.MemberId.(*istypes.MemberIdMemberUserId); ok {
						members[id] = append(members[id], u.Value)
						b.Grant(u.Value, id)
					}
				}
			})
		if err != nil {
			return connector.Snapshot{}, fmt.Errorf("list members of group %s: %w", aws.ToString(g.DisplayName), err)
		}
	}
	if err := c.readAssignments(ctx, instance, &b, members); err != nil {
		return connector.Snapshot{}, err
	}
	err = pages(ctx, identitystore.NewListUsersPaginator(c.store, &identitystore.ListUsersInput{IdentityStoreId: store}),
		func(o *identitystore.ListUsersOutput) {
			for _, u := range o.Users {
				b.Account(account(u))
			}
		})
	if err != nil {
		return connector.Snapshot{}, fmt.Errorf("list users: %w", err)
	}
	return b.Snapshot(), nil
}

func account(u istypes.User) connector.Account {
	a := connector.Account{
		NativeID:    aws.ToString(u.UserId),
		Username:    aws.ToString(u.UserName),
		DisplayName: aws.ToString(u.DisplayName),
		Active:      u.UserStatus != istypes.UserStatusDisabled,
		Attributes:  connector.Attributes("title", aws.ToString(u.Title), "user_type", aws.ToString(u.UserType)),
	}
	if a.DisplayName == "" && u.Name != nil {
		a.DisplayName = strings.TrimSpace(aws.ToString(u.Name.GivenName) + " " + aws.ToString(u.Name.FamilyName))
	}
	for _, e := range u.Emails {
		if a.Email == "" || e.Primary {
			a.Email = aws.ToString(e.Value)
		}
	}
	return a
}

// readAssignments reads which permission set each user and group has on
// each account. An assignment to a group is held by each of its members:
// they can all reach the account.
func (c *Client) readAssignments(ctx context.Context, instance string, b *connector.Builder, members map[string][]string) error {
	in := aws.String(instance)
	var sets []string
	err := pages(ctx, ssoadmin.NewListPermissionSetsPaginator(c.admin, &ssoadmin.ListPermissionSetsInput{InstanceArn: in}),
		func(o *ssoadmin.ListPermissionSetsOutput) { sets = append(sets, o.PermissionSets...) })
	if err != nil {
		return fmt.Errorf("list permission sets: %w", err)
	}
	for _, arn := range sets {
		d, err := c.admin.DescribePermissionSet(ctx, &ssoadmin.DescribePermissionSetInput{InstanceArn: in, PermissionSetArn: aws.String(arn)})
		if err != nil {
			return fmt.Errorf("describe permission set %s: %w", arn, err)
		}
		name := aws.ToString(d.PermissionSet.Name)
		var accounts []string
		err = pages(ctx, ssoadmin.NewListAccountsForProvisionedPermissionSetPaginator(c.admin,
			&ssoadmin.ListAccountsForProvisionedPermissionSetInput{InstanceArn: in, PermissionSetArn: aws.String(arn)}),
			func(o *ssoadmin.ListAccountsForProvisionedPermissionSetOutput) {
				accounts = append(accounts, o.AccountIds...)
			})
		if err != nil {
			return fmt.Errorf("list accounts of permission set %s: %w", name, err)
		}
		for _, acct := range accounts {
			id := assignmentPrefix + acct + ":" + arn
			b.Entitlement(connector.Entitlement{NativeID: id, Name: name + " on " + acct, Description: aws.ToString(d.PermissionSet.Description)})
			err := pages(ctx, ssoadmin.NewListAccountAssignmentsPaginator(c.admin,
				&ssoadmin.ListAccountAssignmentsInput{InstanceArn: in, AccountId: aws.String(acct), PermissionSetArn: aws.String(arn)}),
				func(o *ssoadmin.ListAccountAssignmentsOutput) {
					for _, a := range o.AccountAssignments {
						p := aws.ToString(a.PrincipalId)
						switch a.PrincipalType {
						case ssotypes.PrincipalTypeUser:
							b.Grant(p, id)
						case ssotypes.PrincipalTypeGroup:
							for _, u := range members[p] {
								b.Grant(u, id)
							}
						}
					}
				})
			if err != nil {
				return fmt.Errorf("list assignments of %s on %s: %w", name, acct, err)
			}
		}
	}
	return nil
}
