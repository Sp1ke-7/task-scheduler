FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:latest
WORKDIR /root/
COPY --from=builder /app/main .
EXPOSE 8080
CMD ["./main"]