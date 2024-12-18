module github.com/abicky/ecsmec

go 1.23.2

require (
	dario.cat/mergo v1.0.1
	github.com/aws/aws-sdk-go-v2 v1.32.3
	github.com/aws/aws-sdk-go-v2/config v1.28.0
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.46.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.186.0
	github.com/aws/aws-sdk-go-v2/service/ecs v1.48.0
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.35.2
	github.com/aws/aws-sdk-go-v2/service/sqs v1.36.2
	github.com/spf13/cobra v1.8.1
	go.uber.org/mock v0.5.0
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da
)

require (
	github.com/aws/aws-sdk-go-v2/credentials v1.17.41 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.22 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.22 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.3.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.12.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.12.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.24.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.28.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.32.2 // indirect
	github.com/aws/smithy-go v1.22.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)

// TODO: Delete the next line after https://github.com/aws/aws-sdk-go-v2/issues/2859 is resolved.
replace github.com/aws/aws-sdk-go-v2/service/ecs => github.com/abicky/aws-sdk-go-v2/service/ecs v0.0.0-20241030044159-a8afd4a537b1
