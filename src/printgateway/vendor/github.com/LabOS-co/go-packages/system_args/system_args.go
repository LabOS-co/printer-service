package system_args

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// Environment keys
const (
	portKey       = "PORT"
	envKey        = "LABOS_ENV"
	gatewayKey    = "GATEWAY"
	consulAddrKey = "CONSUL_ADDR"
)

// Default values
const (
	defaultPort           = 8082
	defaultEnv            = ""
	defaultGateway        = ""
	defaultConsulAddr     = ""
	defaultConsulRegister = false
	defaultPublishers     = ""
	defaultServices       = ""
	defaultDisplayVersion = false
)

var (
	once             sync.Once
	port             int
	env              string
	gateway          string
	consulAddr       string
	consulRegister   bool
	activePublishers string
	displayVersion   bool
	activeServices   string
)

func parseArgs() {
	flag.IntVar(&port, "port", defaultPort, "set port number for the server run with")
	flag.IntVar(&port, "p", defaultPort, "set port number for the server run with")
	flag.StringVar(&env, "env", defaultEnv, "set environment name")
	flag.StringVar(&gateway, "gateway", defaultGateway, "set gateway")
	flag.StringVar(&consulAddr, "consul-addr", defaultConsulAddr, "set consul address")
	flag.BoolVar(&consulRegister, "consul-register", defaultConsulRegister, "register to consul")
	flag.StringVar(&activePublishers, "publishers", defaultPublishers, "units to run")
	flag.StringVar(&activeServices, "services", defaultServices, "services to activate")
	flag.BoolVar(&displayVersion, "version", defaultDisplayVersion, "show version")
	flag.BoolVar(&displayVersion, "v", defaultDisplayVersion, "show version")
	flag.Parse()

	getEnvValue(&port, defaultPort, portKey)
	getEnvValue(&env, defaultEnv, envKey)
	getEnvValue(&gateway, defaultGateway, gatewayKey)
	getEnvValue(&consulAddr, defaultConsulAddr, consulAddrKey)
}

func initialize() {
	once.Do(parseArgs)
}

func GetPort() int {
	initialize()
	return port
}

func GetEnvironment() string {
	initialize()
	return env
}

func GetGateway() string {
	initialize()
	return gateway
}

func GetConsulAddress() string {
	initialize()
	return consulAddr
}

func ShouldRegisterToConsul() bool {
	initialize()
	return consulRegister
}

func GetPublishers() []string {
	initialize()

	if activePublishers == "" {
		return []string{}
	}

	return strings.Split(activePublishers, ",")
}

func GetServices() []string {
	initialize()

	if activeServices == "" {
		return []string{}
	}

	return strings.Split(activeServices, ",")
}

func DisplayVersion() bool {
	initialize()
	return displayVersion
}

func getEnvValue(value interface{}, defaultValue interface{}, envKey string) {
	reflectValue := reflect.ValueOf(value).Elem()
	defaultReflectValue := reflect.ValueOf(&defaultValue).Elem()
	if reflect.DeepEqual(reflectValue.Interface(), defaultReflectValue.Interface()) {
		envValue := os.Getenv(envKey)
		if envValue != "" {
			err := setValueFromEnv(reflectValue, envValue)
			if err != nil {
				panic(err)
			}
		}
	}
}

func setValueFromEnv(reflectValue reflect.Value, envValue string) error {
	var err error
	switch reflectValue.Kind() {
	case reflect.String:
		reflectValue.SetString(envValue)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var intValue int64
		intValue, err = strconv.ParseInt(envValue, 10, 64)
		if err == nil {
			reflectValue.SetInt(intValue)
		}
	case reflect.Float32, reflect.Float64:
		var floatValue float64
		floatValue, err = strconv.ParseFloat(envValue, 64)
		if err == nil {
			reflectValue.SetFloat(floatValue)
		}
	case reflect.Bool:
		var boolValue bool
		boolValue, err = strconv.ParseBool(envValue)
		if err == nil {
			reflectValue.SetBool(boolValue)
		}
	default:
		err = fmt.Errorf("unsupported type: %v", reflectValue.Kind())
	}

	return err
}
