package system_api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/LabOS-co/go-packages/logs"
	"github.com/LabOS-co/go-packages/system_args"
	"github.com/go-chi/chi/v5"
	"github.com/version-go/ldflags"
)

const (
	check_api          = "/status"
	DEFAULT_CONSUL_URL = "http://localhost:8500"
)

// ServiceRegisterBody represents the entire request body structure
type ServiceRegisterBody struct {
	ID      string       `json:"ID"`      // "[hostname]-[service_name]-[port]"
	Name    string       `json:"Name"`    // "[service_name]"
	Tags    []string     `json:"Tags"`    // [ "urlprefix-/[service_name] strip=/[service_name]" ]
	Address string       `json:"Address"` // "[ip_addr]"
	Port    int          `json:"Port"`    // [port]
	Check   ServiceCheck `json:"check"`
}

// ServiceCheck represents the check field in the request body
type ServiceCheck struct {
	HTTP                           string `json:"http"`                           // "http://[ip_addr]:[port][check_api]]"
	TLSSkipVerify                  bool   `json:"tls_skip_verify"`                // true
	Method                         string `json:"method"`                         // "GET",
	Interval                       string `json:"interval"`                       // "30s",
	Timeout                        string `json:"timeout"`                        // "5s",
	DeregisterCriticalServiceAfter string `json:"DeregisterCriticalServiceAfter"` // "10m"
}

func registerToConsul(serviceName string, port int, logger logs.Logger) error {
	err := registerToConsulWithAddress(DEFAULT_CONSUL_URL, serviceName, port, logger)
	if _, ok := err.(*url.Error); ok {
		err = registerToConsulWithAddress(system_args.GetConsulAddress(), serviceName, port, logger)
	}

	return err
}

func registerToConsulWithAddress(consulAddr, serviceName string, port int, logger logs.Logger) error {
	req, err := createRequest(consulAddr, serviceName, port, logger)
	if err != nil {
		return err
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	logger.LogInfo(fmt.Sprintf("Request %s Status: %s", req.URL, resp.Status), &logs.LogMetaData{Service: "System API"})
	statusOK := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	if !statusOK {
		return fmt.Errorf("invalid consul response: %s", resp.Body)
	}

	return nil
}

func createRequest(consulUrl, serviceName string, port int, logger logs.Logger) (*http.Request, error) {
	url := fmt.Sprintf("%s/v1/agent/service/register", consulUrl)
	body, err := buildConsulRegistrationBody(serviceName, port)
	if err != nil {
		return nil, err
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	logger.LogInfo(fmt.Sprintf("Sending request:\nMethod: %s\nURL: %s\nHeader: %v\nBody: %s",
		req.Method,
		req.URL.String(),
		req.Header,
		jsonBody),
		&logs.LogMetaData{Service: "System API"})
	return req, nil
}

func buildConsulRegistrationBody(serviceName string, port int) (*ServiceRegisterBody, error) {
	hostname, err := getLocalIPAddress()
	if err != nil {
		return nil, err
	}

	env := system_args.GetEnvironment()
	urlPrefix := ""
	if env != "" {
		urlPrefix = fmt.Sprintf("%s-", env)
	}

	serviceCheckUrl := fmt.Sprintf("http://%s:%d%s", hostname, port, check_api)
	serviceID := fmt.Sprintf("%s-%s-%d", hostname, serviceName, port)

	lowerCaseServiceName := strings.ToLower(serviceName)
	serviceTags := []string{fmt.Sprintf("urlprefix-/%s%s strip=/%s%s", urlPrefix, lowerCaseServiceName, urlPrefix, lowerCaseServiceName)}

	serviceCheck := ServiceCheck{
		HTTP:                           serviceCheckUrl, // "http://[ip_addr]:[port][check_api]]"
		TLSSkipVerify:                  true,
		Method:                         "GET",
		Interval:                       "30s",
		Timeout:                        "5s",
		DeregisterCriticalServiceAfter: "10m",
	}

	body := &ServiceRegisterBody{
		ID:      serviceID,   // "[hostname]-[service_name]-[port]"
		Name:    serviceName, // "[service_name]"
		Tags:    serviceTags, // [ "urlprefix-/[env]-[service_name] strip=/[env]-[service_name]" ]
		Address: hostname,    // "[ip_addr]"
		Port:    port,        // [port]
		Check:   serviceCheck,
	}

	return body, nil
}

func getLocalIPAddress() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String(), nil
}

type ServiceStatus struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Build   string `json:"build"`
	Label   string `json:"label"`
}

func Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	response := ServiceStatus{
		Status:  "Up and running :-)",
		Version: ldflags.Version(),
		Build:   ldflags.Time(),
		Label:   ldflags.Build(),
	}

	json.NewEncoder(w).Encode(response)
}

func Register(router *chi.Mux, serviceName string, port int, logger logs.Logger) {
	if system_args.ShouldRegisterToConsul() {
		err := registerToConsul(serviceName, port, logger)
		if err != nil {
			logger.LogError(err.Error(), &logs.LogMetaData{Service: "System API"})
		}
	}

	router.Get(check_api, Status)
}
