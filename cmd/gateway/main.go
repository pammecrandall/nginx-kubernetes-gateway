package main

import (
	"fmt"
	"os"

	flag "github.com/spf13/pflag"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/nginxinc/nginx-kubernetes-gateway/internal/config"
	"github.com/nginxinc/nginx-kubernetes-gateway/internal/manager"
)

const (
	domain string = "k8s-gateway.nginx.org"
)

var (
	// Set during go build
	version string

	// Command-line flags
	gatewayCtlrName = flag.String(
		"gateway-ctlr-name",
		"",
		fmt.Sprintf("The name of the Gateway controller. The controller name must be of the form: DOMAIN/NAMESPACE/NAME. The controller's domain is '%s'.", domain),
	)

	gatewayClassName = flag.String(
		"gatewayclass",
		"",
		"The name of the GatewayClass resource. Every NGINX Gateway must have a unique corresponding GatewayClass resource")
)

func main() {
	flag.Parse()

	logger := zap.New()
	conf := config.Config{
		GatewayCtlrName:  *gatewayCtlrName,
		Logger:           logger,
		GatewayClassName: *gatewayClassName,
	}

	MustValidateArguments(
		flag.CommandLine,
		GatewayControllerParam(domain, "nginx-gateway" /* FIXME(f5yacobucci) dynamically set */),
		GatewayClassParam(),
	)

	commit, date, dirty := getBuildInfo()
	logger.Info("Starting NGINX Kubernetes Gateway",
		"version", version,
		"commit", commit,
		"dirty", dirty,
		"date", date)

	err := manager.Start(conf)
	if err != nil {
		logger.Error(err, "Failed to start control loop")
		os.Exit(1)
	}
}
