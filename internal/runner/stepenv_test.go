package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// parseEnvFile tests

func TestParseEnvFile_SimpleKV(t *testing.T) {
	m, err := parseEnvFile([]byte("FOO=bar\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["FOO"] != "bar" {
		t.Errorf("got %q, want %q", m["FOO"], "bar")
	}
}

func TestParseEnvFile_MultiplePairs(t *testing.T) {
	m, err := parseEnvFile([]byte("A=1\nB=2\nC=3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "2" || m["C"] != "3" {
		t.Errorf("unexpected map: %v", m)
	}
}

func TestParseEnvFile_EmptyValue(t *testing.T) {
	m, err := parseEnvFile([]byte("EMPTY=\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m["EMPTY"]; !ok || v != "" {
		t.Errorf("got %q (ok=%v), want empty string", v, ok)
	}
}

func TestParseEnvFile_ValueWithEquals(t *testing.T) {
	m, err := parseEnvFile([]byte("KEY=a=b=c\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["KEY"] != "a=b=c" {
		t.Errorf("got %q, want %q", m["KEY"], "a=b=c")
	}
}

func TestParseEnvFile_HeredocSingleLine(t *testing.T) {
	data := "KEY<<EOF\nhello\nEOF\n"
	m, err := parseEnvFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := "hello\n"
	if m["KEY"] != want {
		t.Errorf("got %q, want %q", m["KEY"], want)
	}
}

func TestParseEnvFile_HeredocMultiLine(t *testing.T) {
	data := "MULTILINE<<DELIM\nfirst line\nsecond line\nDELIM\n"
	m, err := parseEnvFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := "first line\nsecond line\n"
	if m["MULTILINE"] != want {
		t.Errorf("got %q, want %q", m["MULTILINE"], want)
	}
}

func TestParseEnvFile_MixedSimpleAndHeredoc(t *testing.T) {
	data := "SIMPLE=value\nMULTI<<EOF\nline1\nline2\nEOF\nANOTHER=x\n"
	m, err := parseEnvFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if m["SIMPLE"] != "value" {
		t.Errorf("SIMPLE: got %q, want %q", m["SIMPLE"], "value")
	}
	if m["MULTI"] != "line1\nline2\n" {
		t.Errorf("MULTI: got %q, want %q", m["MULTI"], "line1\nline2\n")
	}
	if m["ANOTHER"] != "x" {
		t.Errorf("ANOTHER: got %q, want %q", m["ANOTHER"], "x")
	}
}

func TestParseEnvFile_Empty(t *testing.T) {
	m, err := parseEnvFile([]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// ReadDelta tests

func makeRunnerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func TestReadDelta_Empty(t *testing.T) {
	dir := makeRunnerDir(t)
	ef, err := NewEnvFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	env, outputs, paths, err := ef.ReadDelta()
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 || len(outputs) != 0 || len(paths) != 0 {
		t.Errorf("expected all empty, got env=%v outputs=%v paths=%v", env, outputs, paths)
	}
}

func TestReadDelta_OffsetTracking(t *testing.T) {
	dir := makeRunnerDir(t)
	ef, err := NewEnvFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Write first entry
	appendFile(t, filepath.Join(dir, "_env"), "FIRST=one\n")

	env, _, _, err := ef.ReadDelta()
	if err != nil {
		t.Fatal(err)
	}
	if env["FIRST"] != "one" {
		t.Errorf("got %q, want %q", env["FIRST"], "one")
	}

	// Write second entry
	appendFile(t, filepath.Join(dir, "_env"), "SECOND=two\n")

	env2, _, _, err := ef.ReadDelta()
	if err != nil {
		t.Fatal(err)
	}
	// Second call must NOT return FIRST again
	if _, ok := env2["FIRST"]; ok {
		t.Error("second ReadDelta returned FIRST, which should have been consumed")
	}
	if env2["SECOND"] != "two" {
		t.Errorf("got %q, want %q", env2["SECOND"], "two")
	}
}

func TestReadDelta_PathFile(t *testing.T) {
	dir := makeRunnerDir(t)
	ef, err := NewEnvFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	appendFile(t, filepath.Join(dir, "_path"), "/usr/local/bin\n/opt/mytools/bin\n")

	_, _, paths, err := ef.ReadDelta()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/usr/local/bin" || paths[1] != "/opt/mytools/bin" {
		t.Errorf("unexpected paths: %v", paths)
	}
}

func TestReadDelta_OutputFile(t *testing.T) {
	dir := makeRunnerDir(t)
	ef, err := NewEnvFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	appendFile(t, filepath.Join(dir, "_output"), "result=42\n")

	_, outputs, _, err := ef.ReadDelta()
	if err != nil {
		t.Fatal(err)
	}
	if outputs["result"] != "42" {
		t.Errorf("got %q, want %q", outputs["result"], "42")
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
