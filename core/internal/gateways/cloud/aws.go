package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// awsConsole signs people in to the AWS console as the target's IAM role
// (its address, an ARN), through AWS's federation endpoint, the way a
// custom identity broker does. Grantra assumes the role with the access key
// of an IAM user (the vault account): AWS lets only long-term credentials
// make console sessions last as long as the access does.
type awsConsole struct {
	c                   *Consoles
	t                   gateways.Target
	accessKeyID, secret string
}

// awsPartition is where a partition's STS region, sign-in and console are.
type awsPartition struct{ region, signin, console string }

var awsPartitions = map[string]awsPartition{
	"aws":        {"us-east-1", "https://signin.aws.amazon.com/federation", "https://console.aws.amazon.com/"},
	"aws-us-gov": {"us-gov-west-1", "https://signin.amazonaws-us-gov.com/federation", "https://console.amazonaws-us-gov.com/"},
	"aws-cn":     {"cn-north-1", "https://signin.amazonaws.cn/federation", "https://console.amazonaws.cn/"},
}

func (a *awsConsole) partition() awsPartition {
	part, _, _ := strings.Cut(strings.TrimPrefix(a.t.Address, "arn:"), ":")
	return awsPartitions[part]
}

var awsSessionNameUnsafe = regexp.MustCompile(`[^\w+=,.@-]`)

// awsSessionName names the role session after the person, so CloudTrail
// shows who it was: assumed-role/<role>/<email>.
func awsSessionName(email string) string {
	name := awsSessionNameUnsafe.ReplaceAllString(email, "-")
	if len(name) > 64 {
		name = name[:64]
	}
	if len(name) < 2 {
		name = "grantra-" + name
	}
	return name
}

func (a *awsConsole) open(ctx context.Context, p person, until time.Time, _ string) (granted, error) {
	part := a.partition()
	if a.accessKeyID == "" || a.secret == "" {
		return granted{}, errors.New(a.t.Name + " has no access key to assume its role with")
	}
	opts := sts.Options{Region: part.region, HTTPClient: a.c.httpClient(),
		Credentials: credentials.NewStaticCredentialsProvider(a.accessKeyID, a.secret, "")}
	if a.c.Endpoints.AWSSTS != "" {
		opts.BaseEndpoint = aws.String(a.c.Endpoints.AWSSTS)
	}
	seconds := int32(time.Until(until) / time.Second)
	out, err := sts.New(opts).AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         aws.String(a.t.Address),
		RoleSessionName: aws.String(awsSessionName(p.email)),
		DurationSeconds: aws.Int32(seconds),
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) {
			return granted{}, &Refused{"AWS refused to let Grantra assume " + a.t.Address + ": " + api.ErrorCode() + ": " + api.ErrorMessage()}
		}
		return granted{}, err
	}
	if out.Credentials == nil || out.Credentials.AccessKeyId == nil || out.Credentials.SecretAccessKey == nil || out.Credentials.SessionToken == nil {
		return granted{}, errors.New("AWS returned no credentials")
	}
	creds := &AWSCredentials{AccessKeyID: *out.Credentials.AccessKeyId, SecretAccessKey: *out.Credentials.SecretAccessKey,
		SessionToken: *out.Credentials.SessionToken, Expiration: aws.ToTime(out.Credentials.Expiration)}
	account := ""
	if out.AssumedRoleUser != nil {
		account = aws.ToString(out.AssumedRoleUser.Arn)
	}
	link, err := a.signin(ctx, part, creds)
	if err != nil {
		return granted{}, err
	}
	return granted{url: link, account: account, credentials: creds}, nil
}

// signin exchanges role credentials for a console sign-in link, valid for
// 15 minutes. The console session lasts as long as the credentials.
func (a *awsConsole) signin(ctx context.Context, part awsPartition, creds *AWSCredentials) (string, error) {
	endpoint := part.signin
	if a.c.Endpoints.AWSSignin != "" {
		endpoint = a.c.Endpoints.AWSSignin
	}
	session, err := json.Marshal(map[string]string{"sessionId": creds.AccessKeyID, "sessionKey": creds.SecretAccessKey,
		"sessionToken": creds.SessionToken})
	if err != nil {
		return "", err
	}
	// POST keeps the credentials out of URLs.
	form := url.Values{"Action": {"getSigninToken"}, "Session": {string(session)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct{ SigninToken string }
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil || out.SigninToken == "" {
		return "", &Refused{"AWS sign-in refused the role's credentials (HTTP " + resp.Status + ")"}
	}
	q := url.Values{"Action": {"login"}, "Issuer": {a.c.Issuer}, "Destination": {part.console}, "SigninToken": {out.SigninToken}}
	return endpoint + "?" + q.Encode(), nil
}

// url is never used on AWS: every sign-in is a new session.
func (a *awsConsole) url(person) string { return a.partition().console }

// close has nothing to take back: the credentials end on their own.
func (a *awsConsole) close(context.Context, string) error { return nil }
