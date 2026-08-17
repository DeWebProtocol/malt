package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeDaemonHTTPCancelsActiveHandlerBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	exited := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(exited)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveDaemonHTTP(ctx, server, listener) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 2 * time.Second}
		response, _ := client.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("daemon handler did not start")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("daemon handler context was not canceled")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon HTTP server did not stop")
	}
	<-requestDone
}

func TestServeDaemonHTTPForceClosesAfterGracefulDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveDaemonHTTPWithTimeout(ctx, server, listener, 20*time.Millisecond) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: time.Second}
		response, _ := client.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("daemon handler did not start")
	}
	cancel()
	select {
	case err := <-serveDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("serve error=%v, want graceful deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon HTTP server remained blocked after graceful deadline")
	}
	close(release)
	<-requestDone
}
