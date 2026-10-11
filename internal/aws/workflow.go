package awsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/smithy-go"
	"github.com/sptree-m/awsportal/internal/store"
	"strconv"
	"strings"
)

type WorkflowClient struct {
	client               *sfn.Client
	provision, terminate string
}

func NewWorkflow(cfg sdk.Config, provision, terminate string) (*WorkflowClient, error) {
	for _, arn := range []string{provision, terminate} {
		if !strings.Contains(arn, ":stateMachine:") {
			return nil, fmt.Errorf("approved STANDARD state machine ARNs required")
		}
	}
	return &WorkflowClient{sfn.NewFromConfig(cfg), provision, terminate}, nil
}
func (w *WorkflowClient) Start(ctx context.Context, o store.CloudOperation) (string, error) {
	arn := w.provision
	if o.Kind == "TERMINATE" {
		arn = w.terminate
	}
	name := executionName(o)
	result, err := w.client.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: sdk.String(arn), Name: sdk.String(name), Input: sdk.String(o.Input)})
	if err == nil {
		return sdk.ToString(result.ExecutionArn), nil
	}
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "ExecutionAlreadyExists" {
		// Closed STANDARD executions reject StartExecution retries. The deterministic
		// execution ARN recovers the original instead of inventing another name.
		parts := strings.Split(arn, ":")
		if len(parts) < 7 {
			return "", err
		}
		return strings.Join(parts[:5], ":") + ":execution:" + parts[6] + ":" + name, nil
	}
	return "", err
}
func (w *WorkflowClient) Status(ctx context.Context, arn string) (string, store.CloudResult, error) {
	r, err := w.client.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: sdk.String(arn)})
	if err != nil {
		return "", store.CloudResult{}, err
	}
	var out store.CloudResult
	if string(r.Status) == "SUCCEEDED" {
		if err = json.Unmarshal([]byte(sdk.ToString(r.Output)), &out); err != nil {
			return "", out, err
		}
	}
	return string(r.Status), out, nil
}
func (w *WorkflowClient) Lookup(ctx context.Context, o store.CloudOperation) (string, bool, error) {
	arn := w.provision
	if o.Kind == "TERMINATE" {
		arn = w.terminate
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 7 {
		return "", false, fmt.Errorf("invalid workflow ARN")
	}
	execution := strings.Join(parts[:5], ":") + ":execution:" + parts[6] + ":" + executionName(o)
	_, err := w.client.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: sdk.String(execution)})
	if err == nil {
		return execution, true, nil
	}
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "ExecutionDoesNotExist" {
		return execution, false, nil
	}
	return execution, false, err
}

func executionName(o store.CloudOperation) string {
	name := "awsportal-" + o.ID
	var input struct {
		RetryAttempt int `json:"retry_attempt"`
	}
	if json.Unmarshal([]byte(o.Input), &input) == nil && input.RetryAttempt > 0 {
		name += "-retry-" + strconv.Itoa(input.RetryAttempt)
	}
	return name
}
