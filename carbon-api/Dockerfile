FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /carbon-api ./cmd

FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
COPY --from=build /carbon-api /usr/local/bin/carbon-api
USER 1000
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/carbon-api"]
