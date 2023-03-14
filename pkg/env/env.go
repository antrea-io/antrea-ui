package env

import (
	"os"
)

func IsProductionEnv() bool {
	return os.Getenv("APP_ENV") == "production"
}

func GetNamespace() string {
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		return "kube-system"
	}
	return ns
}
