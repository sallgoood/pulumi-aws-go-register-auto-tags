package dulumi

import (
	"fmt"
	aas "github.com/pulumi/pulumi-aws/sdk/v3/go/aws/appautoscaling"
	"github.com/pulumi/pulumi-aws/sdk/v3/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v3/go/aws/ecs"
	alb "github.com/pulumi/pulumi-aws/sdk/v3/go/aws/lb"
	plm "github.com/pulumi/pulumi/sdk/v2/go/pulumi"
	"github.com/sallgoood/dulumi/utils"
	"strconv"
)

type FargateApi struct {
	plm.ResourceState

	Dns plm.StringOutput `pulumi:"dnsName"`
}

func NewFargateApi(ctx *plm.Context,
	service string,
	env string,
	taskSubnetIds []string,
	taskSecurityGroupIds []string,
	lbSubnetIds []string,
	lbSecurityGroupIds []string,
	vpcId string,
	taskRole string,
	appPort int,
	appCpu string,
	appMemory string,
	appHealthCheckPath string,
	certificateArn string,
	scaleMax int,
	scaleMin int,
	scaleCpuPercent float64,
	containerDefinitions string,
	opts ...plm.ResourceOption,
) (*FargateApi, error) {

	if appPort == 0 {
		appPort = 80
	}

	var dfa FargateApi
	err := ctx.RegisterComponentResource("drama:server:fargate-api", "drama-fargate-api", &dfa, opts...)
	if err != nil {
		return nil, err
	}

	cluster, err := ecs.LookupCluster(ctx, &ecs.LookupClusterArgs{
		ClusterName: service,
	})
	if err != nil {
		return nil, err
	}

	lb, err := alb.NewLoadBalancer(ctx, "alb", &alb.LoadBalancerArgs{
		Name:           plm.String(fmt.Sprintf("%v-%v", service, env)),
		Subnets:        utils.ToPulumiStringArray(lbSubnetIds),
		SecurityGroups: utils.ToPulumiStringArray(lbSecurityGroupIds),
	}, plm.Parent(&dfa))
	if err != nil {
		return nil, err
	}
	tg, err := alb.NewTargetGroup(ctx, "targetGroup", &alb.TargetGroupArgs{
		Name:                plm.String(fmt.Sprintf("%v-%v", service, env)),
		Port:                plm.Int(appPort),
		Protocol:            plm.String("HTTP"),
		TargetType:          plm.String("ip"),
		VpcId:               plm.String(vpcId),
		DeregistrationDelay: plm.Int(1),
		HealthCheck: alb.TargetGroupHealthCheckArgs{
			Enabled:            plm.BoolPtr(true),
			HealthyThreshold:   plm.IntPtr(3),
			UnhealthyThreshold: plm.IntPtr(3),
			Interval:           plm.IntPtr(30),
			Matcher:            plm.StringPtr("200-299"),
			Path:               plm.StringPtr(appHealthCheckPath),
			Port:               plm.StringPtr(strconv.Itoa(appPort)),
			Protocol:           plm.StringPtr("HTTP"),
			Timeout:            plm.IntPtr(5),
		},
	}, plm.Parent(&dfa))
	if err != nil {
		return nil, err
	}
	_, err = alb.NewListener(ctx, "httpListener", &alb.ListenerArgs{
		LoadBalancerArn: lb.Arn,
		Port:            plm.Int(80),
		DefaultActions: alb.ListenerDefaultActionArray{
			alb.ListenerDefaultActionArgs{
				Type: plm.String("redirect"),
				Redirect: alb.ListenerDefaultActionRedirectArgs{
					Port:       plm.StringPtr("443"),
					Protocol:   plm.StringPtr("HTTPS"),
					StatusCode: plm.String("HTTP_301"),
				},
			},
		},
	}, plm.Parent(&dfa))
	if err != nil {
		return nil, err
	}
	https, err := alb.NewListener(ctx, "httpsListener", &alb.ListenerArgs{
		LoadBalancerArn: lb.Arn,
		Protocol:        plm.String("HTTPS"),
		Port:            plm.Int(443),
		CertificateArn:  plm.StringPtr(certificateArn),
		DefaultActions: alb.ListenerDefaultActionArray{
			alb.ListenerDefaultActionArgs{
				Type:           plm.String("forward"),
				TargetGroupArn: tg.Arn,
			},
		},
	}, plm.Parent(&dfa))
	if err != nil {
		return nil, err
	}

	if err = ctx.RegisterResourceOutputs(&dfa, plm.Map{
		"dns": lb.DnsName,
	}); err != nil {
		return nil, err
	}

	logGroup, err := cloudwatch.NewLogGroup(ctx, "logGroup", &cloudwatch.LogGroupArgs{
		Name:            plm.String(fmt.Sprintf("%v-%v", service, env)),
		RetentionInDays: plm.IntPtr(30),
	})
	if err != nil {
		return nil, err
	}

	initialTask, err := ecs.NewTaskDefinition(ctx, "ecsTaskDefinition", &ecs.TaskDefinitionArgs{
		Family:                  plm.String(fmt.Sprintf("%v-%v", service, env)),
		Cpu:                     plm.String(appCpu),
		Memory:                  plm.String(appMemory),
		NetworkMode:             plm.String("awsvpc"),
		RequiresCompatibilities: plm.StringArray{plm.String("FARGATE")},
		TaskRoleArn:             plm.String(taskRole),
		ExecutionRoleArn:        plm.String(taskRole),
		ContainerDefinitions:    plm.String(containerDefinitions),
	}, plm.Parent(&dfa),
		plm.DependsOn([]plm.Resource{logGroup}))
	if err != nil {
		return nil, err
	}

	svc, err := ecs.NewService(ctx, "ecsService", &ecs.ServiceArgs{
		Name:           plm.String(fmt.Sprintf("%v-%v", service, env)),
		Cluster:        plm.String(cluster.Arn),
		TaskDefinition: initialTask.Arn,
		DesiredCount:   plm.Int(1),
		LaunchType:     plm.String("FARGATE"),
		DeploymentController: ecs.ServiceDeploymentControllerArgs{
			Type: plm.StringPtr("ECS"),
		},
		NetworkConfiguration: &ecs.ServiceNetworkConfigurationArgs{
			AssignPublicIp: plm.Bool(true),
			Subnets:        utils.ToPulumiStringArray(taskSubnetIds),
			SecurityGroups: utils.ToPulumiStringArray(taskSecurityGroupIds),
		},
		LoadBalancers: ecs.ServiceLoadBalancerArray{
			ecs.ServiceLoadBalancerArgs{
				TargetGroupArn: tg.Arn,
				ContainerName:  plm.String("app"),
				ContainerPort:  plm.Int(appPort),
			},
		},
	}, plm.DependsOn([]plm.Resource{https}),
		plm.Parent(&dfa))
	if err != nil {
		return nil, err
	}

	autoscaleResourceId := plm.String(fmt.Sprintf("service/%v/%v", cluster.ClusterName, fmt.Sprintf("%v-%v", service, env)))

	_, err = aas.NewTarget(ctx, "autoscaleTarget", &aas.TargetArgs{
		MaxCapacity:       plm.Int(scaleMax),
		MinCapacity:       plm.Int(scaleMin),
		ResourceId:        autoscaleResourceId,
		ScalableDimension: plm.String("ecs:service:DesiredCount"),
		ServiceNamespace:  plm.String("ecs"),
	}, plm.Parent(&dfa),
		plm.DependsOn([]plm.Resource{svc}))
	if err != nil {
		return nil, err
	}

	_, err = aas.NewPolicy(ctx, "autoscalePolicy", &aas.PolicyArgs{
		Name:              plm.String("scale-inout"),
		PolicyType:        plm.String("TargetTrackingScaling"),
		ResourceId:        autoscaleResourceId,
		ScalableDimension: plm.String("ecs:service:DesiredCount"),
		ServiceNamespace:  plm.String("ecs"),
		TargetTrackingScalingPolicyConfiguration: aas.PolicyTargetTrackingScalingPolicyConfigurationArgs{
			CustomizedMetricSpecification: nil,
			DisableScaleIn:                nil,
			PredefinedMetricSpecification: aas.PolicyTargetTrackingScalingPolicyConfigurationPredefinedMetricSpecificationArgs{
				PredefinedMetricType: plm.String("ECSServiceAverageCPUUtilization"),
			},
			ScaleInCooldown:  plm.IntPtr(30),
			ScaleOutCooldown: plm.IntPtr(1),
			TargetValue:      plm.Float64(scaleCpuPercent),
		},
	}, plm.Parent(&dfa),
		plm.DependsOn([]plm.Resource{svc}))
	if err != nil {
		return nil, err
	}

	return &dfa, nil
}