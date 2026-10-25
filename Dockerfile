FROM golang:1.26.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOWORK=off go mod download
COPY . ./
RUN GOWORK=off go build -mod=readonly -trimpath -ldflags "-s -w" -o /out/domainry-im ./cmd/im-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /out/domainry-im /app/domainry-im
USER app
ENV APP_ENV=production \
    DOMAINRY_IM_LISTEN_ADDRESS=:8092 \
    DOMAINRY_IM_DATABASE_DRIVER=postgres
EXPOSE 8092
ENTRYPOINT ["/app/domainry-im"]
