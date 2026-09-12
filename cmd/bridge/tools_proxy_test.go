package main

import "testing"

func TestToolsProxyAddrMustBeLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:18732", "localhost:18732", "[::1]:18732"} {
		if !isLoopbackTCPAddr(addr) {
			t.Fatalf("isLoopbackTCPAddr(%q) = false", addr)
		}
	}
	for _, addr := range []string{":18732", "0.0.0.0:18732", "10.88.0.1:18732", "example.com:18732", "127.0.0.1"} {
		if isLoopbackTCPAddr(addr) {
			t.Fatalf("isLoopbackTCPAddr(%q) = true", addr)
		}
	}
}

func TestBridgeTCPListenAddrAllowsEmptyHostBind(t *testing.T) {
	for _, addr := range []string{":9090", "127.0.0.1:9090", "localhost:9090", "[::1]:9090"} {
		if !isBridgeTCPListenAddrAllowed(addr) {
			t.Fatalf("isBridgeTCPListenAddrAllowed(%q) = false", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:9090", "10.88.0.1:9090", "example.com:9090", "127.0.0.1"} {
		if isBridgeTCPListenAddrAllowed(addr) {
			t.Fatalf("isBridgeTCPListenAddrAllowed(%q) = true", addr)
		}
	}
}
