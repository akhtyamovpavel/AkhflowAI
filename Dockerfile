FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/akhflow ./cmd/akhflow

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 akhflow
COPY --from=build /out/akhflow /usr/local/bin/akhflow
USER akhflow
EXPOSE 8080
ENTRYPOINT ["akhflow"]
