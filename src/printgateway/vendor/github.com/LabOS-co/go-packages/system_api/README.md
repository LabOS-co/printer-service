# system_api

`system_api` is a lightweight Go package designed to simplify the process of adding common system HTTP endpoints to your applications. It provides two main functions, `Run` and `Register`, catering to both non-server and server applications.

## Installation

To use `system_api` in your Go project, you can install it using the following:

go get -u github.com/LabOS-co/go-packages/system_api

### Usage
1. For Server Applications (Using Register)
If you are building a server application and already have an HTTP router, you can use the Register function to add common system HTTP endpoints to your existing router.

```
package main

import (
	"github.com/LabOS-co/go-packages/system_api"
	"github.com/go-chi/chi/v5"
)

func main() {
    //create a logger using github.com/LabOS-co/go-packages/logs package
	logger, err := logs.GetLogger("", 0, "TestService")
	if err != nil {
		fmt.Printf("Can't get logger")
	}

    // Create your own router
    router := mux.NewRouter()

    //configure port
    port := 1234

    // Register system API endpoints to the existing router
    system_api.Register(router, "TestServiceName", port, logger)

    // Your additional routes and application logic go here

    // Start your HTTP server
    // For example, using http.ListenAndServe
    http.ListenAndServe(fmt.Sprintf(":%d", port), router)
}
```

#### Common Endpoints
The system_api package provides the following common HTTP endpoints:
* /status: Returns a simple health status indicating that the system is running.
* consul registration: the package will automatically register your service to our systems consul. First it will try to use localhost, if unsuccessful it will try to use the host provided in "consul-addr" command argument or "CONSUL_ADDR" environment variable.

#### Contributing
If you find any issues, have suggestions, or want to contribute, please open an issue or submit a pull request.