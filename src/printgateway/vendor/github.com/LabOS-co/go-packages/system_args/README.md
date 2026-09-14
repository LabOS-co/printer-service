# system_args

`system_args` package provides a simple and convenient way to handle command-line arguments and environment variables in your Go applications.

## Installation

To use `system_args` in your Go project, you can install it using the following:
```
go get -u github.com/LabOS-co/go-packages/system_args
```

### Usage
#### GetPort
To retrieve the port number from command-line arguments, use the GetPort function. If the port is not provided as a command-line argument, the default port will be 8082.

```
package main

import (
	"github.com/LabOS-co/go-packages/system_args"
)

func main() {

    // Get the system port number, default port 8082
    port := system_args.GetPort()   

    // Your application logic goes here
}
```

#### Environment Name
To retrieve the environment name from either command-line arguments or an environment variable, use the GetEnvironment function. If the environment name is not provided as a command-line argument, the function will check the environment variable with the key "LABOS-ENV."

```
package main

import (
	"github.com/LabOS-co/go-packages/system_args"
	"github.com/go-chi/chi/v5"
)

func main() {

    //Get environment name, default env name is ""
    env := system_args.GetEnvironment()
}
```

### Command-line flags
Command-line Flags
The following command-line flags are available:

-port: Specify the port number for the server to run on. Default is 8082.
-env: Specify the environment name.
Example usage:
```
$ your_app -port=9090 -env=production
```

### Environment variable
If the environment name is not provided via command-line arguments, the package will attempt to retrieve it from the "LABOS-ENV" environment variable.
```
$ export LABOS-ENV=dev
$ your_app
```

#### Contributing
If you find any issues, have suggestions, or want to contribute, please open an issue or submit a pull request.