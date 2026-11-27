package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPlaceholders(t *testing.T) {
	cfg := &ProgramConfig{
		Arguments: `-k {token} -s {server} -n "{name}" {stub}`,
		Args: []ArgItem{
			{Key: "token", Value: "abc123"},
			{Key: "server", Value: "example.com:8004"},
			{Key: "name", Value: "My PC"},
			{Key: "stub", Value: ""},
		},
	}
	got := cfg.buildCommandLine()
	// 已定义的占位符（即使值为空）会被替换；未定义的才原样保留。
	want := `-k abc123 -s example.com:8004 -n "My PC"`
	if got != want {
		t.Fatalf("buildCommandLine() = %q, want %q", got, want)
	}

	undefined := &ProgramConfig{Arguments: "-x {missing}"}
	if got := undefined.buildCommandLine(); got != "-x {missing}" {
		t.Fatalf("undefined placeholder = %q", got)
	}
}

func TestStopCommand(t *testing.T) {
	cfg := &ProgramConfig{StopCommand: "--stop {token}", Args: []ArgItem{{Key: "token", Value: "x"}}}
	if got := cfg.buildStopCommand(); got != "--stop x" {
		t.Fatalf("buildStopCommand() = %q", got)
	}
}

func TestParseKeyValue(t *testing.T) {
	cfg := &ProgramConfig{ProbeParser: "KeyValue", ProbeOKKey: "Connection status", ProbeOKValue: "Connected"}
	out := "Name: pc\r\nVirtual ip: 192.168.1.2\r\nConnection status: Connected\r\nNAT type: Cone"
	r := parseProbe(cfg, out)
	if len(r.Fields) != 4 {
		t.Fatalf("fields = %d, want 4", len(r.Fields))
	}
	if r.Level != ProbeOK {
		t.Fatalf("level = %v, want ProbeOK", r.Level)
	}

	bad := parseProbe(cfg, "Connection status: Disconnected")
	if bad.Level != ProbeBad {
		t.Fatalf("level = %v, want ProbeBad", bad.Level)
	}

	// 大小写不敏感
	ci := parseProbe(cfg, "connection STATUS:   connected ")
	if ci.Level != ProbeOK {
		t.Fatalf("case-insensitive level = %v, want ProbeOK", ci.Level)
	}
}

func TestParseJSON(t *testing.T) {
	cfg := &ProgramConfig{ProbeParser: "Json"}
	out := `{"name":"pc","status":{"connected":true},"peers":["a","b"]}`
	r := parseProbe(cfg, out)
	if len(r.Fields) != 3 {
		t.Fatalf("fields = %d, want 3 (got %+v)", len(r.Fields), r.Fields)
	}
	found := map[string]string{}
	for _, f := range r.Fields {
		found[f.Key] = f.Value
	}
	if found["status.connected"] != "true" {
		t.Fatalf("nested flatten = %q", found["status.connected"])
	}
	if found["peers"] != "a,b" {
		t.Fatalf("array join = %q", found["peers"])
	}
}

func TestParseLinesAndEmpty(t *testing.T) {
	cfg := &ProgramConfig{ProbeParser: "Lines"}
	r := parseProbe(cfg, "line1\n\nline2\n")
	if len(r.Fields) != 2 || r.Level != ProbeOK {
		t.Fatalf("lines: fields=%d level=%v", len(r.Fields), r.Level)
	}
	empty := parseProbe(cfg, "")
	if empty.Level != ProbeUnknown || len(empty.Fields) != 0 {
		t.Fatalf("empty should be unknown and fieldless")
	}
}

func TestParseArgsText(t *testing.T) {
	items := parseArgsText("token=abc\r\nserver=a=b\r\n\r\n# comment\r\nbadline\r\nempty=\r\n")
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3: %+v", len(items), items)
	}
	if items[1].Value != "a=b" {
		t.Fatalf("value with = = %q", items[1].Value)
	}
}

func TestResolveExePath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "demo.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &ProgramConfig{ExePath: "demo.exe"}
	if got := cfg.resolveExePath(dir); !strings.EqualFold(got, exe) {
		t.Fatalf("relative resolve = %q, want %q", got, exe)
	}
	// PATH 查找
	pcfg := &ProgramConfig{ExePath: "cmd.exe"}
	got := pcfg.resolveExePath(dir)
	if !strings.HasSuffix(strings.ToLower(got), "cmd.exe") || !isFile(got) {
		t.Fatalf("PATH resolve = %q", got)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	cfg := &AppConfig{
		AutoStartManager: true,
		MinimizeToTray:   false,
		Programs: []*ProgramConfig{{
			ID: "id1", Name: "测试程序", ExePath: `C:\tools\app.exe`,
			Arguments:  "-k {token}",
			Args:       []ArgItem{{Key: "token", Value: "中文 & 符号\"引号"}},
			RunAsAdmin: true, AutoRestart: true, AutoStart: true,
			StopTimeout: 8000, RestartDelay: 3000,
			ProbeEnabled: true, ProbeCommand: "--info", ProbeParser: "Json",
			ProbeOKKey: "status.connected", ProbeOKValue: "true", ProbeInterval: 2000,
		}},
	}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	loaded := loadConfig()
	if len(loaded.Programs) != 1 {
		t.Fatalf("programs = %d", len(loaded.Programs))
	}
	p := loaded.Programs[0]
	if p.Name != "测试程序" || p.Args[0].Value != "中文 & 符号\"引号" {
		t.Fatalf("roundtrip mismatch: %+v", p)
	}
	if !p.RunAsAdmin || !p.AutoRestart || p.StopTimeout != 8000 || p.ProbeParser != "Json" {
		t.Fatalf("flags/numbers mismatch: %+v", p)
	}
	if !loaded.AutoStartManager || loaded.MinimizeToTray {
		t.Fatalf("global mismatch")
	}

	// 损坏配置退回默认值
	path, _ := configPath()
	_ = os.WriteFile(path, []byte("{ not json"), 0o644)
	broken := loadConfig()
	if broken.Programs == nil || len(broken.Programs) != 0 {
		t.Fatalf("broken config should fall back to empty")
	}
}
