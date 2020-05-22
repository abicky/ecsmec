module github.com/abicky/ecsmec

go 1.15

require (
	github.com/aws/aws-sdk-go v1.36.28
	github.com/golang/mock v1.4.4
	github.com/imdario/mergo v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.1.1
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1
)

replace github.com/imdario/mergo => github.com/abicky/mergo v0.3.12-0.20210127171018-7c7592023899
