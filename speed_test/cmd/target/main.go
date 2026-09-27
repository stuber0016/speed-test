// Command target runs the Target service that speed tests measure against.
//
// Environment:
//
//	LISTEN_ADDR   address to listen on (default ":$TARGET_PORT", TARGET_PORT defaulting to 8081)
//	TARGET_TOKEN  bearer token the Client service must present (required)
package main

import (
	"flag"
	"log"
	"os"

	"speedtest/internal/service"
	"speedtest/internal/target"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the running service's /healthz and exit")
	flag.Parse()

	addr := service.Env("LISTEN_ADDR", ":"+service.Env("TARGET_PORT", "8081"))
	if *healthcheck {
		os.Exit(service.Healthcheck(addr))
	}

	token := service.MustEnv("TARGET_TOKEN")
	ctx, stop := service.SignalContext()
	defer stop()
	if err := service.Serve(ctx, "target", addr, target.New().Handler(token)); err != nil {
		log.Fatal(err)
	}
}
