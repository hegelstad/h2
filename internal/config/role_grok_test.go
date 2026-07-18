package config

import (
	"strings"
	"testing"
)

func TestValidate_GrokPermissionMode(t *testing.T) {
	valid := &Role{RoleName: "r", GrokPermissionMode: "dontAsk"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid grok_permission_mode rejected: %v", err)
	}
	invalid := &Role{RoleName: "r", GrokPermissionMode: "yolo"}
	err := invalid.Validate()
	if err == nil || !strings.Contains(err.Error(), "grok_permission_mode") {
		t.Fatalf("expected grok_permission_mode error, got %v", err)
	}
}

func TestValidate_GrokHarnessType(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "grok"}
	if err := r.Validate(); err != nil {
		t.Fatalf("agent_harness grok rejected: %v", err)
	}
}

func TestGetGrokConfigPathPrefix_Default(t *testing.T) {
	r := &Role{RoleName: "r"}
	if got := r.GetGrokConfigPathPrefix(); !strings.HasSuffix(got, "grok-config") {
		t.Fatalf("GetGrokConfigPathPrefix() = %q, want */grok-config", got)
	}
	r2 := &Role{RoleName: "r", GrokConfigPathPrefix: "/custom/prefix"}
	if got := r2.GetGrokConfigPathPrefix(); got != "/custom/prefix" {
		t.Fatalf("GetGrokConfigPathPrefix() = %q, want /custom/prefix", got)
	}
}
