# linkpeer-signal: build a static pure-Go binary.
# CGO_ENABLED=0 keeps it a single static binary matching hiq's
# "zero runtime deps, cross-platform" ethos (ENGINEERING §1.4).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/linkpeer-signal cmd/linkpeer-signal
COPY internal/linkpeersignal internal/linkpeersignal
# only the signal binary is needed; it depends on stdlib + gorilla/websocket + toml
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /signal ./cmd/linkpeer-signal

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /signal /signal
ENTRYPOINT ["/signal"]
CMD ["-config", "/etc/linkpeer/signal.toml"]
