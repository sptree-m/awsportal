package awsapi
import("context";"fmt";"github.com/aws/aws-sdk-go-v2/aws";"github.com/aws/aws-sdk-go-v2/service/ec2")
type Controller interface{Start(context.Context,string)error;Stop(context.Context,string)error;State(context.Context,string)(string,error);States(context.Context,[]string)(map[string]string,error)}
type EC2 struct{c *ec2.Client}
func New(cfg aws.Config)*EC2{return &EC2{c:ec2.NewFromConfig(cfg)}}
func(e *EC2)Start(ctx context.Context,id string)error{if id==""{return fmt.Errorf("empty instance id")};_,err:=e.c.StartInstances(ctx,&ec2.StartInstancesInput{InstanceIds:[]string{id}});return err}
func(e *EC2)Stop(ctx context.Context,id string)error{if id==""{return fmt.Errorf("empty instance id")};_,err:=e.c.StopInstances(ctx,&ec2.StopInstancesInput{InstanceIds:[]string{id}});return err}
func(e *EC2)State(ctx context.Context,id string)(string,error){xs,err:=e.States(ctx,[]string{id});if err!=nil{return "",err};state,ok:=xs[id];if !ok{return "",fmt.Errorf("instance not found")};return state,nil}
func(e *EC2)States(ctx context.Context,ids []string)(map[string]string,error){out:=map[string]string{};if len(ids)==0{return out,nil};x,err:=e.c.DescribeInstances(ctx,&ec2.DescribeInstancesInput{InstanceIds:ids});if err!=nil{return nil,err};for _,r:=range x.Reservations{for _,i:=range r.Instances{if i.InstanceId!=nil&&i.State!=nil{out[*i.InstanceId]=string(i.State.Name)}}};return out,nil}
