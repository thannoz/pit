FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -o /teashop .

FROM alpine:3.22
COPY --from=build /teashop /teashop
CMD ["/teashop"]
