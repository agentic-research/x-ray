FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS TARGETARCH
WORKDIR /app
COPY . .
# Strip local replace directive so Go fetches mache from GitHub.
# The replace stays in go.mod for local dev — this only affects the Docker build.
RUN sed -i '/^replace.*mache/d' go.mod && go mod tidy
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o agentd ./cmd/agentd

FROM gcr.io/distroless/static-debian12
COPY --from=builder /app/agentd /agentd
COPY --from=builder /app/static /static
EXPOSE 8080
ENTRYPOINT ["/agentd"]
