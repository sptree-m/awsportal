package awsapi

import (
 "context"
 "fmt"
 "github.com/aws/aws-sdk-go-v2/aws"
 "github.com/aws/aws-sdk-go-v2/service/ec2"
)
type EC2 struct{ c *ec2.Client }
func New(cfg aws.Config)*EC2{return &EC2{c:ec2.NewFromConfig(cfg)}}
func(e *EC2)Start(ctx context.Context,id string)error{ if id==""{return fmt.Errorf("empty instance id")}; _,err:=e.c.StartInstances(ctx,&ec2.StartInstancesInput{InstanceIds:[]string{id}}); return err }
func(e *EC2)Stop(ctx context.Context,id string)error{ if id==""{return fmt.Errorf("empty instance id")}; _,err:=e.c.StopInstances(ctx,&ec2.StopInstancesInput{InstanceIds:[]string{id}}); return err }
