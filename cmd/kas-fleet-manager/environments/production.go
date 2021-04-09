package environments

import (
	"github.com/bf2fc6cc711aee1a0c2a/kas-fleet-manager/pkg/db"
)

var productionConfigDefaults map[string]string = map[string]string{
	"v":                                 "1",
	"ocm-debug":                         "false",
	"enable-ocm-mock":                   "false",
	"enable-sentry":                     "true",
	"enable-deny-list":                  "true",
	"max-allowed-instances":             "1",
	"mas-sso-realm":                     "rhoas",
	"mas-sso-base-url":                  "https://keycloak-mas-sso-stage.apps.app-sre-stage-0.k3s7.p1.openshiftapps.com",
	"enable-kafka-external-certificate": "true",
	"cluster-compute-machine-type":      "m5.4xlarge",
	"ingress-controller-replicas":       "9",
	"enable-quota-service":              "true",
}

func loadProduction(env *Env) error {
	env.DBFactory = db.NewConnectionFactory(env.Config.Database)

	err := env.LoadClients()
	if err != nil {
		return err
	}
	err = env.LoadServices()
	if err != nil {
		return err
	}

	return env.InitializeSentry()
}
