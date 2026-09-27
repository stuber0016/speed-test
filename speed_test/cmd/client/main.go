// Command client runs the Client service, which orchestrates speed tests
// against a Target service and exposes them over an HTTP API and web page.
//
// Environment:
//
//	LISTEN_ADDR     address to listen on (default ":$CLIENT_PORT", CLIENT_PORT defaulting to 8080)
//	TARGET_URL      base URL of the Target service (default "http://localhost:$TARGET_PORT",
//	                TARGET_PORT defaulting to 8081)
//	TARGET_TOKEN    bearer token presented to the Target (required)
//	CLIENT_API_KEY  bearer token controllers must present to this service (required)
package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"speedtest/internal/client"
	"speedtest/internal/service"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the running service's /healthz and exit")
	flag.Parse()

	addr := service.Env("LISTEN_ADDR", ":"+service.Env("CLIENT_PORT", "8080"))
	if *healthcheck {
		os.Exit(service.Healthcheck(addr))
	}

	targetURL := strings.TrimRight(service.Env("TARGET_URL", "http://localhost:"+service.Env("TARGET_PORT", "8081")), "/")
	runner := client.NewRunner(targetURL, service.MustEnv("TARGET_TOKEN"))
	apiKey := service.MustEnv("CLIENT_API_KEY")

	ctx, stop := service.SignalContext()
	defer stop()
	log.Printf("testing against %s", targetURL)
	if err := service.Serve(ctx, "client", addr, client.NewAPI(ctx, runner, client.NewStore(20), apiKey)); err != nil {
		log.Fatal(err)
	}
}
