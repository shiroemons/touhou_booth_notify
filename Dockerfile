FROM golang:1.18-alpine as builder

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

WORKDIR /go/src/app
COPY . /go/src/app

RUN go mod download
RUN go clean -cache \
  && go build -o /go/bin/app

FROM gcr.io/distroless/base

COPY --from=builder /go/bin/app /

CMD ["/app"]
