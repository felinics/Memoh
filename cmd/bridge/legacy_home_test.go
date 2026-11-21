package main

import "testing"

func TestLegacyHomeIsAdoptedOnlyFromAContainerThatLivedElsewhere(t *testing.T) {
	// Created with HOME=/data: the image's home was never the user's.
	if _, ok := legacyHomeToAdopt("/data"); ok {
		t.Fatal("a container created with HOME=/data adopts the image's home")
	}
	// Created before HOME=/data: the user lived in the image's home.
	if old, ok := legacyHomeToAdopt("/root"); !ok || old != "/root" {
		t.Fatalf("legacyHomeToAdopt(/root) = %q, %v", old, ok)
	}
	// No HOME at all: the passwd entry decides.
	if old, ok := legacyHomeToAdopt(""); !ok || old != "" {
		t.Fatalf("legacyHomeToAdopt(\"\") = %q, %v", old, ok)
	}
}
