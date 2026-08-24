// Initial prototype Print Gateway.
//
// Accepts a print request over HTTP, either multipart/form-data (file
// attached) or application/json ({"printer","file_url"} — server downloads
// the file itself). Either way, once the file is on local disk it's handed
// to CUPS via `lp -d <printer> <path>` — this server does not talk IPP
// itself and does not know about PPDs/media/resolution; CUPS's own queue
// configuration (already set up, static PPD, ippfix if that printer needs
// it) handles all of that. See internal/httpapi for the request contract.
//
// This file is wiring only: build the dependencies, then start the server.
package main

import (
	"fmt"
	"os"

	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/config"
	"printgateway/internal/cups"
	"printgateway/internal/fetch"
	"printgateway/internal/httpapi"
	"printgateway/internal/printgw"
)

func main() {
	cfg := config.Load(os.Args, os.Getenv)
	logger := logs.GetConsoleLogger()

	svc := printgw.NewService(cups.NewLPSubmitter(), fetch.NewHTTPFetcher())
	api := httpapi.New(cfg, logger, svc)
	server := httpapi.NewServer(api)

	logger.LogInfo(fmt.Sprintf("print gateway (prototype) listening on %s", cfg.Addr), api.MetaData())
	if err := server.ListenAndServe(); err != nil {
		logger.LogError(fmt.Sprintf("server exited: %v", err), api.MetaData())
		os.Exit(1)
	}
}
