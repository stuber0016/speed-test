# One build stage compiles all binaries. The client image also carries the
# stctl CLI so it can be run with `docker compose exec client /stctl ...`.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot AS target
COPY --from=build /out/target /target
EXPOSE 8081
ENTRYPOINT ["/target"]

FROM gcr.io/distroless/static-debian12:nonroot AS client
COPY --from=build /out/client /client
COPY --from=build /out/stctl /stctl
EXPOSE 8080
ENTRYPOINT ["/client"]
