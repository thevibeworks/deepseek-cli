// dsgate is a separate module from the CLI on purpose.
//
// The CLI's dependency list is part of its pitch — two direct
// dependencies, everything else stdlib — and a server has no business
// widening it. Keeping the module boundary here means `go install
// github.com/thevibeworks/deepseek-cli/cmd/deepseek@latest` never pulls
// a line of gateway code.
//
// As it turns out the gateway needs nothing either. This file has no
// require block and is expected to stay that way.
module github.com/thevibeworks/deepseek-cli/gateway

go 1.26.5
