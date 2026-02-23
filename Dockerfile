FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
# Strip local replace directive so Go fetches mache from GitHub.
# The replace stays in go.mod for local dev — this only affects the Docker build.
RUN sed -i '/^replace.*mache/d' go.mod && go mod tidy
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o agentd ./cmd/agentd

FROM gcr.io/distroless/static-debian12
COPY --from=builder /app/agentd /agentd
EXPOSE 8080
ENTRYPOINT ["/agentd"]
